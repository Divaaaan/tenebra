//! `tenebra://` deep-link handling: parse an incoming link into an action, and
//! deliver those actions to the webview.
//!
//! The grammar is intentionally small and neutral:
//!   - `tenebra://import?url=<subscription-url>` — open the import flow with the
//!     subscription URL pre-filled.
//!   - `tenebra://connect?profile=<id>` — connect the named profile.
//!
//! Parsing lives here (one source of truth) rather than in the front end, so the
//! webview only ever receives an already-validated, tagged action. Delivery has
//! two paths: a link that arrives while the app runs is emitted as an event; a
//! link that *launched* the app (cold start) is captured before the webview can
//! listen and drained by the front end on mount.

use std::sync::Mutex;
use std::time::{Duration, Instant};

use serde::Serialize;
use tauri::{AppHandle, Emitter, Manager};
use url::Url;

/// The custom URL scheme the desktop app claims.
const SCHEME: &str = "tenebra";

/// Event carrying one routed deep link to the webview. Mirrors the tray's
/// `tray://…` events, but with a payload. The front end listens via `onDeepLink`.
pub const EVENT_DEEP_LINK: &str = "deep-link://action";

/// How long a delivered URL is remembered for de-duplication. A second launch on
/// Windows can reach the primary instance twice — once via the single-instance
/// plugin's built-in CLI forwarding (which drives `on_open_url`) and once via our
/// own argv fallback — so the same link must not be acted on twice.
const DEDUP_WINDOW: Duration = Duration::from_secs(3);

/// A parsed deep link, tagged so the front end can dispatch on `action`. The
/// wire form is `{ "action": "import", "url": … }` / `{ "action": "connect",
/// "profile": … }`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(tag = "action", rename_all = "lowercase")]
pub enum DeepLinkAction {
    /// Open the import flow pre-filled with a subscription URL.
    Import { url: String },
    /// Connect a profile by id.
    Connect { profile: String },
}

/// Managed state for deep-link delivery: the cold-start queue plus the recent-URL
/// window used to drop duplicate live deliveries.
#[derive(Default)]
pub struct DeepLinkState {
    /// Actions parsed from the launch command line, held until the front end
    /// drains them on mount. At setup time the webview isn't listening yet, so an
    /// emitted event would be lost — the queue bridges that gap.
    launch: Mutex<Vec<DeepLinkAction>>,
    /// URLs delivered within the last [`DEDUP_WINDOW`], newest kept, so the two
    /// warm-delivery paths don't act on one link twice.
    recent: Mutex<Vec<(String, Instant)>>,
}

impl DeepLinkState {
    /// Take and clear the queued launch actions. Called once by the front end.
    pub fn take_launch(&self) -> Vec<DeepLinkAction> {
        std::mem::take(&mut self.launch.lock().unwrap())
    }

    /// Record `url` and report whether it is fresh (not seen within the window).
    /// A no-op-safe wrapper over [`accept_at`] using the real clock.
    fn accept(&self, url: &str) -> bool {
        accept_at(&mut self.recent.lock().unwrap(), url, Instant::now())
    }
}

/// Parse a `tenebra://…` link into an action. Returns `None` for a foreign
/// scheme, an unknown verb, or a missing/blank parameter — an unroutable link is
/// ignored, never a panic.
pub fn route(raw: &str) -> Option<DeepLinkAction> {
    let url = Url::parse(raw.trim()).ok()?;
    if url.scheme() != SCHEME {
        return None;
    }
    // The host is the verb: tenebra://import / tenebra://connect. `url` lower-cases
    // it, so the match is effectively case-insensitive.
    match url.host_str()? {
        "import" => {
            let sub = query_value(&url, "url")?;
            (!sub.is_empty()).then_some(DeepLinkAction::Import { url: sub })
        }
        "connect" => {
            let profile = query_value(&url, "profile")?;
            (!profile.is_empty()).then_some(DeepLinkAction::Connect { profile })
        }
        _ => None,
    }
}

/// The trimmed, percent-decoded value of query parameter `key`, if present.
fn query_value(url: &Url, key: &str) -> Option<String> {
    url.query_pairs()
        .find(|(k, _)| k == key)
        .map(|(_, v)| v.trim().to_string())
}

/// Pull the `tenebra://…` URLs out of a process argument list. This is the
/// fallback for a second launch: on Windows the OS hands the link to the new
/// process as a CLI argument, which the single-instance callback forwards here.
pub fn find_urls(args: &[String]) -> Vec<String> {
    args.iter()
        .filter(|a| a.to_ascii_lowercase().starts_with("tenebra://"))
        .cloned()
        .collect()
}

/// De-dup core, split out so it can be tested with a supplied clock: prune
/// entries older than the window, then accept `url` only if it isn't already
/// present, recording it when fresh.
fn accept_at(recent: &mut Vec<(String, Instant)>, url: &str, now: Instant) -> bool {
    recent.retain(|(_, at)| now.duration_since(*at) < DEDUP_WINDOW);
    if recent.iter().any(|(u, _)| u == url) {
        return false;
    }
    recent.push((url.to_string(), now));
    true
}

/// Route live URLs (from the deep-link plugin's `on_open_url` or the argv
/// fallback) and emit one event per fresh, routable link. Duplicate deliveries of
/// the same link within the window are dropped.
pub fn deliver_live(app: &AppHandle, urls: &[String]) {
    let state = app.state::<DeepLinkState>();
    for raw in urls {
        if let Some(action) = route(raw) {
            if state.accept(raw) {
                let _ = app.emit(EVENT_DEEP_LINK, action);
            }
        }
    }
}

/// Parse the launch command line and queue any deep links for the front end to
/// drain on mount. Cold start only; duplicates (e.g. a URL listed by both the
/// plugin's `get_current` and the raw argv) are collapsed.
pub fn capture_launch(app: &AppHandle, urls: &[String]) {
    let state = app.state::<DeepLinkState>();
    let mut launch = state.launch.lock().unwrap();
    for raw in urls {
        if let Some(action) = route(raw) {
            if !launch.contains(&action) {
                launch.push(action);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn route_parses_import_with_encoded_url() {
        // The subscription URL is percent-encoded in the link and must come back
        // decoded, ready to hand to the import flow.
        let action = route("tenebra://import?url=https%3A%2F%2Fexample.invalid%2Fsub%3Ft%3D1");
        assert_eq!(
            action,
            Some(DeepLinkAction::Import {
                url: "https://example.invalid/sub?t=1".into(),
            })
        );
    }

    #[test]
    fn route_parses_connect_with_profile() {
        assert_eq!(
            route("tenebra://connect?profile=demo-sub"),
            Some(DeepLinkAction::Connect {
                profile: "demo-sub".into(),
            })
        );
    }

    #[test]
    fn route_ignores_extra_query_params() {
        // Unknown params alongside the one we need are tolerated.
        assert_eq!(
            route("tenebra://connect?ref=x&profile=p1&z=9"),
            Some(DeepLinkAction::Connect {
                profile: "p1".into(),
            })
        );
    }

    #[test]
    fn route_rejects_foreign_scheme() {
        assert_eq!(route("https://import?url=https://x"), None);
        assert_eq!(route("tenebrax://import?url=https://x"), None);
    }

    #[test]
    fn route_rejects_unknown_verb() {
        assert_eq!(route("tenebra://delete?profile=p1"), None);
        assert_eq!(route("tenebra://import?profile=p1"), None); // wrong param for verb
    }

    #[test]
    fn route_rejects_missing_or_blank_params() {
        assert_eq!(route("tenebra://import"), None);
        assert_eq!(route("tenebra://import?url="), None);
        assert_eq!(route("tenebra://connect?profile="), None);
        assert_eq!(route("tenebra://connect?profile=%20%20"), None); // whitespace only
    }

    #[test]
    fn route_does_not_panic_on_garbage() {
        // A range of malformed inputs must all fold to None rather than panic.
        for junk in [
            "",
            "   ",
            "not a url",
            "tenebra://",
            "tenebra:///",
            "tenebra://connect",
            "://",
            "tenebra://%%%?profile=p",
        ] {
            assert_eq!(route(junk), None, "expected None for {junk:?}");
        }
    }

    #[test]
    fn find_urls_picks_the_link_out_of_argv() {
        // The launch argv holds the exe path, some flags, and the link somewhere
        // among them; the fallback must find exactly the tenebra:// entries.
        let argv = vec![
            "C:\\Program Files\\Tenebra\\tenebra.exe".to_string(),
            "--minimized".to_string(),
            "tenebra://connect?profile=p1".to_string(),
        ];
        assert_eq!(find_urls(&argv), vec!["tenebra://connect?profile=p1"]);
    }

    #[test]
    fn find_urls_is_scheme_case_insensitive_and_handles_none() {
        assert_eq!(
            find_urls(&["TENEBRA://import?url=https://x".to_string()]),
            vec!["TENEBRA://import?url=https://x"]
        );
        assert!(find_urls(&["tenebra.exe".to_string(), "--flag".to_string()]).is_empty());
        assert!(find_urls(&[]).is_empty());
    }

    #[test]
    fn accept_at_drops_a_duplicate_inside_the_window() {
        let mut recent = Vec::new();
        let t0 = Instant::now();
        assert!(accept_at(&mut recent, "tenebra://connect?profile=p", t0));
        // Same URL a moment later (within the window) is a duplicate.
        assert!(!accept_at(
            &mut recent,
            "tenebra://connect?profile=p",
            t0 + Duration::from_millis(50)
        ));
        // A different URL in the same window is still accepted.
        assert!(accept_at(
            &mut recent,
            "tenebra://connect?profile=q",
            t0 + Duration::from_millis(60)
        ));
    }

    #[test]
    fn accept_at_readmits_after_the_window() {
        let mut recent = Vec::new();
        let t0 = Instant::now();
        assert!(accept_at(&mut recent, "tenebra://import?url=https://x", t0));
        // Past the window the same link is fresh again, and the stale entry was
        // pruned rather than accumulating.
        assert!(accept_at(
            &mut recent,
            "tenebra://import?url=https://x",
            t0 + DEDUP_WINDOW + Duration::from_millis(1)
        ));
        assert_eq!(recent.len(), 1, "expired entry should have been pruned");
    }

    #[test]
    fn action_serializes_with_an_action_tag() {
        // The front end dispatches on `action`; lock that wire shape.
        let import = serde_json::to_value(DeepLinkAction::Import {
            url: "https://example.invalid/sub".into(),
        })
        .unwrap();
        assert_eq!(
            import,
            serde_json::json!({ "action": "import", "url": "https://example.invalid/sub" })
        );

        let connect = serde_json::to_value(DeepLinkAction::Connect {
            profile: "p1".into(),
        })
        .unwrap();
        assert_eq!(
            connect,
            serde_json::json!({ "action": "connect", "profile": "p1" })
        );
    }
}
