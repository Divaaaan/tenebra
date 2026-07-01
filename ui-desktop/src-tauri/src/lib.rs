//! Tauri shell. It owns the backend, exposes the control protocol as Tauri
//! commands, and bridges backend events onto the webview event bus.
//!
//! The backend is hidden behind the [`Backend`](backend::Backend) trait. Today
//! it is a [`MockBackend`](backend::mock::MockBackend); see `make_backend` for
//! the single place to swap in the real sidecar client.

mod backend;
mod tray;

use std::sync::Arc;

use serde_json::json;
use tauri::{AppHandle, Emitter, Manager, State as TauriState, WindowEvent};

use backend::{
    Backend, ConnectionState, EventSink, ImportLinksResult, LeakCheck, PingResult, Profile,
    RoutingMode, SplitMode, State, TunStack, EVENT_LOG, EVENT_PROFILES, EVENT_STATE, EVENT_TRAFFIC,
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
}

impl EventSink for TauriSink {
    fn state(&self, state: &State) {
        // The tray tooltip mirrors the live connection state; this is the one
        // place backend state flows through, so we refresh it here.
        tray::sync_state(&self.app, state);
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
        // Payload-less: the renderer reloads the profile list on receipt.
        let _ = self.app.emit(EVENT_PROFILES, ());
    }
}

// =============================================================================
// Backend selection.
//
// The ONE switch between the real core sidecar and the demo fake. By default we
// spawn the `tenebra-core` sidecar and drive the real tunnel; set TENEBRA_MOCK=1
// to fall back to the in-process mock (useful for UI work without the core, or
// when the sidecar binary isn't built). Both implement the same `Backend` trait,
// so nothing else in this file or the front end changes.
//
// If the sidecar fails to spawn (e.g. the binary is missing), we log and fall
// back to the mock rather than leaving the UI with no backend at all.
// =============================================================================
fn make_backend(app: &AppHandle, sink: Arc<dyn EventSink>) -> Arc<dyn Backend> {
    if std::env::var_os("TENEBRA_MOCK").is_some() {
        return Arc::new(backend::mock::MockBackend::new(sink));
    }

    let program = backend::sidecar::SidecarBackend::default_program();
    let singbox = singbox_path(app);
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
/// shipped beside the app as a bundle resource, with wintun.dll in the same
/// directory for the tun device to load.
fn singbox_path(app: &AppHandle) -> std::path::PathBuf {
    if let Some(p) = std::env::var_os("TENEBRA_SINGBOX") {
        return std::path::PathBuf::from(p);
    }
    app.path()
        .resolve(
            "resources/sing-box.exe",
            tauri::path::BaseDirectory::Resource,
        )
        .unwrap_or_else(|_| std::path::PathBuf::from("sing-box.exe"))
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
// network-bound ones (connect, import, refresh, ping, leak_check) can take
// seconds. So each clones the `Arc` backend and runs the blocking call on a
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
    // None, which we treat as "not auto" — the protocol's default order.
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
async fn set_tun(state: TauriState<'_, AppState>, stack: TunStack) -> Result<State, String> {
    off_thread(Arc::clone(&state.backend), move |b| b.set_tun(stack)).await
}

#[tauri::command]
async fn leak_check(state: TauriState<'_, AppState>) -> Result<LeakCheck, String> {
    off_thread(Arc::clone(&state.backend), |b| b.leak_check()).await
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
    tauri::Builder::default()
        // Single-instance must be the FIRST plugin so a second launch is caught
        // before any window or other plugin spins up; it just focuses the window
        // we already have.
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
            focus_main_window(app);
        }))
        .plugin(tauri_plugin_autostart::Builder::new().build())
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .plugin(tauri_plugin_process::init())
        .setup(|app| {
            let sink: Arc<dyn EventSink> = Arc::new(TauriSink {
                app: app.handle().clone(),
            });
            let backend = make_backend(app.handle(), sink);
            app.manage(AppState { backend });
            tray::create(app.handle())?;
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
            set_tun,
            leak_check,
            quit_app,
        ])
        .run(tauri::generate_context!())
        .expect("error while running Tenebra");
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

/// The connection state the tray tooltip should reflect, as plain English.
/// Pulled out so both the sink and the initial tray build agree on the wording.
fn tooltip_for(state: ConnectionState) -> &'static str {
    match state {
        ConnectionState::Idle => "Tenebra — Disconnected",
        ConnectionState::Connecting => "Tenebra — Connecting…",
        ConnectionState::Connected => "Tenebra — Connected",
        ConnectionState::Error => "Tenebra — Error",
    }
}
