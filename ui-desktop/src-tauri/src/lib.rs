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

use serde_json::json;
use tauri::{AppHandle, Emitter, Manager, State as TauriState, WindowEvent};
use tauri_plugin_deep_link::DeepLinkExt;
use tauri_plugin_notification::NotificationExt;

use backend::{
    AttemptsSnapshot, Backend, ConnectionState, EventSink, ImportLinksResult, LeakCheck,
    PingResult, Profile, RoutingMode, SpeedTest, SplitMode, State, StunCheck, TunStack,
    EVENT_ATTEMPTS, EVENT_LOG, EVENT_PROFILES, EVENT_STATE, EVENT_TRAFFIC,
};

/// Held in Tauri's managed state and shared by every command handler. The
/// backend is an `Arc` rather than a `Box` so an async command can clone a
/// handle and run its blocking backend call on a worker thread (the trait is
/// `Send + Sync`), keeping the main/event-loop thread free.
struct AppState {
    backend: Arc<dyn Backend>,
}

/// Bridges backend events to the webview. The backend calls these; we forward
/// each onto the matching event channel with the protocol's exact payload shape.
struct TauriSink {
    app: AppHandle,
    /// The last connection state delivered here, so a desktop notification fires
    /// only on a real transition вЂ” not on every snapshot that leaves the state
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
        if let Some((title, body)) = transition_notice(prev, state) {
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
//     core, or when the sidecar binary isn't built).
//  2. On Windows, if a core is already listening on the control pipe (the
//     installed service, or `tenebra-core --pipe` in a console), attach to it.
//     The tunnel then outlives this process and the GUI needs no elevation.
//     TENEBRA_PIPE renames the pipe or (`off`) skips it вЂ” see
//     backend::pipe::configured_name.
//  2'. On macOS, the same probe over the daemon's unix socket
//     (`/var/run/tenebra.sock`): if the root LaunchDaemon is listening, attach.
//     TENEBRA_SOCKET renames the path or (`off`) skips it вЂ” see
//     backend::unix::configured_path.
//  3. Otherwise spawn the `tenebra-core` sidecar and own it вЂ” today's default
//     and the development path.
//
// If the sidecar fails to spawn (e.g. the binary is missing), we log and fall
// back to the mock rather than leaving the UI with no backend at all. Every
// choice implements the same `Backend` trait and is logged on the UI's own log
// channel, so nothing else in this file or the front end changes.
// =============================================================================
fn make_backend(app: &AppHandle, sink: Arc<dyn EventSink>) -> Arc<dyn Backend> {
    if std::env::var_os("TENEBRA_MOCK").is_some() {
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
            // No service (or an unreachable pipe) is the normal development
            // case, not an error: fall through to the sidecar.
            Err(e) => sink.log(
                "info",
                &format!("no Tenebra service on {name} ({e}); spawning the core as a sidecar"),
            ),
        }
    }

    #[cfg(target_os = "macos")]
    if let Some(path) = backend::unix::configured_path() {
        match backend::unix::UnixBackend::connect(&path, Arc::clone(&sink)) {
            Ok(backend) => {
                sink.log("info", &format!("attached to the Tenebra daemon on {path}"));
                return Arc::new(backend);
            }
            // No daemon (or an unreachable socket) is the normal development
            // case, not an error: fall through to the sidecar.
            Err(e) => sink.log(
                "info",
                &format!("no Tenebra daemon on {path} ({e}); spawning the core as a sidecar"),
            ),
        }
    }

    // Both the core and sing-box must resolve to an absolute, bundled path. If
    // either can't be located we fail closed to the demo backend rather than let
    // a bare name resolve from the current directory or PATH вЂ” a spawn against a
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

/// Path passed to the core as TENEBRA_SINGBOX so it can locate sing-box. An
/// explicit env var wins (handy in development); otherwise we use the copy
/// shipped beside the app as a bundle resource. The bundled binary is named per
/// platform: `sing-box.exe` on Windows (with wintun.dll in the same directory
/// for the tun device to load), plain `sing-box` elsewhere.
///
/// Fails closed: if the bundled resource can't be resolved to an absolute path
/// that exists, we return an error instead of falling back to a bare name.
/// A bare `sing-box` would be resolved by the core relative to its CWD (and
/// then PATH), so a `sing-box` planted in an attacker-chosen working directory
/// could be launched with the tunnel's privileges. `resolve` against the
/// Resource base directory is always absolute and rooted at the app bundle, so
/// it can't be redirected by the CWD; requiring it removes the planting vector.
/// The `TENEBRA_SINGBOX` override is operator-supplied, not webview-reachable,
/// so it stays trusted.
fn singbox_path(app: &AppHandle) -> Result<std::path::PathBuf, String> {
    if let Some(p) = std::env::var_os("TENEBRA_SINGBOX") {
        return Ok(std::path::PathBuf::from(p));
    }
    #[cfg(windows)]
    let resource = "resources/sing-box.exe";
    #[cfg(not(windows))]
    let resource = "resources/sing-box";

    let resolved = app
        .path()
        .resolve(resource, tauri::path::BaseDirectory::Resource)
        .map_err(|e| {
            format!(
                "cannot resolve the bundled sing-box resource ({resource}): {e}; \
                 refusing to fall back to a bare name resolved from CWD/PATH"
            )
        })?;
    if !resolved.exists() {
        return Err(format!(
            "bundled sing-box resource resolved to {} but no file is there; \
             refusing to fall back to a bare name resolved from CWD/PATH",
            resolved.display()
        ));
    }
    Ok(resolved)
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
    // Optional so an older/leaner caller can omit it; Tauri maps a missing arg to
    // None, which we treat as "not auto" вЂ” the protocol's default order.
    auto: Option<bool>,
) -> Result<State, String> {
    let auto = auto.unwrap_or(false);
    off_thread(Arc::clone(&state.backend), move |b| {
        b.connect(profile, node, auto)
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

#[tauri::command]
async fn set_tun(state: TauriState<'_, AppState>, stack: TunStack) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.set_tun(stack)).await
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
// command convention (see set_dns) вЂ” Tauri v2 would otherwise expect camelCase.
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

/// Quit the whole app. Closing the window only hides it (see the close handler
/// in `run`); this is the explicit "really exit" path the tray's Quit item and
/// the front end share.
#[tauri::command]
fn quit_app(app: AppHandle) {
    app.exit(0);
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
    // it is the only chance to persist a panic вЂ” install it first of all.
    crash::install_panic_hook();
    tauri::Builder::default()
        // Single-instance must be the FIRST plugin so a second launch is caught
        // before any window or other plugin spins up. It focuses the window we
        // already have and, as a fallback for tauri#12726 (the plugin's own
        // forwarding may not reach the primary instance on Windows), routes any
        // tenebra:// link the second launch carried in its argv. The deep-link
        // feature also forwards that argv to the deep-link plugin, so the same
        // link can arrive twice вЂ” deliver_live de-dups it.
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
            set_routing,
            set_split,
            set_kill_switch,
            set_tls_fragment,
            set_tun,
            set_autoconnect,
            set_auto_failover,
            set_dns,
            set_rules,
            set_crash_reports,
            leak_check,
            run_stun_check,
            run_speed_test,
            quit_app,
            take_launch_deep_links,
            update_channel::check_update_for_channel,
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
    // Development only: register the tenebra:// scheme at runtime so links work
    // from a `tauri dev` build. In a release build the installer owns
    // registration, pointing the scheme at the installed executable.
    #[cfg(debug_assertions)]
    {
        let _ = app.deep_link().register_all();
    }

    // Cold start: the OS launches us with the link as a CLI argument. Collect it
    // from the plugin (get_current) and, defensively, the raw argv, then queue it
    // for the front end to drain once the webview is listening вЂ” an event emitted
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
        if let Err(e) = backend.connect(profile, node, false) {
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

/// The connection state the tray tooltip should reflect, as plain English.
/// Pulled out so both the sink and the initial tray build agree on the wording.
fn tooltip_for(state: ConnectionState) -> &'static str {
    match state {
        ConnectionState::Idle => "Tenebra вЂ” Disconnected",
        ConnectionState::Connecting => "Tenebra вЂ” ConnectingвЂ¦",
        // A one-shot auto-failover switch reads as reconnecting, like connecting.
        ConnectionState::HealthReconnecting => "Tenebra вЂ” ReconnectingвЂ¦",
        ConnectionState::Connected => "Tenebra вЂ” Connected",
        ConnectionState::Error => "Tenebra вЂ” Error",
    }
}

/// The desktop notification a state transition warrants, as `(title, body)`, or
/// `None` when it isn't noteworthy: the first snapshot (`prev` is `None`, so the
/// app just learned the current state rather than seeing it change), no change
/// (`prev == new` вЂ” the debounce), or a transient `Connecting`. Pure so the
/// mapping is unit-tested without a Tauri app or a real toast.
///
/// The wording is plain English (a system notification lives outside the
/// localized webview, like the tray labels).
fn transition_notice(
    prev: Option<ConnectionState>,
    state: &State,
) -> Option<(&'static str, String)> {
    let prev = prev?;
    let now = state.state;
    if prev == now {
        return None;
    }
    match now {
        ConnectionState::Connected => Some(("Connected", "The secure tunnel is up.".to_string())),
        // A drop while the kill switch is armed is the kill switch doing its job:
        // traffic is now blocked. Call it out distinctly from a plain failure.
        ConnectionState::Error if state.kill_switch.unwrap_or(false) => Some((
            "Kill switch engaged",
            "The tunnel dropped and traffic is blocked.".to_string(),
        )),
        ConnectionState::Error => Some((
            "Connection failed",
            state
                .error
                .clone()
                .unwrap_or_else(|| "The tunnel could not be established.".to_string()),
        )),
        // Only a drop from a live tunnel is a "disconnect"; a return to idle from
        // connecting is an aborted/failed dial, not worth a toast.
        ConnectionState::Idle if prev == ConnectionState::Connected => {
            Some(("Disconnected", "The tunnel is down.".to_string()))
        }
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
            split: None,
            split_apps: None,
            kill_switch: None,
            tls_fragment: None,
            tun_stack: None,
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
            crash_reports: None,
            crash_reports_asked: false,
            error: None,
        }
    }

    #[test]
    fn first_snapshot_is_silent() {
        // No previous state: the app is just learning where it stands, not seeing
        // a change, so nothing fires вЂ” even for connected.
        assert_eq!(
            transition_notice(None, &state_with(ConnectionState::Connected)),
            None
        );
        assert_eq!(
            transition_notice(None, &state_with(ConnectionState::Idle)),
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
                transition_notice(Some(s), &state_with(s)),
                None,
                "prev == new should not notify for {s:?}"
            );
        }
    }

    #[test]
    fn connecting_to_connected_notifies() {
        let notice = transition_notice(
            Some(ConnectionState::Connecting),
            &state_with(ConnectionState::Connected),
        );
        assert_eq!(notice.map(|(t, _)| t), Some("Connected"));
    }

    #[test]
    fn connected_to_idle_is_a_disconnect() {
        let notice = transition_notice(
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
        let notice = transition_notice(Some(ConnectionState::Connecting), &s);
        assert_eq!(
            notice,
            Some(("Connection failed", "handshake timed out".to_string()))
        );
    }

    #[test]
    fn error_with_kill_switch_armed_calls_out_the_kill_switch() {
        let mut s = state_with(ConnectionState::Error);
        s.kill_switch = Some(true);
        let notice = transition_notice(Some(ConnectionState::Connected), &s);
        assert_eq!(notice.map(|(t, _)| t), Some("Kill switch engaged"));
    }
}
