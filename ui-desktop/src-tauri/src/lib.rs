//! Tauri shell. It owns the backend, exposes the control protocol as Tauri
//! commands, and bridges backend events onto the webview event bus.
//!
//! The backend is hidden behind the [`Backend`](backend::Backend) trait. Today
//! it is a [`MockBackend`](backend::mock::MockBackend); see `make_backend` for
//! the single place to swap in the real sidecar client.

mod backend;

use std::sync::Arc;

use serde_json::json;
use tauri::{AppHandle, Emitter, Manager, State as TauriState};

use backend::{
    Backend, EventSink, LeakCheck, PingResult, Profile, RoutingMode, State, EVENT_LOG, EVENT_STATE,
    EVENT_TRAFFIC,
};

/// Held in Tauri's managed state and shared by every command handler.
struct AppState {
    backend: Box<dyn Backend>,
}

/// Bridges backend events to the webview. The backend calls these; we forward
/// each onto the matching event channel with the protocol's exact payload shape.
struct TauriSink {
    app: AppHandle,
}

impl EventSink for TauriSink {
    fn state(&self, state: &State) {
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
fn make_backend(sink: Arc<dyn EventSink>) -> Box<dyn Backend> {
    if std::env::var_os("TENEBRA_MOCK").is_some() {
        return Box::new(backend::mock::MockBackend::new(sink));
    }

    let program = backend::sidecar::SidecarBackend::default_program();
    let singbox = singbox_path();
    match backend::sidecar::SidecarBackend::spawn(program, singbox, Arc::clone(&sink)) {
        Ok(backend) => Box::new(backend),
        Err(e) => {
            sink.log(
                "error",
                &format!("could not start tenebra-core, using demo backend: {e}"),
            );
            Box::new(backend::mock::MockBackend::new(sink))
        }
    }
}

/// Path passed to the core as TENEBRA_SINGBOX so it can locate sing-box. An
/// explicit env var wins; otherwise the repo's local `bin/sing-box.exe` is used.
/// Bundling sing-box + wintun as Tauri resources is a follow-up.
fn singbox_path() -> std::path::PathBuf {
    if let Some(p) = std::env::var_os("TENEBRA_SINGBOX") {
        return std::path::PathBuf::from(p);
    }
    std::path::PathBuf::from(r"C:\Users\danil\projects\tenebra\bin\sing-box.exe")
}

// --- command handlers ---------------------------------------------------------
//
// Each mirrors one row of the control-protocol request table. They return
// `Result<T, String>`; Tauri serializes `Ok` as the response `data` and `Err`
// as the response `error`, matching the protocol's `{ ok, data | error }`.

#[tauri::command]
fn status(state: TauriState<'_, AppState>) -> Result<State, String> {
    state.backend.status()
}

#[tauri::command]
fn list_profiles(state: TauriState<'_, AppState>) -> Result<ProfileList, String> {
    state
        .backend
        .list_profiles()
        .map(|profiles| ProfileList { profiles })
}

#[tauri::command]
fn import_subscription(
    state: TauriState<'_, AppState>,
    url: String,
    name: String,
) -> Result<ProfileWrap, String> {
    state
        .backend
        .import_subscription(url, name)
        .map(ProfileWrap::new)
}

#[tauri::command]
fn import_link(
    state: TauriState<'_, AppState>,
    link: String,
    name: Option<String>,
) -> Result<ProfileWrap, String> {
    state.backend.import_link(link, name).map(ProfileWrap::new)
}

#[tauri::command]
fn remove_profile(state: TauriState<'_, AppState>, profile: String) -> Result<(), String> {
    state.backend.remove_profile(profile)
}

#[tauri::command]
fn refresh_subscription(
    state: TauriState<'_, AppState>,
    profile: String,
) -> Result<ProfileWrap, String> {
    state
        .backend
        .refresh_subscription(profile)
        .map(ProfileWrap::new)
}

#[tauri::command]
fn connect(
    state: TauriState<'_, AppState>,
    profile: String,
    node: Option<String>,
) -> Result<State, String> {
    state.backend.connect(profile, node)
}

#[tauri::command]
fn disconnect(state: TauriState<'_, AppState>) -> Result<State, String> {
    state.backend.disconnect()
}

#[tauri::command]
fn ping(state: TauriState<'_, AppState>, profile: String) -> Result<PingList, String> {
    state
        .backend
        .ping(profile)
        .map(|results| PingList { results })
}

#[tauri::command]
fn set_routing(state: TauriState<'_, AppState>, mode: RoutingMode) -> Result<State, String> {
    state.backend.set_routing(mode)
}

#[tauri::command]
fn leak_check(state: TauriState<'_, AppState>) -> Result<LeakCheck, String> {
    state.backend.leak_check()
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
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_dialog::init())
        .setup(|app| {
            let sink: Arc<dyn EventSink> = Arc::new(TauriSink {
                app: app.handle().clone(),
            });
            let backend = make_backend(sink);
            app.manage(AppState { backend });
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            status,
            list_profiles,
            import_subscription,
            import_link,
            remove_profile,
            refresh_subscription,
            connect,
            disconnect,
            ping,
            set_routing,
            leak_check,
        ])
        .run(tauri::generate_context!())
        .expect("error while running Tenebra");
}
