//! System tray: a single icon whose icon, tooltip and menu mirror the live
//! connection state, plus quick actions that drive the backend directly — dial
//! the current profile, dial a specific node from a submenu, or disconnect — all
//! without surfacing the window. A left-click toggles the main window.
//!
//! The node submenu lists the active profile's nodes. That list isn't carried on
//! the state event, so it is fetched from the backend, cached, and combined with
//! each state update to rebuild the whole menu. The fetch is a *blocking* backend
//! call and must not run on the event reader thread: that thread delivers the
//! very response the call would block waiting for, so an inline fetch would
//! deadlock until the request timed out. It therefore runs on a worker thread
//! (see [`refresh_profiles`]), triggered once at startup and again whenever the
//! profile set changes (the `profiles` event).
//!
//! Two actions still can't be fully handled here. "Show" raises the window and
//! also emits `tray://show` so the front end can react (e.g. close an overlay);
//! it is the one action the front end still shares. "Quit" is the one path that
//! actually exits the app. Connect and Disconnect need no profile from the front
//! end — the tray owns the target now — so they drive the backend directly.

use std::sync::Mutex;

use serde_json::json;
use tauri::{
    image::Image,
    menu::{CheckMenuItem, IsMenuItem, Menu, MenuEvent, MenuItem, PredefinedMenuItem, Submenu},
    tray::{MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent},
    AppHandle, Emitter, Manager, Wry,
};

use crate::backend::{ConnectionState, Profile, State, EVENT_LOG};
use crate::{
    connect_backend, disconnect_backend, focus_main_window, list_profiles_blocking, tooltip_for,
};

// State icons, decoded at compile time from the bundled PNGs (path resolved from
// the crate manifest dir, `src-tauri/`). A ring that reads idle / connected /
// error at a glance in the tray.
const ICON_IDLE: Image<'static> = tauri::include_image!("./icons/tray-idle.png");
const ICON_CONNECTED: Image<'static> = tauri::include_image!("./icons/tray-connected.png");
const ICON_ERROR: Image<'static> = tauri::include_image!("./icons/tray-error.png");

/// The tray icon for a connection state: connected and error each have their own,
/// everything else (idle, connecting) uses the neutral idle ring.
fn icon_for(state: ConnectionState) -> Image<'static> {
    match state {
        ConnectionState::Connected => ICON_CONNECTED,
        ConnectionState::Error => ICON_ERROR,
        _ => ICON_IDLE,
    }
}

/// The id we build the tray with, so the event sink can find it again via
/// [`AppHandle::tray_by_id`] without threading a handle through managed state.
const TRAY_ID: &str = "main";

/// Fixed menu-item ids. Matched in the menu-event handler; kept as constants so
/// the build site and the handler can't drift apart.
const MENU_SHOW: &str = "show";
const MENU_QUICK_CONNECT: &str = "quick_connect";
const MENU_DISCONNECT: &str = "disconnect";
const MENU_QUIT: &str = "quit";
/// Prefix for a per-node connect item's id; the node id is appended and parsed
/// back out in the handler. Distinct from every fixed id above and chosen so it
/// can't be the start of a node id, so the two never alias.
const MENU_NODE_PREFIX: &str = "node:";

/// Event the tray emits for the front end to act on: `tray://show` lets the
/// renderer react to being surfaced (it also raises the window itself). The
/// webview listens via `onTrayShow` in `src/api/client.ts`, wired up in
/// `src/App.tsx`. Connecting no longer routes through the front end — the tray
/// dials the backend directly — so it emits no connect event.
pub const EVENT_TRAY_SHOW: &str = "tray://show";

/// The inputs the tray menu is built from. The connection state arrives on the
/// state event; the profile list is fetched separately (it isn't on that event)
/// and cached here, so an update to either can rebuild the menu from both.
#[derive(Clone, Default)]
struct TrayModel {
    /// The latest connection state, or `None` before the first state event.
    state: Option<State>,
    /// The last known profile set, source of the node submenu. Empty until the
    /// first fetch completes.
    profiles: Vec<Profile>,
}

/// Managed tray state: the model behind a mutex, shared by the event sink (which
/// updates the connection state) and the worker that refreshes the profile list.
#[derive(Default)]
struct TrayState {
    model: Mutex<TrayModel>,
}

/// Build the tray icon and install its initial menu and event handlers. Called
/// once from `setup`. The labels are plain English; the webview owns localized
/// UI, and a system tray menu is the one surface that lives outside it.
pub fn create(app: &AppHandle) -> tauri::Result<()> {
    app.manage(TrayState::default());

    // The initial menu is built from an empty model: idle, no profiles yet. The
    // profile fetch below fills in the node submenu once it returns.
    let menu = build_menu(app, &TrayModel::default(), ConnectionState::Idle)?;

    TrayIconBuilder::with_id(TRAY_ID)
        .icon(icon_for(ConnectionState::Idle))
        .tooltip(tooltip_for(ConnectionState::Idle))
        // We handle left-click ourselves to toggle the window; the menu opens on
        // a right-click, the platform-conventional behaviour on Windows.
        .show_menu_on_left_click(false)
        .menu(&menu)
        .on_menu_event(on_menu_event)
        .on_tray_icon_event(on_tray_icon_event)
        .build(app)?;

    // Load the profile list so the node submenu is populated from the start.
    refresh_profiles(app);
    Ok(())
}

/// Refresh the tray to match the latest backend state: rebuild the menu (tooltip,
/// state icon, and which actions apply) from the new state combined with the
/// cached profile list. Called from the event sink, the single place backend
/// state flows through. If the state names a profile the tray hasn't loaded yet,
/// it also kicks off a profile refresh so the node submenu can catch up.
pub fn sync_state(app: &AppHandle, state: &State) {
    let Some(tray_state) = app.try_state::<TrayState>() else {
        return;
    };
    // Snapshot under the lock, then build the menu without holding it: building
    // marshals onto the main thread, which may itself be waiting on this lock in
    // a menu handler — holding it across the hop could deadlock.
    let (model, unknown_profile) = {
        let mut model = tray_state.model.lock().unwrap();
        model.state = Some(state.clone());
        let unknown = state
            .profile
            .as_deref()
            .is_some_and(|id| !model.profiles.iter().any(|p| p.id == id));
        (model.clone(), unknown)
    };
    apply(app, &model);
    if unknown_profile {
        refresh_profiles(app);
    }
}

/// The stored profile set changed (an import, a removal, or a subscription
/// refresh that may have altered a node list). Reload it and rebuild the menu.
pub fn sync_profiles(app: &AppHandle) {
    refresh_profiles(app);
}

/// Fetch the profile list on a worker thread, cache it, and rebuild the menu.
/// Deliberately off-thread: `list_profiles` blocks on the backend, and the state
/// event that commonly triggers a refresh is delivered on the backend's reader
/// thread — calling the backend from there would block that thread waiting for a
/// response only it can read. A failure is non-fatal: the menu keeps its current
/// node list and the reason is surfaced on the log channel.
fn refresh_profiles(app: &AppHandle) {
    let app = app.clone();
    tauri::async_runtime::spawn_blocking(move || match list_profiles_blocking(&app) {
        Ok(profiles) => {
            let Some(tray_state) = app.try_state::<TrayState>() else {
                return;
            };
            let model = {
                let mut model = tray_state.model.lock().unwrap();
                model.profiles = profiles;
                model.clone()
            };
            apply(&app, &model);
        }
        Err(e) => {
            let _ = app.emit(
                EVENT_LOG,
                json!({ "level": "warn", "msg": format!("tray: could not load profiles: {e}") }),
            );
        }
    });
}

/// Install the icon, tooltip and a freshly built menu for `model`. A no-op for
/// whatever isn't up yet (e.g. an event racing tray creation). A menu that fails
/// to build leaves the current one in place rather than tearing it down.
fn apply(app: &AppHandle, model: &TrayModel) {
    let conn = model
        .state
        .as_ref()
        .map_or(ConnectionState::Idle, |s| s.state);
    let Some(tray) = app.tray_by_id(TRAY_ID) else {
        return;
    };
    let _ = tray.set_tooltip(Some(tooltip_for(conn)));
    let _ = tray.set_icon(Some(icon_for(conn)));
    if let Ok(menu) = build_menu(app, model, conn) {
        let _ = tray.set_menu(Some(menu));
    }
}

/// Build the whole tray menu for `model`: Show, then the connect actions (a
/// direct quick-connect and a per-node submenu for the target profile), then
/// Disconnect and Quit. Rebuilt wholesale on every update — the profile and its
/// nodes can change — so the node list and the enabled/checked state always match
/// the live model.
fn build_menu(
    app: &AppHandle,
    model: &TrayModel,
    conn: ConnectionState,
) -> tauri::Result<Menu<Wry>> {
    let connected = conn == ConnectionState::Connected;
    let idle = conn == ConnectionState::Idle;

    // The profile whose nodes the submenu offers, and that the connect actions
    // target. `None` only when no profiles are loaded.
    let target = resolve_target(model.state.as_ref(), &model.profiles);
    // The node the tunnel is on (or coming up on); only meaningful when not idle.
    let active_node_id = if idle {
        None
    } else {
        model.state.as_ref().and_then(|s| s.node.as_deref())
    };

    let show = MenuItem::with_id(app, MENU_SHOW, "Show Tenebra", true, None::<&str>)?;

    // Quick connect: dial the target profile's default node directly, no window.
    // Pointless once connected, and impossible with no profile to target.
    let quick = MenuItem::with_id(
        app,
        MENU_QUICK_CONNECT,
        "Quick connect",
        !connected && target.is_some(),
        None::<&str>,
    )?;

    // Per-node connect submenu. Each item dials its node directly; the active
    // node is checked and disabled (you are already on it). Built fresh so the
    // check marks and the node set track the live state.
    let node_items: Vec<CheckMenuItem<Wry>> = match target {
        Some(p) => p
            .nodes
            .iter()
            .map(|n| {
                let is_active = active_node_id == Some(n.id.as_str());
                CheckMenuItem::with_id(
                    app,
                    format!("{MENU_NODE_PREFIX}{}", n.id),
                    &n.name,
                    !is_active,
                    is_active,
                    None::<&str>,
                )
            })
            .collect::<tauri::Result<_>>()?,
        None => Vec::new(),
    };
    let node_refs: Vec<&dyn IsMenuItem<Wry>> = node_items
        .iter()
        .map(|i| i as &dyn IsMenuItem<Wry>)
        .collect();
    // A submenu with no nodes to offer is shown disabled rather than dropped, so
    // its absence never reads as a glitch.
    let nodes = Submenu::with_items(app, "Connect to", !node_refs.is_empty(), &node_refs)?;

    let disconnect = MenuItem::with_id(app, MENU_DISCONNECT, "Disconnect", !idle, None::<&str>)?;
    let quit = MenuItem::with_id(app, MENU_QUIT, "Quit", true, None::<&str>)?;

    let sep_actions = PredefinedMenuItem::separator(app)?;
    let sep_quit = PredefinedMenuItem::separator(app)?;

    let items: Vec<&dyn IsMenuItem<Wry>> = vec![
        &show,
        &sep_actions,
        &quick,
        &nodes,
        &disconnect,
        &sep_quit,
        &quit,
    ];
    Menu::with_items(app, &items)
}

/// The profile the tray targets for its connect actions and node submenu: the
/// active one if the state names one we hold, else the first loaded, else `None`
/// (no profiles). Pure, so the resolution the submenu is built from and the one a
/// click dials are provably the same — a node item can never resolve to a
/// different profile than the submenu it sat under.
fn resolve_target<'a>(state: Option<&State>, profiles: &'a [Profile]) -> Option<&'a Profile> {
    let active = state.and_then(|s| s.profile.as_deref());
    active
        .and_then(|id| profiles.iter().find(|p| p.id == id))
        .or_else(|| profiles.first())
}

/// The id of the profile [`resolve_target`] picks, read from the live model.
/// `None` when the tray has no profiles yet — the actions that would use it are
/// disabled in that case.
fn target_profile_id(app: &AppHandle) -> Option<String> {
    let tray_state = app.try_state::<TrayState>()?;
    let model = tray_state.model.lock().unwrap();
    resolve_target(model.state.as_ref(), &model.profiles).map(|p| p.id.clone())
}

/// Route a tray menu selection. Show raises the window (and lets the front end
/// react); Quick connect and the per-node items dial the backend directly with
/// no window; Disconnect and Quit act directly.
fn on_menu_event(app: &AppHandle, event: MenuEvent) {
    match event.id().as_ref() {
        MENU_SHOW => {
            focus_main_window(app);
            let _ = app.emit(EVENT_TRAY_SHOW, ());
        }
        MENU_QUICK_CONNECT => {
            if let Some(profile) = target_profile_id(app) {
                connect_backend(app, profile, None);
            }
        }
        MENU_DISCONNECT => disconnect_backend(app),
        MENU_QUIT => app.exit(0),
        // A per-node item: dial exactly that node of the current target profile.
        other => {
            if let Some(node) = other.strip_prefix(MENU_NODE_PREFIX) {
                if let Some(profile) = target_profile_id(app) {
                    connect_backend(app, profile, Some(node.to_string()));
                }
            }
        }
    }
}

/// Left-click toggles the main window's visibility; anything else (right-click,
/// which opens the menu) is left to the platform.
fn on_tray_icon_event(tray: &TrayIcon, event: TrayIconEvent) {
    if let TrayIconEvent::Click {
        button: MouseButton::Left,
        button_state: MouseButtonState::Up,
        ..
    } = event
    {
        toggle_main_window(tray.app_handle());
    }
}

/// Hide the window if it's visible, otherwise show and focus it.
fn toggle_main_window(app: &AppHandle) {
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    if window.is_visible().unwrap_or(false) {
        let _ = window.hide();
    } else {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}

#[cfg(test)]
mod tests {
    //! The menu building itself needs a running app (Tauri marshals every item
    //! onto the main thread), so it isn't unit-tested here. What is testable
    //! without an app is the pure logic the menu and the click handler both lean
    //! on: which profile the tray targets, and the node-id encoding that carries a
    //! node through the menu and back.

    use super::*;
    use crate::backend::{Node, Protocol, Source};

    fn profile(id: &str, node_ids: &[&str]) -> Profile {
        Profile {
            id: id.into(),
            name: format!("Profile {id}"),
            source: Source::Manual,
            url: None,
            nodes: node_ids
                .iter()
                .map(|n| Node {
                    id: (*n).into(),
                    name: format!("Node {n}"),
                    protocol: Protocol::Vless,
                    server: "198.51.100.1".into(),
                    port: 443,
                    insecure: false,
                })
                .collect(),
            updated_at: "2026-01-01T00:00:00Z".into(),
            expires_at: None,
            traffic_used: None,
            traffic_total: None,
            managed: false,
            tier: String::new(),
        }
    }

    /// An idle state naming (or not) an active profile; the fields the tray reads.
    fn state_for(active_profile: Option<&str>) -> State {
        State {
            state: ConnectionState::Idle,
            node: None,
            profile: active_profile.map(Into::into),
            routing: None,
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
            crash_reports: None,
            crash_reports_asked: false,
            error: None,
        }
    }

    #[test]
    fn resolve_target_prefers_the_active_profile() {
        let profiles = vec![profile("a", &["a1"]), profile("b", &["b1"])];
        let s = state_for(Some("b"));
        assert_eq!(
            resolve_target(Some(&s), &profiles).map(|p| p.id.as_str()),
            Some("b")
        );
    }

    #[test]
    fn resolve_target_falls_back_to_the_first_profile() {
        let profiles = vec![profile("a", &["a1"]), profile("b", &["b1"])];
        // Active names a profile we don't hold (e.g. removed): use the first.
        let unknown = state_for(Some("missing"));
        assert_eq!(
            resolve_target(Some(&unknown), &profiles).map(|p| p.id.as_str()),
            Some("a")
        );
        // No active profile named: the first.
        let none = state_for(None);
        assert_eq!(
            resolve_target(Some(&none), &profiles).map(|p| p.id.as_str()),
            Some("a")
        );
        // No state yet at all: still the first.
        assert_eq!(
            resolve_target(None, &profiles).map(|p| p.id.as_str()),
            Some("a")
        );
    }

    #[test]
    fn resolve_target_is_none_without_profiles() {
        assert!(resolve_target(Some(&state_for(Some("a"))), &[]).is_none());
        assert!(resolve_target(None, &[]).is_none());
    }

    #[test]
    fn node_menu_id_round_trips_and_stays_distinct_from_fixed_ids() {
        // A node id encodes into a menu id and parses cleanly back out.
        let id = format!("{MENU_NODE_PREFIX}nl-ams-01");
        assert_eq!(id.strip_prefix(MENU_NODE_PREFIX), Some("nl-ams-01"));
        // None of the fixed ids look like a node item, so the handler's node
        // branch can never swallow one.
        for fixed in [MENU_SHOW, MENU_QUICK_CONNECT, MENU_DISCONNECT, MENU_QUIT] {
            assert!(
                fixed.strip_prefix(MENU_NODE_PREFIX).is_none(),
                "fixed id {fixed} must not parse as a node item"
            );
        }
    }
}
