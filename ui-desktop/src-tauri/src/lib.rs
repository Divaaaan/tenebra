//! Tauri shell. It owns the backend, exposes the control protocol as Tauri
//! commands, and bridges backend events onto the webview event bus.
//!
//! The backend is hidden behind the [`Backend`](backend::Backend) trait; see
//! `make_backend` for the single place a transport is chosen (the service's
//! named pipe when one is listening, the spawned sidecar otherwise, or the
//! in-process mock on request).

mod backend;
mod crash;
mod deeplink;
mod tray;
mod update_channel;

use std::sync::{Arc, Mutex};
#[cfg(any(windows, target_os = "linux"))]
use std::time::{Duration, Instant};

use serde_json::json;
use tauri::{AppHandle, Emitter, Manager, State as TauriState, WindowEvent};
use tauri_plugin_deep_link::DeepLinkExt;
use tauri_plugin_notification::NotificationExt;

use backend::{
    AttemptsSnapshot, Backend, ConnectionMode, ConnectionState, EventSink, ImportLinksResult,
    LeakCheck, NodeCheck, PingResult, Profile, RoutingMode, ServiceChecks, SpeedTest, SplitMode,
    State, StunCheck, TunStack, ZapretActive, ZapretBundle, ZapretPick, ZapretUpdate,
    EVENT_ATTEMPTS, EVENT_LOG, EVENT_PROFILES, EVENT_STATE, EVENT_TRAFFIC,
};

/// Held in Tauri's managed state and shared by every command handler. The
/// backend is an `Arc` rather than a `Box` so an async command can clone a
/// handle and run its blocking backend call on a worker thread (the trait is
/// `Send + Sync`), keeping the main/event-loop thread free.
struct AppState {
    backend: Arc<dyn Backend>,
}

/// The app's active UI language. It mirrors the front end's own localization so
/// the native surfaces the webview can't reach — the tray menu, its tooltip, and
/// desktop notifications — speak the same language as the rest of the app. The
/// front end pushes the current value on startup and on every change through the
/// `set_language` command.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
enum Lang {
    /// English — the default until the front end reports otherwise.
    #[default]
    En,
    /// Russian.
    Ru,
}

impl Lang {
    /// Map a front-end language code — the `"en"`/`"ru"` values persisted in
    /// `localStorage["tenebra.lang"]` — to a `Lang`. Anything unrecognised falls
    /// back to English, so a stray code never leaves a surface unlabelled.
    fn from_code(code: &str) -> Lang {
        match code {
            "ru" => Lang::Ru,
            _ => Lang::En,
        }
    }
}

/// Managed holder for the active [`Lang`], shared by the notification path and
/// the tray. Defaults to English; the front end overwrites it at startup.
#[derive(Default)]
struct LangState {
    lang: Mutex<Lang>,
}

/// The app's active language, read from managed state. `En` before the front end
/// has reported one (or if the state isn't managed yet), so every surface always
/// has a definite language to render in.
fn current_lang(app: &AppHandle) -> Lang {
    app.try_state::<LangState>()
        .map(|s| *s.lang.lock().unwrap())
        .unwrap_or_default()
}

/// Bridges backend events to the webview. The backend calls these; we forward
/// each onto the matching event channel with the protocol's exact payload shape.
struct TauriSink {
    app: AppHandle,
    /// The last connection state delivered here, so a desktop notification fires
    /// only on a real transition — not on every snapshot that leaves the state
    /// unchanged (e.g. a live kill-switch or tun re-apply).
    last_state: Mutex<Option<ConnectionState>>,
}

impl TauriSink {
    /// Show a desktop notification when the connection state meaningfully
    /// changes. Records the new state and, if the transition is noteworthy, shows
    /// the toast. Debounced against the previous state via [`transition_notice`].
    fn notify_transition(&self, state: &State) {
        let prev = {
            let mut last = self.last_state.lock().unwrap();
            let prev = *last;
            *last = Some(state.state);
            prev
        };
        if let Some((title, body)) = transition_notice(current_lang(&self.app), prev, state) {
            // A missing notification (permission denied, headless test host) is
            // non-fatal; the state event still drives the UI.
            let _ = self
                .app
                .notification()
                .builder()
                .title(title)
                .body(body)
                .show();
        }
    }
}

impl EventSink for TauriSink {
    fn state(&self, state: &State) {
        // The tray mirrors the live connection state (tooltip, icon, menu); this
        // is the one place backend state flows through, so we refresh it here and
        // fire any notification the transition warrants.
        tray::sync_state(&self.app, state);
        self.notify_transition(state);
        let _ = self.app.emit(EVENT_STATE, state);
    }

    fn traffic(&self, up: u64, down: u64, up_rate: u64, down_rate: u64) {
        let _ = self.app.emit(
            EVENT_TRAFFIC,
            json!({
                "up": up,
                "down": down,
                "up_rate": up_rate,
                "down_rate": down_rate,
            }),
        );
    }

    fn log(&self, level: &str, msg: &str) {
        let _ = self
            .app
            .emit(EVENT_LOG, json!({ "level": level, "msg": msg }));
    }

    fn profiles(&self) {
        // The stored profile set changed: refresh the tray's node submenu from the
        // new list, then tell the renderer to reload its own (payload-less).
        tray::sync_profiles(&self.app);
        let _ = self.app.emit(EVENT_PROFILES, ());
    }

    fn attempts(&self, snapshot: &AttemptsSnapshot) {
        // Forward the fallback-walk snapshot verbatim; the renderer renders the
        // anti-DPI attempt sequence from it.
        let _ = self.app.emit(EVENT_ATTEMPTS, snapshot);
    }
}

// =============================================================================
// Backend selection.
//
// The ONE place a transport is chosen, tried in order:
//
//  1. TENEBRA_MOCK=1 forces the in-process demo fake (UI work without the
//     core, or when the sidecar binary isn't built). Read by value, so an
//     explicit `0`/`off`/`false`/`no` — or an empty one — is not a request
//     for it; see mock_requested.
//  2. On Windows, if a core is already listening on the control pipe (the
//     installed service, or `tenebra-core --pipe` in a console), attach to it.
//     The tunnel then outlives this process and the GUI needs no elevation.
//     TENEBRA_PIPE renames the pipe or (`off`) skips it — see
//     backend::pipe::configured_name.
//  2'. On macOS and Linux, the same probe over the daemon's unix socket
//     (`/var/run/tenebra.sock` and `/run/tenebra.sock` respectively): if the
//     root daemon — the macOS LaunchDaemon, the Linux systemd service — is
//     listening, attach. TENEBRA_SOCKET renames the path or (`off`) skips it —
//     see backend::unix::configured_path.
//  3. Otherwise spawn the `tenebra-core` sidecar and own it — today's default
//     and the development path.
//
// If the sidecar fails to spawn (e.g. the binary is missing), we log and fall
// back to the mock rather than leaving the UI with no backend at all. Every
// choice implements the same `Backend` trait and is logged on the UI's own log
// channel, so nothing else in this file or the front end changes.
//
// The choice is made once and kept for the life of the process (the front end
// holds no notion of a transport, and a live sidecar tunnel cannot be handed to
// the service mid-run), which makes step 3 a consequential place to land by
// accident: an app-owned core keeps its profiles in the per-user store, so a
// user whose profiles live in the service's machine store sees an empty list
// and a Connect button that appears to do nothing. Two things guard against
// arriving there by mistake rather than by configuration: the dial itself waits
// out a service that is merely still starting (backend::pipe, and
// backend::unix where the platform warrants it), and the fallback is reported
// at warn with a plain description of what changed. Where a listener can be
// probed without displacing whoever holds it — Windows via WaitNamedPipeW,
// Linux via /proc/net/unix — we then keep watching for a while and say so if
// the service turns up late, so a user in that state is told a restart is all
// it takes. macOS has no such probe, so there the warning stands alone.
// =============================================================================
fn make_backend(app: &AppHandle, sink: Arc<dyn EventSink>) -> Arc<dyn Backend> {
    if mock_requested(std::env::var("TENEBRA_MOCK").ok().as_deref()) {
        return Arc::new(backend::mock::MockBackend::new(sink));
    }

    #[cfg(windows)]
    if let Some(name) = backend::pipe::configured_name() {
        match backend::pipe::PipeBackend::connect(&name, Arc::clone(&sink)) {
            Ok(backend) => {
                sink.log(
                    "info",
                    &format!("attached to the Tenebra service on {name}"),
                );
                return Arc::new(backend);
            }
            // Falling through to the sidecar is a working configuration (it is
            // the development path), but on an installed machine it is a
            // downgrade the user never asked for and cannot see from the UI, so
            // it is reported as a warning that names the consequences rather
            // than as a note about spawning a process.
            Err(e) => {
                sink.log(
                    "warn",
                    &format!(
                        "could not reach the Tenebra service on {name} ({e}); \
                         running this app's own core instead — profiles saved by the service \
                         are not visible here, and connecting in tun mode needs \
                         administrator rights"
                    ),
                );
                watch_for_a_late_service(name, Arc::clone(&sink));
            }
        }
    }

    #[cfg(any(target_os = "macos", target_os = "linux"))]
    if let Some(path) = backend::unix::configured_path() {
        match backend::unix::UnixBackend::connect(&path, Arc::clone(&sink)) {
            Ok(backend) => {
                sink.log("info", &format!("attached to the Tenebra daemon on {path}"));
                return Arc::new(backend);
            }
            // As on Windows: a working configuration, but on an installed
            // machine a downgrade the user cannot see from the UI, so it is
            // reported as a warning naming what changed.
            Err(e) => {
                sink.log(
                    "warn",
                    &format!(
                        "could not reach the Tenebra daemon on {path} ({e}); \
                         running this app's own core instead — profiles saved by the daemon \
                         are not visible here, and connecting in tun mode needs \
                         root privileges"
                    ),
                );
                #[cfg(target_os = "linux")]
                watch_for_a_late_daemon(path, Arc::clone(&sink));
            }
        }
    }

    // Both the core and sing-box must resolve to an absolute, bundled path. If
    // either can't be located we fail closed to the demo backend rather than let
    // a bare name resolve from the current directory or PATH — a spawn against a
    // planted `tenebra-core`/`sing-box` in an attacker-chosen CWD would otherwise
    // run untrusted code with the app's privileges.
    let program = match backend::sidecar::SidecarBackend::default_program() {
        Ok(p) => p,
        Err(e) => {
            sink.log(
                "error",
                &format!("could not locate tenebra-core, using demo backend: {e}"),
            );
            return Arc::new(backend::mock::MockBackend::new(sink));
        }
    };
    let singbox = match singbox_path(app) {
        Ok(p) => p,
        Err(e) => {
            sink.log(
                "error",
                &format!("could not locate sing-box, using demo backend: {e}"),
            );
            return Arc::new(backend::mock::MockBackend::new(sink));
        }
    };
    match backend::sidecar::SidecarBackend::spawn(program, singbox, Arc::clone(&sink)) {
        Ok(backend) => Arc::new(backend),
        Err(e) => {
            sink.log(
                "error",
                &format!("could not start tenebra-core, using demo backend: {e}"),
            );
            Arc::new(backend::mock::MockBackend::new(sink))
        }
    }
}

/// How long the app keeps an eye out for a service that started after it did,
/// and how often it looks. The window covers the cases where the fallback was a
/// lost race rather than a verdict — an installer's `sc start` or `systemctl
/// start`, a boot where the service manager was slow, a service started by hand
/// right after the app — and then stops: a machine that genuinely has no service
/// should not carry a polling thread for the life of the process. The tick is
/// deliberately lazy; nothing here depends on catching the transition promptly,
/// only on catching it at all.
#[cfg(any(windows, target_os = "linux"))]
const LATE_SERVICE_WATCH: Duration = Duration::from_secs(60);
#[cfg(any(windows, target_os = "linux"))]
const LATE_SERVICE_TICK: Duration = Duration::from_secs(2);

/// Watch for a service that comes up after this app already committed to its own
/// core, and say so once if it does.
///
/// This app cannot promote itself onto the service mid-run: the sidecar it
/// spawned may be carrying a live tunnel, and dropping that to attach elsewhere
/// would take the user's connection down without being asked. What it can do is
/// stop the state from being silent — a relaunch is all it takes, and the user
/// has no way to know that from a UI that simply shows no profiles. The watch
/// lives on its own thread, ends with [`LATE_SERVICE_WATCH`], and probes without
/// dialing (see [`backend::pipe::is_listening`]) so it never displaces the
/// session of whatever client the service is actually serving.
#[cfg(windows)]
fn watch_for_a_late_service(name: String, sink: Arc<dyn EventSink>) {
    // A thread that cannot be spawned costs the user nothing but this notice.
    let _ = std::thread::Builder::new()
        .name("tenebra-service-watch".into())
        .spawn(move || {
            let appeared = await_probe(
                || backend::pipe::is_listening(&name),
                LATE_SERVICE_TICK,
                LATE_SERVICE_WATCH,
            );
            if appeared {
                sink.log(
                    "warn",
                    &format!(
                        "the Tenebra service is listening on {name} now, but this session is \
                         already running the app's own core; restart Tenebra to control the \
                         service and see the profiles saved there"
                    ),
                );
            }
        });
}

/// Watch for a daemon that comes up after this app already committed to its own
/// core, and say so once if it does. The Linux half of
/// [`watch_for_a_late_service`], for the same reason and with the same limits;
/// it probes the kernel's socket table rather than dialing (see
/// [`backend::unix::is_listening`]), so it never displaces the session of
/// whatever client the daemon is actually serving.
#[cfg(target_os = "linux")]
fn watch_for_a_late_daemon(path: String, sink: Arc<dyn EventSink>) {
    // A thread that cannot be spawned costs the user nothing but this notice.
    let _ = std::thread::Builder::new()
        .name("tenebra-daemon-watch".into())
        .spawn(move || {
            let appeared = await_probe(
                || backend::unix::is_listening(&path),
                LATE_SERVICE_TICK,
                LATE_SERVICE_WATCH,
            );
            if appeared {
                sink.log(
                    "warn",
                    &format!(
                        "the Tenebra daemon is listening on {path} now, but this session is \
                         already running the app's own core; restart Tenebra to control the \
                         daemon and see the profiles saved there"
                    ),
                );
            }
        });
}

/// Poll `probe` every `tick` until it answers true or `window` runs out,
/// reporting whether it ever did. Split out from the watch thread so its
/// schedule — look first, then wait, and always look at least once — can be
/// tested without a real pipe, a real socket, or real seconds.
#[cfg(any(windows, target_os = "linux"))]
fn await_probe(mut probe: impl FnMut() -> bool, tick: Duration, window: Duration) -> bool {
    let deadline = Instant::now() + window;
    loop {
        if probe() {
            return true;
        }
        if Instant::now() >= deadline {
            return false;
        }
        std::thread::sleep(tick);
    }
}

/// Whether `TENEBRA_MOCK` asks for the in-process demo backend, judged by its
/// *value*: unset or empty means no, and so do the usual ways of writing "off"
/// (`0`, `off`, `false`, `no`, in any case, with surrounding whitespace
/// ignored). Anything else arms the fake.
///
/// The same on/off convention `backend::pipe::configured_name` applies to
/// `TENEBRA_PIPE`, and for the same reason: a variable is set to a value, not
/// merely present, and `TENEBRA_MOCK=0` unmistakably means "no mock". Testing
/// presence alone made every one of those spellings arm it — and the mock is
/// compiled into release builds too (see `backend/mod.rs`), so that was a live
/// footgun, not just a development annoyance: a user with the variable parked at
/// `0` in their environment would get a plausible, entirely fictional app.
fn mock_requested(value: Option<&str>) -> bool {
    match value.map(str::trim) {
        None | Some("") => false,
        Some(v) => !matches!(
            v.to_ascii_lowercase().as_str(),
            "0" | "off" | "false" | "no"
        ),
    }
}

/// Path passed to the core as TENEBRA_SINGBOX so it can locate sing-box. An
/// explicit env var wins (handy in development); otherwise we use the copy
/// shipped beside the app as a bundle resource. The bundled binary is named per
/// platform: `sing-box.exe` on Windows (with wintun.dll in the same directory
/// for the tun device to load), plain `sing-box` elsewhere.
///
/// The core derives more than the executable from this path: the bundled
/// rule-sets (`geoip-ru.srs` and friends) are looked up in the same directory,
/// so whatever answers here has to be the directory the whole payload was laid
/// down in.
///
/// Fails closed: if no candidate resolves to an absolute path that exists, we
/// return an error instead of falling back to a bare name. A bare `sing-box`
/// would be resolved by the core relative to its CWD (and then PATH), so a
/// `sing-box` planted in an attacker-chosen working directory could be launched
/// with the tunnel's privileges. Every candidate here is absolute — Tauri's
/// `resolve` against the Resource base directory is rooted at the app bundle,
/// and the packaged locations are literal system paths — so none can be
/// redirected by the CWD. The `TENEBRA_SINGBOX` override is operator-supplied,
/// not webview-reachable, so it stays trusted.
fn singbox_path(app: &AppHandle) -> Result<std::path::PathBuf, String> {
    if let Some(p) = std::env::var_os("TENEBRA_SINGBOX") {
        return Ok(std::path::PathBuf::from(p));
    }
    #[cfg(windows)]
    let name = "sing-box.exe";
    #[cfg(not(windows))]
    let name = "sing-box";
    let resource = format!("resources/{name}");

    let mut tried: Vec<String> = Vec::new();
    let resolved = app
        .path()
        .resolve(&resource, tauri::path::BaseDirectory::Resource)
        .map_err(|e| {
            format!(
                "cannot resolve the bundled sing-box resource ({resource}): {e}; \
                 refusing to fall back to a bare name resolved from CWD/PATH"
            )
        })?;
    if resolved.exists() {
        return Ok(resolved);
    }
    tried.push(resolved.display().to_string());

    for candidate in packaged_resource_paths(name) {
        if candidate.exists() {
            return Ok(candidate);
        }
        tried.push(candidate.display().to_string());
    }

    Err(format!(
        "no bundled sing-box found (looked for {}); \
         refusing to fall back to a bare name resolved from CWD/PATH",
        tried.join(", ")
    ))
}

/// Where a distribution package may have put the payload instead of the layout
/// Tauri's own bundler produces.
///
/// Tauri resolves resources relative to the bundle it built (`/usr/lib/Tenebra`
/// from the .deb, the mount point inside an AppImage), which is exactly right
/// for those two and useless for a native package built without the bundler: a
/// distribution package spreads the same payload across the filesystem
/// hierarchy, with the launcher in `<prefix>/bin` and the private helpers in a
/// per-package directory. Arch ships one of those, and it carries its own
/// sing-box — the binary is not in the official repositories — so the resources
/// really are somewhere under `/usr/lib/tenebra` rather than beside the app.
///
/// The system directories are the ones the core walks for the same payload
/// (`adapters/linux.InstallDirs`), in the same order, so both ends of the handoff
/// agree on where a package may have put things: `<prefix>/{lib,libexec,share}
/// /tenebra` derived from the running executable, then the same three under
/// `/usr` as an absolute backstop. Each is tried with and without the
/// `resources/` sub-directory the bundler adds. The core's list opens with the
/// executable's own directory, which here is already covered by the Tauri
/// resolve this runs after; it closes with a `PATH` lookup, which this one
/// deliberately omits — a search that ends in a spawn must not resolve anything
/// a user could have planted, the whole reason [`singbox_path`] fails closed.
///
/// Empty off Linux: nothing else ships the app outside its own bundle format.
fn packaged_resource_paths(name: &str) -> Vec<std::path::PathBuf> {
    #[cfg(target_os = "linux")]
    {
        use std::path::PathBuf;

        // /usr for a /usr/bin/tenebra, /usr/local for a local install; absent
        // when the executable cannot be located, leaving the /usr backstop.
        let prefix = std::env::current_exe()
            .ok()
            .and_then(|exe| exe.parent().and_then(|dir| dir.parent()).map(PathBuf::from));

        let mut out: Vec<PathBuf> = Vec::new();
        for base in prefix.into_iter().chain([PathBuf::from("/usr")]) {
            for private in ["lib", "libexec", "share"] {
                let dir = base.join(private).join("tenebra");
                for candidate in [dir.join(name), dir.join("resources").join(name)] {
                    // An install under /usr makes the prefix and the backstop the
                    // same directory; drop the repeats so the error a miss
                    // produces reads as an honest search path. Absolute only —
                    // a relative candidate would be resolved from the current
                    // directory, the very thing this must never do.
                    if candidate.is_absolute() && !out.contains(&candidate) {
                        out.push(candidate);
                    }
                }
            }
        }
        out
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = name;
        Vec::new()
    }
}

// --- command handlers ---------------------------------------------------------
//
// Each mirrors one row of the control-protocol request table. They return
// `Result<T, String>`; Tauri serializes `Ok` as the response `data` and `Err`
// as the response `error`, matching the protocol's `{ ok, data | error }`.
//
// They are `async`: a sync Tauri command runs on the main/event-loop thread, so
// it freezes the whole window for its duration. Every backend call here blocks
// on the sidecar's response channel (up to the 60s request timeout), and the
// network-bound ones (connect, import, refresh, ping, leak_check, run_stun_check,
// run_speed_test) can take seconds. So each clones the `Arc` backend and runs the
// blocking call on a
// worker thread via `spawn_blocking`, leaving the UI responsive. `spawn_blocking`
// only fails if the runtime is shutting down; we surface that as an error string
// like any other.

/// Run a blocking backend call off the main thread and flatten the join error
/// into the command's `Result<_, String>`. `f` gets an owned `Arc` handle.
async fn off_thread<T, F>(backend: Arc<dyn Backend>, f: F) -> Result<T, String>
where
    T: Send + 'static,
    F: FnOnce(Arc<dyn Backend>) -> Result<T, String> + Send + 'static,
{
    tauri::async_runtime::spawn_blocking(move || f(backend))
        .await
        .map_err(|e| format!("backend task failed: {e}"))?
}

#[tauri::command]
async fn status(state: TauriState<'_, AppState>) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), |b| b.status()).await
}

#[tauri::command]
async fn list_profiles(state: TauriState<'_, AppState>) -> Result<ProfileList, String> {
    off_thread(Arc::clone(&state.backend), |b| {
        b.list_profiles().map(|profiles| ProfileList { profiles })
    })
    .await
}

#[tauri::command]
async fn import_subscription(
    state: TauriState<'_, AppState>,
    url: String,
    name: String,
) -> Result<ProfileWrap, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.import_subscription(url, name).map(ProfileWrap::new)
    })
    .await
}

#[tauri::command]
async fn import_link(
    state: TauriState<'_, AppState>,
    link: String,
    name: Option<String>,
) -> Result<ProfileWrap, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.import_link(link, name).map(ProfileWrap::new)
    })
    .await
}

#[tauri::command]
async fn import_links(
    state: TauriState<'_, AppState>,
    links: Vec<String>,
    name: Option<String>,
) -> Result<ImportLinksResult, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.import_links(links, name)
    })
    .await
}

#[tauri::command]
async fn remove_profile(state: TauriState<'_, AppState>, profile: String) -> Result<(), String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.remove_profile(profile)
    })
    .await
}

#[tauri::command]
async fn refresh_subscription(
    state: TauriState<'_, AppState>,
    profile: String,
) -> Result<ProfileWrap, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.refresh_subscription(profile).map(ProfileWrap::new)
    })
    .await
}

#[tauri::command]
async fn connect(
    state: TauriState<'_, AppState>,
    profile: String,
    node: Option<String>,
    auto: Option<bool>,
    allow_tun_conflict: Option<bool>,
) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.connect(
            profile,
            node,
            auto.unwrap_or(false),
            allow_tun_conflict.unwrap_or(false),
        )
    })
    .await
}

#[tauri::command]
async fn disconnect(state: TauriState<'_, AppState>) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), |b| b.disconnect()).await
}

#[tauri::command]
async fn ping(state: TauriState<'_, AppState>, profile: String) -> Result<PingList, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.ping(profile).map(|results| PingList { results })
    })
    .await
}

/// Measure what actually survives each node. Unlike `ping` this opens real
/// connections through every node, so it takes seconds rather than milliseconds —
/// the UI must show it running rather than appear frozen.
#[tauri::command]
async fn check_nodes(
    state: TauriState<'_, AppState>,
    profile: String,
) -> Result<NodeCheck, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.check_nodes(profile)).await
}

/// Check whether video, voice and game latency work right now. Costs about one
/// timeout: the core runs its three probes concurrently.
#[tauri::command]
async fn check_services(state: TauriState<'_, AppState>) -> Result<ServiceChecks, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.check_services()).await
}

#[tauri::command]
async fn set_routing(state: TauriState<'_, AppState>, mode: RoutingMode) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.set_routing(mode)).await
}

#[tauri::command]
async fn set_split(
    state: TauriState<'_, AppState>,
    mode: SplitMode,
    apps: Vec<String>,
) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.set_split(mode, apps)).await
}

#[tauri::command]
async fn set_kill_switch(state: TauriState<'_, AppState>, on: bool) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.set_kill_switch(on)).await
}

#[tauri::command]
async fn set_tls_fragment(state: TauriState<'_, AppState>, on: bool) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.set_tls_fragment(on)).await
}

// rename_all keeps the JS-side argument keys snake_case (entry_id, exit_id),
// matching this file's multi-word command convention (see set_dns) — Tauri v2
// would otherwise expect camelCase.
#[tauri::command(rename_all = "snake_case")]
async fn set_multihop(
    state: TauriState<'_, AppState>,
    profile: String,
    enabled: bool,
    entry_id: String,
    exit_id: String,
) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.set_multihop(profile, enabled, entry_id, exit_id)
    })
    .await
}

#[tauri::command]
async fn set_tun(state: TauriState<'_, AppState>, stack: TunStack) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.set_tun(stack)).await
}

#[tauri::command]
async fn set_proxy_mode(
    state: TauriState<'_, AppState>,
    mode: ConnectionMode,
) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.set_proxy_mode(mode)).await
}

#[tauri::command]
async fn set_autoconnect(state: TauriState<'_, AppState>, on: bool) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.set_autoconnect(on)).await
}

#[tauri::command]
async fn set_auto_failover(state: TauriState<'_, AppState>, on: bool) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.set_auto_failover(on)).await
}

#[tauri::command]
async fn set_crash_reports(state: TauriState<'_, AppState>, on: bool) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.set_crash_reports(on)).await
}

// rename_all keeps the JS-side argument keys snake_case (ad_block, dns_remote,
// dns_direct, ipv4_only), matching this file's command convention. Tauri v2
// otherwise expects camelCase keys by default; the existing single-word commands
// don't expose the difference, but these multi-word ones would, so pin it
// explicitly.
#[tauri::command(rename_all = "snake_case")]
async fn set_dns(
    state: TauriState<'_, AppState>,
    ad_block: bool,
    dns_remote: String,
    dns_direct: String,
    ipv4_only: bool,
) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.set_dns(ad_block, dns_remote, dns_direct, ipv4_only)
    })
    .await
}

// rename_all keeps the JS-side argument keys snake_case (rules_direct,
// rules_proxy, preset_ru_banking, preset_ru_gov), matching this file's multi-word
// command convention (see set_dns) — Tauri v2 would otherwise expect camelCase.
#[tauri::command(rename_all = "snake_case")]
async fn set_rules(
    state: TauriState<'_, AppState>,
    rules_direct: Vec<String>,
    rules_proxy: Vec<String>,
    preset_ru_banking: bool,
    preset_ru_gov: bool,
) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.set_rules(rules_direct, rules_proxy, preset_ru_banking, preset_ru_gov)
    })
    .await
}

// Every argument is optional, and an omitted one leaves that preset alone — the
// core reads an absent field as "unchanged". Tauri fills a missing argument with
// `None` for an `Option`, so the JS side sends only the presets it means to
// change. The names are single words, so no rename_all is needed here (see
// set_dns for why the multi-word commands pin it).
#[tauri::command]
async fn set_presets(
    state: TauriState<'_, AppState>,
    games: Option<bool>,
    voice: Option<bool>,
    services: Option<bool>,
) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.set_presets(games, voice, services)
    })
    .await
}

#[tauri::command]
async fn leak_check(state: TauriState<'_, AppState>) -> Result<LeakCheck, String> {
    off_thread(Arc::clone(&state.backend), |b| b.leak_check()).await
}

#[tauri::command]
async fn run_stun_check(state: TauriState<'_, AppState>) -> Result<StunCheck, String> {
    off_thread(Arc::clone(&state.backend), |b| b.run_stun_check()).await
}

#[tauri::command]
async fn run_speed_test(state: TauriState<'_, AppState>) -> Result<SpeedTest, String> {
    off_thread(Arc::clone(&state.backend), |b| b.run_speed_test()).await
}

/// Ask the core for a support bundle and save it next to the crash log, then
/// hand the UI the path.
///
/// The file is written here rather than by the core because the core may be a
/// LocalSystem service whose data directory the reporting user cannot read. It
/// lands in the same per-user directory as `crash-gui.txt` and `core.log`, so
/// "the place to look when something went wrong" stays one directory rather
/// than three.
///
/// The core decides the filename, but only its last component is used and a
/// separator in it is refused outright: a filename is not a place to accept a
/// path from another process, however trusted.
#[tauri::command]
async fn collect_diagnostics(state: TauriState<'_, AppState>) -> Result<String, String> {
    off_thread(Arc::clone(&state.backend), |b| {
        let bundle = b.collect_diagnostics()?;
        let name = std::path::Path::new(&bundle.filename)
            .file_name()
            .and_then(|n| n.to_str())
            .filter(|n| !n.is_empty() && *n != "." && *n != "..")
            .ok_or_else(|| "the core suggested an unusable filename".to_string())?;
        let dir = crash::data_dir().ok_or_else(|| "no writable data directory".to_string())?;
        let path = dir.join(name);
        std::fs::write(&path, bundle.text.as_bytes())
            .map_err(|e| format!("could not write {}: {e}", path.display()))?;
        Ok(path.to_string_lossy().into_owned())
    })
    .await
}

/// Install a zapret DPI-bypass bundle.
///
/// The UI can hand over either the archive's bytes (a file dropped into the
/// webview has contents but no path) or a filesystem path (Tauri's drag-drop
/// gives real paths, which is the only way a dropped FOLDER can be taken at all).
/// `name` carries the dropped file's name when the UI knows it: the release
/// archives are named after their version, and reading it here is what keeps the
/// first update check from re-downloading the bundle the user just installed.
#[tauri::command]
async fn import_zapret(
    state: TauriState<'_, AppState>,
    data: Option<String>,
    path: Option<String>,
    name: Option<String>,
) -> Result<ZapretBundle, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.import_zapret(data, path, name)
    })
    .await
}

#[tauri::command]
async fn list_zapret(state: TauriState<'_, AppState>) -> Result<ZapretBundle, String> {
    off_thread(Arc::clone(&state.backend), |b| b.list_zapret()).await
}

/// Probe every strategy and report which to keep. Minutes long by nature — each
/// strategy is attached, measured and detached — so it runs off-thread like every
/// other backend call and the UI shows progress from the core's log events.
#[tauri::command]
async fn pick_zapret(state: TauriState<'_, AppState>) -> Result<ZapretPick, String> {
    off_thread(Arc::clone(&state.backend), |b| b.pick_zapret()).await
}

#[tauri::command]
async fn start_zapret(
    state: TauriState<'_, AppState>,
    name: Option<String>,
) -> Result<ZapretActive, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.start_zapret(name)).await
}

#[tauri::command]
async fn stop_zapret(state: TauriState<'_, AppState>) -> Result<(), String> {
    off_thread(Arc::clone(&state.backend), |b| b.stop_zapret()).await
}

/// Check for a newer published bundle and install it, downloading one outright
/// when none is installed. The core also does this on its own schedule; this is
/// the "check now" the user reaches for when video stops loading.
#[tauri::command]
async fn update_zapret(state: TauriState<'_, AppState>) -> Result<ZapretUpdate, String> {
    off_thread(Arc::clone(&state.backend), |b| b.update_zapret()).await
}

#[tauri::command]
async fn set_zapret_auto_update(
    state: TauriState<'_, AppState>,
    on: bool,
) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| {
        b.set_zapret_auto_update(on)
    })
    .await
}

/// Quit the whole app. Closing the window only hides it (see the close handler
/// in `run`); this is the explicit "really exit" path the tray's Quit item and
/// the front end share.
#[tauri::command]
fn quit_app(app: AppHandle) {
    app.exit(0);
}

/// Set the app's active language from the front end. Called once at startup and
/// again on every in-app language change, so the tray menu, its tooltip and
/// desktop notifications track the webview's own localization. The tray is
/// rebuilt right away so the switch shows without a restart; notifications simply
/// read the new value on the next state transition. An unknown code is treated as
/// English (see [`Lang::from_code`]).
#[tauri::command]
fn set_language(app: AppHandle, lang: String) {
    if let Some(state) = app.try_state::<LangState>() {
        *state.lang.lock().unwrap() = Lang::from_code(&lang);
    }
    tray::relabel(&app);
}

// Response envelopes for the commands the protocol wraps in an object.
#[derive(serde::Serialize)]
struct ProfileList {
    profiles: Vec<Profile>,
}

#[derive(serde::Serialize)]
struct ProfileWrap {
    profile: Profile,
}

impl ProfileWrap {
    fn new(profile: Profile) -> Self {
        Self { profile }
    }
}

#[derive(serde::Serialize)]
struct PingList {
    results: Vec<PingResult>,
}

pub fn run() {
    // Capture GUI panics to the local crash file before Tauri starts. Under the
    // release profile's panic=abort the hook runs and then the process aborts, so
    // it is the only chance to persist a panic — install it first of all.
    crash::install_panic_hook();
    tauri::Builder::default()
        // Single-instance must be the FIRST plugin so a second launch is caught
        // before any window or other plugin spins up. It focuses the window we
        // already have and, as a fallback for tauri#12726 (the plugin's own
        // forwarding may not reach the primary instance on Windows), routes any
        // tenebra:// link the second launch carried in its argv. The deep-link
        // feature also forwards that argv to the deep-link plugin, so the same
        // link can arrive twice — deliver_live de-dups it.
        .plugin(tauri_plugin_single_instance::init(|app, argv, _cwd| {
            focus_main_window(app);
            deeplink::deliver_live(app, &deeplink::find_urls(&argv));
        }))
        .plugin(tauri_plugin_deep_link::init())
        .plugin(
            tauri_plugin_autostart::Builder::new()
                // Autostart brings Tenebra up straight into the tray; the window
                // unhides on demand. A manual launch (no flag) opens normally.
                .arg("--minimized")
                .build(),
        )
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .plugin(tauri_plugin_process::init())
        .setup(|app| {
            let sink: Arc<dyn EventSink> = Arc::new(TauriSink {
                app: app.handle().clone(),
                last_state: Mutex::new(None),
            });
            let backend = make_backend(app.handle(), sink);
            app.manage(AppState { backend });
            app.manage(LangState::default());
            app.manage(deeplink::DeepLinkState::default());
            tray::create(app.handle())?;
            setup_deep_link(app.handle());
            // Launch-minimized: autostart passes --minimized so we come up in the
            // tray. The webview still mounts (hidden), so the existing
            // auto-connect effect runs and reconnects silently.
            if std::env::args().any(|a| a == "--minimized") {
                if let Some(window) = app.get_webview_window("main") {
                    let _ = window.hide();
                }
            }
            Ok(())
        })
        .on_window_event(|window, event| {
            // Close-to-tray: intercept the window's close request and hide it
            // instead of letting the app exit. Only the tray's Quit item (or the
            // quit_app command) calls `app.exit`, which is what actually ends the
            // process.
            if let WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .invoke_handler(tauri::generate_handler![
            status,
            list_profiles,
            import_subscription,
            import_link,
            import_links,
            remove_profile,
            refresh_subscription,
            connect,
            disconnect,
            ping,
            check_nodes,
            check_services,
            set_routing,
            set_split,
            set_kill_switch,
            set_tls_fragment,
            set_multihop,
            set_tun,
            set_proxy_mode,
            set_autoconnect,
            set_auto_failover,
            set_dns,
            set_rules,
            set_presets,
            set_crash_reports,
            leak_check,
            run_stun_check,
            run_speed_test,
            collect_diagnostics,
            import_zapret,
            list_zapret,
            pick_zapret,
            start_zapret,
            stop_zapret,
            update_zapret,
            set_zapret_auto_update,
            quit_app,
            set_language,
            take_launch_deep_links,
            update_channel::check_update_for_channel,
            update_channel::in_app_updates_supported,
            crash::check_crash_report,
            crash::record_web_crash,
            crash::open_report_url,
        ])
        .run(tauri::generate_context!())
        .expect("error while running Tenebra");
}

/// Wire up `tenebra://` deep links: register the scheme in development, capture
/// any link the app launched with (cold start), and forward links that arrive
/// while it runs. Pulled out of `run` so the setup closure stays readable.
fn setup_deep_link(app: &AppHandle) {
    // Register the tenebra:// scheme at runtime where nothing else will have.
    // In development that is every platform: a `tauri dev` build was never
    // installed, so no installer claimed the scheme for it.
    //
    // On Linux it is also the release path. The scheme is claimed by a .desktop
    // file, and only a package that installs one — the .deb, or a native package
    // shipping the same entry — hands the desktop environment a handler; an
    // AppImage is a single file that no one registered, so without this the
    // link has nowhere to go. Registration writes a per-user handler entry
    // pointing at this executable (the AppImage path when running from one) and
    // is idempotent, so re-running it on every launch keeps it correct after the
    // file moves. A machine without `xdg-mime` simply gets an error we ignore.
    #[cfg(any(debug_assertions, target_os = "linux"))]
    {
        let _ = app.deep_link().register_all();
    }

    // Cold start: the OS launches us with the link as a CLI argument. Collect it
    // from the plugin (get_current) and, defensively, the raw argv, then queue it
    // for the front end to drain once the webview is listening — an event emitted
    // now, before setup finishes, would be lost.
    let mut launch: Vec<String> = Vec::new();
    if let Ok(Some(urls)) = app.deep_link().get_current() {
        launch.extend(urls.iter().map(|u| u.to_string()));
    }
    launch.extend(deeplink::find_urls(&std::env::args().collect::<Vec<_>>()));
    deeplink::capture_launch(app, &launch);

    // Warm: a link opened while we run drives on_open_url in this (primary)
    // instance. deliver_live de-dups against the single-instance argv fallback.
    let handle = app.clone();
    app.deep_link().on_open_url(move |event| {
        let urls: Vec<String> = event.urls().iter().map(|u| u.to_string()).collect();
        deeplink::deliver_live(&handle, &urls);
    });
}

/// Hand the front end the deep links the app launched with (cold start), clearing
/// them so a later webview remount doesn't replay them.
#[tauri::command]
fn take_launch_deep_links(
    state: TauriState<'_, deeplink::DeepLinkState>,
) -> Vec<deeplink::DeepLinkAction> {
    state.take_launch()
}

/// Bring the main window to the front: unhide, un-minimize, and focus it. Shared
/// by the tray "Show" action and the single-instance callback.
fn focus_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}

/// Disconnect through the managed backend. Used by the tray, which can tear the
/// tunnel down without a profile (unlike connect, which the front end drives).
/// The backend's own state event drives the UI and tray tooltip; on failure we
/// surface the reason on the log channel the webview already listens to.
fn disconnect_backend(app: &AppHandle) {
    if let Some(state) = app.try_state::<AppState>() {
        if let Err(e) = state.backend.disconnect() {
            let _ = app.emit(
                EVENT_LOG,
                json!({ "level": "error", "msg": format!("disconnect failed: {e}") }),
            );
        }
    }
}

/// Connect through the managed backend, straight from the tray — its quick
/// connect and per-node items dial a profile without surfacing the window (unlike
/// the front end's connect flow, which owns the selected profile and its
/// prompts). `node` picks an exact exit; `None` lets the core walk its default
/// order. Run on a worker thread: a connect blocks on the core's acknowledgement,
/// and the menu handler runs on the event-loop thread we must not freeze. The
/// backend's own state event drives the UI and tray; a failure is surfaced on the
/// log channel the webview already listens to.
fn connect_backend(app: &AppHandle, profile: String, node: Option<String>) {
    let Some(state) = app.try_state::<AppState>() else {
        return;
    };
    let backend = Arc::clone(&state.backend);
    let app = app.clone();
    tauri::async_runtime::spawn_blocking(move || {
        // The tray connect never overrides the tun-conflict guard: there is no
        // window in front of the user to explain what it would be overriding.
        if let Err(e) = backend.connect(profile, node, false, false) {
            let _ = app.emit(
                EVENT_LOG,
                json!({ "level": "error", "msg": format!("connect failed: {e}") }),
            );
        }
    });
}

/// Fetch the profile list through the managed backend for the tray's node
/// submenu. Blocking, so the tray runs it on a worker thread (never the event
/// reader thread, which would deadlock delivering its own response). `Err` when
/// the backend isn't managed yet or the call fails.
fn list_profiles_blocking(app: &AppHandle) -> Result<Vec<Profile>, String> {
    let state = app
        .try_state::<AppState>()
        .ok_or("backend is not available")?;
    state.backend.list_profiles()
}

/// The connection state the tray tooltip should reflect, localized to `lang`.
/// Pulled out so both the sink and the initial tray build agree on the wording.
fn tooltip_for(lang: Lang, state: ConnectionState) -> &'static str {
    match lang {
        Lang::En => match state {
            ConnectionState::Idle => "Tenebra — Disconnected",
            ConnectionState::Connecting => "Tenebra — Connecting…",
            // A one-shot auto-failover switch reads as reconnecting, like connecting.
            ConnectionState::HealthReconnecting => "Tenebra — Reconnecting…",
            ConnectionState::Connected => "Tenebra — Connected",
            ConnectionState::Error => "Tenebra — Error",
        },
        Lang::Ru => match state {
            ConnectionState::Idle => "Tenebra — Отключено",
            ConnectionState::Connecting => "Tenebra — Подключение…",
            ConnectionState::HealthReconnecting => "Tenebra — Переподключение…",
            ConnectionState::Connected => "Tenebra — Подключено",
            ConnectionState::Error => "Tenebra — Ошибка",
        },
    }
}

/// The desktop notification a state transition warrants, as `(title, body)`, or
/// `None` when it isn't noteworthy: the first snapshot (`prev` is `None`, so the
/// app just learned the current state rather than seeing it change), no change
/// (`prev == new` — the debounce), or a transient `Connecting`. Pure so the
/// mapping is unit-tested without a Tauri app or a real toast.
///
/// The wording is localized to `lang`: a system notification lives outside the
/// webview, so — like the tray labels — the shell must translate it itself. A
/// backend-supplied error message (`state.error`) is passed through verbatim, as
/// it is data rather than a UI string.
fn transition_notice(
    lang: Lang,
    prev: Option<ConnectionState>,
    state: &State,
) -> Option<(&'static str, String)> {
    let prev = prev?;
    let now = state.state;
    if prev == now {
        return None;
    }
    match now {
        ConnectionState::Connected => Some(match lang {
            Lang::En => ("Connected", "The secure tunnel is up.".to_string()),
            Lang::Ru => ("Подключено", "Защищённый туннель активен.".to_string()),
        }),
        // A drop while the kill switch is armed is the kill switch doing its job:
        // traffic is now blocked. Call it out distinctly from a plain failure.
        ConnectionState::Error if state.kill_switch.unwrap_or(false) => Some(match lang {
            Lang::En => (
                "Kill switch engaged",
                "The tunnel dropped and traffic is blocked.".to_string(),
            ),
            Lang::Ru => (
                "Сработал kill-switch",
                "Туннель разорван, трафик заблокирован.".to_string(),
            ),
        }),
        ConnectionState::Error => {
            let (title, fallback) = match lang {
                Lang::En => ("Connection failed", "The tunnel could not be established."),
                Lang::Ru => ("Не удалось подключиться", "Не удалось установить туннель."),
            };
            Some((
                title,
                state.error.clone().unwrap_or_else(|| fallback.to_string()),
            ))
        }
        // Only a drop from a live tunnel is a "disconnect"; a return to idle from
        // connecting is an aborted/failed dial, not worth a toast.
        ConnectionState::Idle if prev == ConnectionState::Connected => Some(match lang {
            Lang::En => ("Disconnected", "The tunnel is down.".to_string()),
            Lang::Ru => ("Отключено", "Туннель выключен.".to_string()),
        }),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Build a `State` with a given connection state, leaving the rest at their
    /// idle defaults; individual fields are set by the caller where they matter.
    fn state_with(state: ConnectionState) -> State {
        State {
            state,
            node: None,
            profile: None,
            routing: None,
            daemon_version: None,
            split: None,
            split_apps: None,
            kill_switch: None,
            tls_fragment: None,
            multihop: None,
            tun_stack: None,
            proxy_mode: None,
            proxy_port: None,
            autoconnect: None,
            auto_failover: None,
            ad_block: None,
            dns_remote: None,
            dns_direct: None,
            ipv4_only: None,
            rules_direct: None,
            rules_proxy: None,
            preset_ru_banking: None,
            preset_ru_gov: None,
            preset_games_direct: None,
            preset_voice_direct: None,
            preset_unblock_services: None,
            crash_reports: None,
            crash_reports_asked: false,
            zapret_active: None,
            zapret_strategy: None,
            zapret_version: None,
            zapret_auto_update: None,
            error: None,
        }
    }

    #[test]
    fn first_snapshot_is_silent() {
        // No previous state: the app is just learning where it stands, not seeing
        // a change, so nothing fires — even for connected.
        assert_eq!(
            transition_notice(Lang::En, None, &state_with(ConnectionState::Connected)),
            None
        );
        assert_eq!(
            transition_notice(Lang::En, None, &state_with(ConnectionState::Idle)),
            None
        );
    }

    #[test]
    fn unchanged_state_is_debounced() {
        // The core re-emits state on a live kill-switch/tun re-apply without the
        // connection state changing; those must stay quiet.
        for s in [
            ConnectionState::Idle,
            ConnectionState::Connecting,
            ConnectionState::Connected,
            ConnectionState::Error,
        ] {
            assert_eq!(
                transition_notice(Lang::En, Some(s), &state_with(s)),
                None,
                "prev == new should not notify for {s:?}"
            );
        }
    }

    #[test]
    fn connecting_to_connected_notifies() {
        let notice = transition_notice(
            Lang::En,
            Some(ConnectionState::Connecting),
            &state_with(ConnectionState::Connected),
        );
        assert_eq!(notice.map(|(t, _)| t), Some("Connected"));
    }

    #[test]
    fn connected_to_idle_is_a_disconnect() {
        let notice = transition_notice(
            Lang::En,
            Some(ConnectionState::Connected),
            &state_with(ConnectionState::Idle),
        );
        assert_eq!(notice.map(|(t, _)| t), Some("Disconnected"));
    }

    #[test]
    fn connecting_to_idle_is_silent() {
        // An aborted or failed dial returns to idle without ever being connected;
        // that isn't a "disconnect".
        assert_eq!(
            transition_notice(
                Lang::En,
                Some(ConnectionState::Connecting),
                &state_with(ConnectionState::Idle)
            ),
            None
        );
    }

    #[test]
    fn error_without_kill_switch_reports_the_reason() {
        let mut s = state_with(ConnectionState::Error);
        s.error = Some("handshake timed out".to_string());
        let notice = transition_notice(Lang::En, Some(ConnectionState::Connecting), &s);
        assert_eq!(
            notice,
            Some(("Connection failed", "handshake timed out".to_string()))
        );
    }

    #[test]
    fn error_with_kill_switch_armed_calls_out_the_kill_switch() {
        let mut s = state_with(ConnectionState::Error);
        s.kill_switch = Some(true);
        let notice = transition_notice(Lang::En, Some(ConnectionState::Connected), &s);
        assert_eq!(notice.map(|(t, _)| t), Some("Kill switch engaged"));
    }

    #[test]
    fn the_mock_is_requested_by_value_not_by_presence() {
        // The demo backend is compiled into every release build, so the switch
        // that arms it has to mean what it says. Anyone who exports
        // TENEBRA_MOCK=0 (or leaves it empty) is asking for the real core, and
        // getting a fake one instead would look exactly like the bug this
        // module's fallback path already produces: an app that answers, plausibly
        // and wrongly, with somebody else's data.
        assert!(!mock_requested(None));
        assert!(!mock_requested(Some("")));
        assert!(!mock_requested(Some("0")));
        assert!(!mock_requested(Some("off")));
        assert!(!mock_requested(Some("false")));
        assert!(!mock_requested(Some("no")));
        // Case and stray whitespace come from hand-typed shell exports, not from
        // data, so they must not decide the outcome.
        assert!(!mock_requested(Some("  OFF  ")));
        assert!(!mock_requested(Some("False")));
        // And the ways a developer actually turns it on still work.
        assert!(mock_requested(Some("1")));
        assert!(mock_requested(Some("on")));
        assert!(mock_requested(Some("true")));
        assert!(mock_requested(Some("yes")));
    }

    #[cfg(any(windows, target_os = "linux"))]
    #[test]
    fn the_service_watch_stops_at_the_first_sighting() {
        // A service that shows up on the third look is reported, and the watch
        // ends there rather than keeping a thread polling for the rest of the
        // run.
        let looks = std::cell::Cell::new(0);
        let seen = await_probe(
            || {
                looks.set(looks.get() + 1);
                looks.get() >= 3
            },
            Duration::from_millis(1),
            Duration::from_secs(5),
        );
        assert!(seen, "a service that appeared must be reported");
        assert_eq!(looks.get(), 3, "the watch must stop once it has an answer");
    }

    #[cfg(any(windows, target_os = "linux"))]
    #[test]
    fn the_service_watch_gives_up_when_its_window_closes() {
        // Nothing ever appears — the ordinary case on a machine with no service
        // at all. The watch must look at least once, then end by itself.
        let looks = std::cell::Cell::new(0);
        let seen = await_probe(
            || {
                looks.set(looks.get() + 1);
                false
            },
            Duration::from_millis(1),
            Duration::from_millis(20),
        );
        assert!(!seen);
        assert!(
            looks.get() >= 1,
            "the window must be given at least one look"
        );
    }

    #[test]
    fn packaged_resource_paths_stay_absolute_and_off_windows_and_macos() {
        // The fallback exists for a distribution package that lays the payload
        // out per the FHS instead of inside a Tauri bundle. Every candidate has
        // to be an absolute system path: a relative one would be resolved from
        // the current directory, which is exactly the planting vector
        // `singbox_path` fails closed to avoid.
        let paths = packaged_resource_paths("sing-box");
        assert!(
            paths.iter().all(|p| p.is_absolute()),
            "every candidate must be absolute: {paths:?}"
        );

        #[cfg(target_os = "linux")]
        {
            let shown: Vec<String> = paths.iter().map(|p| p.display().to_string()).collect();
            // The layout the Arch package actually ships — sing-box is not in
            // the official repositories, so the package carries its own copy
            // beside the rule-sets — plus the other FHS homes the core walks for
            // the same payload, and the bundler's resources/ sub-directory.
            for expected in [
                "/usr/lib/tenebra/sing-box",
                "/usr/lib/tenebra/resources/sing-box",
                "/usr/libexec/tenebra/sing-box",
                "/usr/share/tenebra/sing-box",
            ] {
                assert!(
                    shown.iter().any(|p| p == expected),
                    "missing {expected} in {shown:?}"
                );
            }
            // Each directory is offered once, even though the running
            // executable's own prefix is very often /usr itself.
            let mut unique = shown.clone();
            unique.sort();
            unique.dedup();
            assert_eq!(unique.len(), shown.len(), "duplicate candidates: {shown:?}");
        }
        // Nothing else ships the app outside its own bundle format, so nothing
        // else widens the search.
        #[cfg(not(target_os = "linux"))]
        assert!(paths.is_empty(), "unexpected candidates: {paths:?}");
    }

    #[test]
    fn language_code_maps_to_lang() {
        assert_eq!(Lang::from_code("ru"), Lang::Ru);
        assert_eq!(Lang::from_code("en"), Lang::En);
        // An unknown or empty code is treated as English rather than left blank.
        assert_eq!(Lang::from_code("fr"), Lang::En);
        assert_eq!(Lang::from_code(""), Lang::En);
    }

    #[test]
    fn tooltip_is_localized() {
        assert_eq!(
            tooltip_for(Lang::En, ConnectionState::Connected),
            "Tenebra — Connected"
        );
        assert_eq!(
            tooltip_for(Lang::Ru, ConnectionState::Connected),
            "Tenebra — Подключено"
        );
    }

    #[test]
    fn notices_are_localized_to_russian() {
        // The same transitions the English tests cover, asserted in Russian so a
        // mojibake regression in the tables is caught here.
        let connected = transition_notice(
            Lang::Ru,
            Some(ConnectionState::Connecting),
            &state_with(ConnectionState::Connected),
        );
        assert_eq!(
            connected,
            Some(("Подключено", "Защищённый туннель активен.".to_string()))
        );

        let disconnected = transition_notice(
            Lang::Ru,
            Some(ConnectionState::Connected),
            &state_with(ConnectionState::Idle),
        );
        assert_eq!(
            disconnected,
            Some(("Отключено", "Туннель выключен.".to_string()))
        );

        let mut armed = state_with(ConnectionState::Error);
        armed.kill_switch = Some(true);
        let killed = transition_notice(Lang::Ru, Some(ConnectionState::Connected), &armed);
        assert_eq!(killed.map(|(t, _)| t), Some("Сработал kill-switch"));
    }
}
