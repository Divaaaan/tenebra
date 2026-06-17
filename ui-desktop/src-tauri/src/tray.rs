//! System tray: a single icon with a Show / Connect / Disconnect / Quit menu,
//! a left-click that toggles the main window, and a tooltip that mirrors the
//! live connection state.
//!
//! Two of the menu actions can't be handled here alone. "Connect" needs a
//! profile, which the front end owns, so it emits `tray://connect` for the
//! webview to handle with its normal selected-profile flow; "Show" likewise
//! emits `tray://show` (the front end may want to route somewhere) in addition
//! to raising the window. "Disconnect" needs no profile, so it drives the
//! backend directly, and "Quit" is the one path that actually exits the app.

use tauri::{
    menu::{Menu, MenuEvent, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent},
    AppHandle, Emitter, Manager,
};

use crate::backend::{ConnectionState, State};
use crate::{disconnect_backend, focus_main_window, tooltip_for};

/// The id we build the tray with, so the event sink can find it again via
/// [`AppHandle::tray_by_id`] without threading a handle through managed state.
const TRAY_ID: &str = "main";

/// Menu item ids. Matched in the menu-event handler; kept as constants so the
/// build site and the handler can't drift apart.
const MENU_SHOW: &str = "show";
const MENU_CONNECT: &str = "connect";
const MENU_DISCONNECT: &str = "disconnect";
const MENU_QUIT: &str = "quit";

/// Events the tray emits for the front end to act on. `tray://connect` triggers
/// the renderer's selected-profile connect flow; `tray://show` lets it react to
/// being surfaced. The webview listens via `onTrayConnect`/`onTrayShow` in
/// `src/api/client.ts`, wired up in `src/App.tsx`.
pub const EVENT_TRAY_CONNECT: &str = "tray://connect";
pub const EVENT_TRAY_SHOW: &str = "tray://show";

/// Build the tray icon and install its menu and event handlers. Called once
/// from `setup`. The labels are plain English; the webview owns localized UI,
/// and a system tray menu is the one surface that lives outside it.
pub fn create(app: &AppHandle) -> tauri::Result<()> {
    let show = MenuItem::with_id(app, MENU_SHOW, "Show Tenebra", true, None::<&str>)?;
    let connect = MenuItem::with_id(app, MENU_CONNECT, "Connect", true, None::<&str>)?;
    let disconnect = MenuItem::with_id(app, MENU_DISCONNECT, "Disconnect", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, MENU_QUIT, "Quit", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&show, &connect, &disconnect, &quit])?;

    TrayIconBuilder::with_id(TRAY_ID)
        .icon(
            app.default_window_icon().cloned().ok_or_else(|| {
                tauri::Error::AssetNotFound("default window icon for tray".into())
            })?,
        )
        .tooltip(tooltip_for(ConnectionState::Idle))
        // We handle left-click ourselves to toggle the window; the menu opens on
        // a right-click, the platform-conventional behaviour on Windows.
        .show_menu_on_left_click(false)
        .menu(&menu)
        .on_menu_event(on_menu_event)
        .on_tray_icon_event(on_tray_icon_event)
        .build(app)?;

    Ok(())
}

/// Refresh the tray tooltip to match the latest backend state. Called from the
/// event sink, the single place backend state flows through. A no-op if the
/// tray isn't up yet (e.g. an event racing setup).
pub fn sync_state(app: &AppHandle, state: &State) {
    if let Some(tray) = app.tray_by_id(TRAY_ID) {
        let _ = tray.set_tooltip(Some(tooltip_for(state.state)));
    }
}

/// Route a tray menu selection. Connect/Show defer to the front end (which owns
/// the selected profile); Disconnect and Quit act directly.
fn on_menu_event(app: &AppHandle, event: MenuEvent) {
    match event.id().as_ref() {
        MENU_SHOW => {
            focus_main_window(app);
            let _ = app.emit(EVENT_TRAY_SHOW, ());
        }
        MENU_CONNECT => {
            // Raise the window so the connect flow (and any prompts it shows) is
            // visible, then let the front end connect the selected profile.
            focus_main_window(app);
            let _ = app.emit(EVENT_TRAY_CONNECT, ());
        }
        MENU_DISCONNECT => disconnect_backend(app),
        MENU_QUIT => app.exit(0),
        _ => {}
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
