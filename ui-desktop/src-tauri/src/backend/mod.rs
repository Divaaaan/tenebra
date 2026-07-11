//! The boundary the UI talks to.
//!
//! Every implementation of the [`Backend`] trait drives the same control
//! protocol (see `docs/control-protocol.md`); the Tauri command layer in
//! `lib.rs` never has to know which one is wired in. [`wire`] holds the
//! transport-agnostic protocol client; [`sidecar`] runs it over a spawned
//! core's stdin/stdout; [`pipe`] (Windows) runs it over the named pipe of a
//! core that outlives the GUI — the service or `tenebra-core --pipe`; [`unix`]
//! (macOS) runs it over the unix domain socket of a root LaunchDaemon that
//! likewise outlives the GUI — `tenebra-core --socket`; [`mock`] is an
//! in-process fake for UI work without the core. `make_backend` in `lib.rs`
//! picks one at startup.
//!
//! The structs below mirror the protocol's `State`, `Node`, `Profile` and
//! `PingResult` shapes. They serialize to exactly the JSON the front-end types
//! in `src/api/types.ts` expect.

pub mod mock;
#[cfg(windows)]
pub mod pipe;
pub mod sidecar;
#[cfg(test)]
pub mod testutil;
#[cfg(target_os = "macos")]
pub mod unix;
pub mod wire;

use serde::{Deserialize, Serialize};

/// Channels the backend pushes events on. The names match the protocol's
/// `event` field and the listeners registered in `src/api/client.ts`.
pub const EVENT_STATE: &str = "state";
pub const EVENT_TRAFFIC: &str = "traffic";
pub const EVENT_LOG: &str = "log";
pub const EVENT_PROFILES: &str = "profiles";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ConnectionState {
    Idle,
    Connecting,
    Connected,
    Error,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum RoutingMode {
    Smart,
    Global,
    Direct,
}

/// Per-app split tunnelling mode. `Off` leaves base routing untouched;
/// `Exclude` sends the listed apps direct; `Include` routes only the listed
/// apps through the proxy. Mirrors the core's split mode.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum SplitMode {
    Off,
    Exclude,
    Include,
}

/// The tun network stack sing-box runs. `System` is the kernel's own TCP/IP
/// (fastest, the default); `Gvisor` is a userspace stack (slower, immune to tun
/// driver quirks); `Mixed` puts TCP on system and UDP on gvisor. Mirrors the
/// core's stack tokens.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum TunStack {
    System,
    Gvisor,
    Mixed,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Protocol {
    Vless,
    Hysteria2,
    Amneziawg,
    Shadowsocks,
    Trojan,
    Vmess,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Source {
    Subscription,
    Manual,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct State {
    pub state: ConnectionState,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub node: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub profile: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub routing: Option<RoutingMode>,
    /// Per-app split mode; absent (treated as off) when no split is active.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub split: Option<SplitMode>,
    /// Executable names the split applies to; absent when off.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub split_apps: Option<Vec<String>>,
    /// Whether the kill switch is armed; absent (treated as off) when it isn't.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub kill_switch: Option<bool>,
    /// The tun network stack the current or next tunnel uses.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tun_stack: Option<TunStack>,
    /// Whether the core reconnects the last profile when the daemon starts;
    /// absent (treated as off) when it doesn't.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub autoconnect: Option<bool>,
    /// Whether DNS ad/tracker blocking is armed; absent (treated as off) when it
    /// isn't.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ad_block: Option<bool>,
    /// The resolvers the current or next tunnel uses: the encrypted resolver
    /// reached over the proxy and the direct one. Present once the core has
    /// normalized them, so the UI can prefill the custom-DNS inputs with the
    /// effective values.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dns_remote: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dns_direct: Option<String>,
    /// Whether the DNS strategy is pinned to IPv4-only (A records, no AAAA);
    /// absent (treated as off) when it isn't.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ipv4_only: Option<bool>,
    /// The custom domain-suffix routing rules the current or next tunnel uses:
    /// destinations pinned to the direct outbound, and to the proxy. Normalized;
    /// absent when empty, like the split fields.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub rules_direct: Option<Vec<String>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub rules_proxy: Option<Vec<String>>,
    /// Whether the bundled Russian banking / government direct-rule presets are
    /// on; absent (treated as off) when they aren't.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub preset_ru_banking: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub preset_ru_gov: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Node {
    pub id: String,
    pub name: String,
    pub protocol: Protocol,
    pub server: String,
    pub port: u16,
    /// TLS certificate verification is off on this node (the subscription's
    /// skip-cert-verify, carried through to sing-box's `insecure:true`). The
    /// core omits the field when verification is on, so `#[serde(default)]`
    /// treats a missing field as `false`. Passed through to the webview so the
    /// UI can flag the on-path-interception trade-off.
    #[serde(default)]
    pub insecure: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Profile {
    pub id: String,
    pub name: String,
    pub source: Source,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub url: Option<String>,
    pub nodes: Vec<Node>,
    #[serde(rename = "updatedAt")]
    pub updated_at: String,
    #[serde(rename = "expiresAt", skip_serializing_if = "Option::is_none")]
    pub expires_at: Option<String>,
    #[serde(rename = "trafficUsed", skip_serializing_if = "Option::is_none")]
    pub traffic_used: Option<u64>,
    #[serde(rename = "trafficTotal", skip_serializing_if = "Option::is_none")]
    pub traffic_total: Option<u64>,
}

/// Result of a batch link import (`import_links`): the single profile created
/// from all the valid links, plus how many links were imported and how many were
/// skipped because they didn't parse. Mirrors the core's wrapped response so the
/// UI can report "imported N, skipped M".
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ImportLinksResult {
    pub profile: Profile,
    pub imported: u32,
    pub skipped: u32,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PingResult {
    /// The node id this result is for.
    pub node: String,
    /// Round-trip time in milliseconds. Widened to match the core's wire type
    /// (`RTTMs int64`): a value the core reports as negative (e.g. a sentinel for
    /// a failed probe) or beyond `u32` would otherwise fail to decode and turn the
    /// whole ping response into an error, losing every node.
    pub rtt_ms: i64,
    pub ok: bool,
}

/// The IP-vs-exit comparison outcome, mirroring the core's `ExitVerdict`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ExitMatch {
    Match,
    Mismatch,
    Unknown,
}

/// Severity of a leak-check finding the UI maps to pass/warn styling, mirroring
/// the core's `Verdict`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Verdict {
    Ok,
    Warn,
    Neutral,
    Error,
}

/// DNS leak assessment outcome, mirroring the core's `DNSStatus`. `Inconclusive`
/// and `Unavailable` are deliberately distinct from a pass — the UI must never
/// present them as "safe".
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum DnsStatus {
    Ok,
    Leak,
    Inconclusive,
    Unavailable,
}

/// The DNS portion of a leak check.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct DnsResult {
    pub status: DnsStatus,
    /// Observed resolver IPs, if any. Best-effort; may be empty even on a
    /// successful probe, so it is omitted when empty (matching the core's
    /// omitempty).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub resolvers: Vec<String>,
    pub message: String,
}

/// Result of the `leak_check` command. Mirrors the core's `LeakCheck` byte for
/// byte (see `docs/control-protocol.md`): the observed public IP and a verdict
/// on whether traffic is leaving through the tunnel exit, plus a best-effort DNS
/// assessment that is honest about its limits.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct LeakCheck {
    /// Public IP observed from the current vantage point; absent if every echo
    /// endpoint failed (then `ip_verdict` is `Error`).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub public_ip: Option<String>,
    /// Best-effort ISO 3166-1 alpha-2 country for `public_ip`, when an endpoint
    /// volunteered it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub country: Option<String>,
    /// The echo endpoint that answered, for transparency.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source: Option<String>,

    /// Whether a tunnel was active at check time. Decides how `public_ip` is
    /// judged: against the exit when connected, neutrally when not.
    pub connected: bool,
    /// The active node's configured exit address, present only when connected.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exit_server: Option<String>,
    /// Verdict on whether the observed IP corresponds to the tunnel exit; absent
    /// when not connected.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exit_match: Option<ExitMatch>,
    /// Overall severity of the IP finding for the UI to style.
    pub ip_verdict: Verdict,
    /// Short human summary of the IP finding.
    pub ip_message: String,

    /// Best-effort DNS leak assessment.
    pub dns: DnsResult,
}

/// Push events back to the UI. Implemented by the Tauri `AppHandle` wrapper in
/// `lib.rs`; the backend stays unaware of Tauri itself.
pub trait EventSink: Send + Sync + 'static {
    fn state(&self, state: &State);
    fn traffic(&self, up: u64, down: u64, up_rate: u64, down_rate: u64);
    fn log(&self, level: &str, msg: &str);
    /// Signal that the stored profile set changed (e.g. a background
    /// subscription refresh updated usage or node lists). Carries no payload;
    /// the UI re-fetches the profile list in response.
    fn profiles(&self);
}

/// Everything the control protocol exposes. Methods return a plain `Result`
/// with a human-readable error; the command layer turns `Err` into the
/// protocol's `{ ok: false, error }` response.
pub trait Backend: Send + Sync + 'static {
    fn status(&self) -> Result<State, String>;
    fn list_profiles(&self) -> Result<Vec<Profile>, String>;
    fn import_subscription(&self, url: String, name: String) -> Result<Profile, String>;
    fn import_link(&self, link: String, name: Option<String>) -> Result<Profile, String>;
    /// Import several share links at once into a single multi-node profile.
    /// `links` may hold pasted multi-line blocks or individual links; the core
    /// splits, de-duplicates, skips comments/blanks, and counts unparseable lines
    /// in the returned `skipped` rather than failing. An empty result (no valid
    /// links) is an error.
    fn import_links(
        &self,
        links: Vec<String>,
        name: Option<String>,
    ) -> Result<ImportLinksResult, String>;
    fn remove_profile(&self, profile: String) -> Result<(), String>;
    fn refresh_subscription(&self, profile: String) -> Result<Profile, String>;
    /// Start a connection. With an explicit `node` the core uses exactly that
    /// exit. Without one, `auto` chooses the candidate ordering the core walks:
    /// `false` keeps the protocol-fallback order, `true` selects the fastest node
    /// by measured ping. `auto` is ignored when `node` is set.
    fn connect(&self, profile: String, node: Option<String>, auto: bool) -> Result<State, String>;
    fn disconnect(&self) -> Result<State, String>;
    fn ping(&self, profile: String) -> Result<Vec<PingResult>, String>;
    fn set_routing(&self, mode: RoutingMode) -> Result<State, String>;
    fn set_split(&self, mode: SplitMode, apps: Vec<String>) -> Result<State, String>;
    /// Arm or disarm the kill switch. The core persists the choice and, when a
    /// tunnel is live, re-applies it in place by hot-swapping sing-box on the
    /// same node (a brief connecting→connected dip, not a full reconnect).
    fn set_kill_switch(&self, on: bool) -> Result<State, String>;
    /// Switch the tun network stack. Same live re-apply semantics as the kill
    /// switch; when idle the choice simply applies on the next connect.
    fn set_tun(&self, stack: TunStack) -> Result<State, String>;
    /// Arm or disarm connect-on-start. The core persists the choice and, when
    /// armed, reconnects the last profile the next time the daemon itself
    /// starts (service mode: at boot); nothing about a live tunnel changes.
    fn set_autoconnect(&self, on: bool) -> Result<State, String>;
    /// Record the DNS preferences: the ad/tracker-block toggle, the IPv4-only
    /// toggle, plus the two custom resolvers (the encrypted proxied resolver and
    /// the direct one). The core persists them and, when a tunnel is live,
    /// re-applies them in place by hot-swapping sing-box on the same node (a brief
    /// connecting→connected dip, not a full reconnect). An empty resolver resets
    /// that one to the core's default; a malformed one is refused.
    fn set_dns(
        &self,
        ad_block: bool,
        dns_remote: String,
        dns_direct: String,
        ipv4_only: bool,
    ) -> Result<State, String>;
    /// Record the custom domain-suffix routing rules and the RU direct-rule
    /// presets: `rules_direct` pins destinations to the direct outbound,
    /// `rules_proxy` to the proxy, and the presets add bundled banking /
    /// government direct rules. The core validates each suffix (a malformed one is
    /// refused), persists them, and — when a tunnel is live — re-applies them in
    /// place by hot-swapping sing-box on the same node (a brief
    /// connecting→connected dip, not a full reconnect).
    fn set_rules(
        &self,
        rules_direct: Vec<String>,
        rules_proxy: Vec<String>,
        preset_ru_banking: bool,
        preset_ru_gov: bool,
    ) -> Result<State, String>;
    fn leak_check(&self) -> Result<LeakCheck, String>;
}

#[cfg(test)]
mod tests {
    //! These lock the wire format the front-end types in `src/api/types.ts`
    //! depend on: the lowercase enum tokens, the camelCase / explicit field
    //! renames, and which optional fields drop out when absent. A change here
    //! that breaks the UI shows up as a failing round-trip rather than a silent
    //! protocol drift.

    use super::*;
    use serde_json::{from_value, json, to_value, Value};

    /// Assert an enum value serializes to exactly `token` and parses back.
    fn assert_token<T>(value: T, token: &str)
    where
        T: Serialize + for<'de> Deserialize<'de> + PartialEq + std::fmt::Debug + Copy,
    {
        assert_eq!(to_value(value).unwrap(), json!(token));
        let back: T = from_value(json!(token)).unwrap();
        assert_eq!(back, value);
    }

    #[test]
    fn connection_state_tokens() {
        assert_token(ConnectionState::Idle, "idle");
        assert_token(ConnectionState::Connecting, "connecting");
        assert_token(ConnectionState::Connected, "connected");
        assert_token(ConnectionState::Error, "error");
    }

    #[test]
    fn routing_mode_tokens() {
        assert_token(RoutingMode::Smart, "smart");
        assert_token(RoutingMode::Global, "global");
        assert_token(RoutingMode::Direct, "direct");
    }

    #[test]
    fn split_mode_tokens() {
        assert_token(SplitMode::Off, "off");
        assert_token(SplitMode::Exclude, "exclude");
        assert_token(SplitMode::Include, "include");
    }

    #[test]
    fn tun_stack_tokens() {
        assert_token(TunStack::System, "system");
        assert_token(TunStack::Gvisor, "gvisor");
        assert_token(TunStack::Mixed, "mixed");
    }

    #[test]
    fn protocol_tokens() {
        assert_token(Protocol::Vless, "vless");
        assert_token(Protocol::Hysteria2, "hysteria2");
        assert_token(Protocol::Amneziawg, "amneziawg");
        assert_token(Protocol::Shadowsocks, "shadowsocks");
        assert_token(Protocol::Trojan, "trojan");
        assert_token(Protocol::Vmess, "vmess");
    }

    #[test]
    fn source_tokens() {
        assert_token(Source::Subscription, "subscription");
        assert_token(Source::Manual, "manual");
    }

    #[test]
    fn exit_match_tokens() {
        assert_token(ExitMatch::Match, "match");
        assert_token(ExitMatch::Mismatch, "mismatch");
        assert_token(ExitMatch::Unknown, "unknown");
    }

    #[test]
    fn verdict_tokens() {
        assert_token(Verdict::Ok, "ok");
        assert_token(Verdict::Warn, "warn");
        assert_token(Verdict::Neutral, "neutral");
        assert_token(Verdict::Error, "error");
    }

    #[test]
    fn dns_status_tokens() {
        assert_token(DnsStatus::Ok, "ok");
        assert_token(DnsStatus::Leak, "leak");
        assert_token(DnsStatus::Inconclusive, "inconclusive");
        assert_token(DnsStatus::Unavailable, "unavailable");
    }

    #[test]
    fn full_state_round_trips() {
        let state = State {
            state: ConnectionState::Connected,
            node: Some("demo-nl".into()),
            profile: Some("demo-sub".into()),
            routing: Some(RoutingMode::Smart),
            split: Some(SplitMode::Exclude),
            split_apps: Some(vec!["chrome.exe".into(), "steam.exe".into()]),
            kill_switch: Some(true),
            tun_stack: Some(TunStack::Gvisor),
            autoconnect: Some(true),
            ad_block: Some(true),
            dns_remote: Some("tls://1.1.1.1".into()),
            dns_direct: Some("https://77.88.8.8/dns-query".into()),
            ipv4_only: Some(true),
            rules_direct: Some(vec!["sberbank.ru".into(), "bank.example".into()]),
            rules_proxy: Some(vec!["work.example".into()]),
            preset_ru_banking: Some(true),
            preset_ru_gov: Some(true),
            error: None,
        };
        let json = to_value(&state).unwrap();
        let back: State = from_value(json).unwrap();
        assert_eq!(back, state);
    }

    #[test]
    fn idle_state_omits_absent_fields() {
        let state = State {
            state: ConnectionState::Idle,
            node: None,
            profile: None,
            routing: None,
            split: None,
            split_apps: None,
            kill_switch: None,
            tun_stack: None,
            autoconnect: None,
            ad_block: None,
            dns_remote: None,
            dns_direct: None,
            ipv4_only: None,
            rules_direct: None,
            rules_proxy: None,
            preset_ru_banking: None,
            preset_ru_gov: None,
            error: None,
        };
        let obj = to_value(&state).unwrap();
        let map = obj.as_object().unwrap();
        // Only the always-present discriminant survives.
        assert_eq!(map.get("state"), Some(&json!("idle")));
        for absent in [
            "node",
            "profile",
            "routing",
            "split",
            "split_apps",
            "kill_switch",
            "tun_stack",
            "autoconnect",
            "ad_block",
            "dns_remote",
            "dns_direct",
            "ipv4_only",
            "rules_direct",
            "rules_proxy",
            "preset_ru_banking",
            "preset_ru_gov",
            "error",
        ] {
            assert!(
                !map.contains_key(absent),
                "expected {absent} to be omitted, got {obj}"
            );
        }
        // And a bare state deserializes to all-None options.
        let back: State = from_value(json!({ "state": "idle" })).unwrap();
        assert_eq!(back, state);
    }

    fn sample_node() -> Node {
        Node {
            id: "demo-nl".into(),
            name: "Amsterdam".into(),
            protocol: Protocol::Vless,
            server: "198.51.100.10".into(),
            port: 443,
            insecure: false,
        }
    }

    #[test]
    fn profile_renames_camel_case_keys() {
        let profile = Profile {
            id: "demo-sub".into(),
            name: "Demo".into(),
            source: Source::Subscription,
            url: Some("https://example.invalid/sub".into()),
            nodes: vec![sample_node()],
            updated_at: "2026-01-01T00:00:00Z".into(),
            expires_at: Some("2026-02-01T00:00:00Z".into()),
            traffic_used: Some(7),
            traffic_total: Some(100),
        };
        let obj = to_value(&profile).unwrap();
        let map = obj.as_object().unwrap();
        // snake_case fields are renamed on the wire; plain fields keep their name.
        assert!(map.contains_key("updatedAt"));
        assert!(map.contains_key("expiresAt"));
        assert!(map.contains_key("trafficUsed"));
        assert!(map.contains_key("trafficTotal"));
        for plain in ["id", "name", "source", "url", "nodes"] {
            assert!(map.contains_key(plain), "missing {plain} in {obj}");
        }
        // The snake_case spellings must NOT leak through.
        for snake in ["updated_at", "expires_at", "traffic_used", "traffic_total"] {
            assert!(!map.contains_key(snake), "{snake} leaked into {obj}");
        }
        let back: Profile = from_value(obj).unwrap();
        assert_eq!(back, profile);
    }

    #[test]
    fn manual_profile_omits_absent_optionals() {
        let profile = Profile {
            id: "demo-manual".into(),
            name: "Manual".into(),
            source: Source::Manual,
            url: None,
            nodes: vec![sample_node()],
            updated_at: "2026-01-01T00:00:00Z".into(),
            expires_at: None,
            traffic_used: None,
            traffic_total: None,
        };
        let obj = to_value(&profile).unwrap();
        let map = obj.as_object().unwrap();
        for absent in ["url", "expiresAt", "trafficUsed", "trafficTotal"] {
            assert!(
                !map.contains_key(absent),
                "expected {absent} omitted in {obj}"
            );
        }
        let back: Profile = from_value(obj).unwrap();
        assert_eq!(back, profile);
    }

    #[test]
    fn ping_result_uses_camel_case() {
        let result = PingResult {
            node: "demo-nl".into(),
            rtt_ms: 42,
            ok: true,
        };
        let obj = to_value(&result).unwrap();
        let map = obj.as_object().unwrap();
        assert_eq!(map.get("node"), Some(&json!("demo-nl")));
        assert_eq!(map.get("rttMs"), Some(&json!(42)));
        assert_eq!(map.get("ok"), Some(&json!(true)));
        assert!(!map.contains_key("rtt_ms"), "rtt_ms leaked: {obj}");
        let back: PingResult = from_value(obj).unwrap();
        assert_eq!(back, result);
    }

    #[test]
    fn ping_result_rtt_ms_is_i64_wire_compatible() {
        // The core emits RTTMs as int64. A negative sentinel and a value past
        // u32::MAX must both decode — modelling rtt_ms as u32 would reject these
        // and fail the entire ping response.
        for rtt in [-1_i64, 0, 42, i64::from(u32::MAX) + 1, i64::MAX] {
            let value = json!({ "node": "n", "rttMs": rtt, "ok": false });
            let parsed: PingResult =
                from_value(value).unwrap_or_else(|e| panic!("rttMs {rtt} should decode: {e}"));
            assert_eq!(parsed.rtt_ms, rtt);
            // And it round-trips back to the same wire token.
            assert_eq!(to_value(parsed).unwrap()["rttMs"], json!(rtt));
        }
    }

    #[test]
    fn connected_leak_check_round_trips() {
        let leak = LeakCheck {
            public_ip: Some("198.51.100.10".into()),
            country: Some("NL".into()),
            source: Some("ipify".into()),
            connected: true,
            exit_server: Some("198.51.100.10".into()),
            exit_match: Some(ExitMatch::Match),
            ip_verdict: Verdict::Ok,
            ip_message: "Public IP matches the tunnel exit.".into(),
            dns: DnsResult {
                status: DnsStatus::Inconclusive,
                resolvers: vec!["198.51.100.53".into()],
                message: "Observed resolver shown.".into(),
            },
        };
        let back: LeakCheck = from_value(to_value(&leak).unwrap()).unwrap();
        assert_eq!(back, leak);
    }

    #[test]
    fn idle_leak_check_omits_exit_fields_but_keeps_required() {
        let leak = LeakCheck {
            public_ip: None,
            country: None,
            source: None,
            connected: false,
            exit_server: None,
            exit_match: None,
            ip_verdict: Verdict::Neutral,
            ip_message: "Not connected.".into(),
            dns: DnsResult {
                status: DnsStatus::Inconclusive,
                resolvers: vec![],
                message: "Inconclusive.".into(),
            },
        };
        let obj = to_value(&leak).unwrap();
        let map = obj.as_object().unwrap();
        for absent in [
            "public_ip",
            "country",
            "source",
            "exit_server",
            "exit_match",
        ] {
            assert!(
                !map.contains_key(absent),
                "expected {absent} omitted in {obj}"
            );
        }
        // These carry meaning even when empty and must always be present.
        for required in ["connected", "ip_verdict", "ip_message", "dns"] {
            assert!(map.contains_key(required), "missing {required} in {obj}");
        }
        let back: LeakCheck = from_value(obj).unwrap();
        assert_eq!(back, leak);
    }

    #[test]
    fn dns_result_resolvers_omitted_only_when_empty() {
        let empty = DnsResult {
            status: DnsStatus::Unavailable,
            resolvers: vec![],
            message: "n/a".into(),
        };
        let empty_obj = to_value(&empty).unwrap();
        assert!(
            !empty_obj.as_object().unwrap().contains_key("resolvers"),
            "empty resolvers should be omitted: {empty_obj}"
        );

        let filled = DnsResult {
            status: DnsStatus::Ok,
            resolvers: vec!["198.51.100.53".into()],
            message: "ok".into(),
        };
        let filled_obj = to_value(&filled).unwrap();
        assert_eq!(
            filled_obj.as_object().unwrap().get("resolvers"),
            Some(&json!(["198.51.100.53"]))
        );

        // A payload without the key deserializes to an empty Vec (serde default).
        let parsed: DnsResult = from_value(json!({ "status": "ok", "message": "ok" })).unwrap();
        assert_eq!(parsed.resolvers, Vec::<String>::new());
    }

    #[test]
    fn unknown_node_fields_are_ignored_on_deserialize() {
        // The core may carry node fields the UI doesn't model; they must not
        // break decoding (mirrors the sidecar's "extra fields ignored" contract).
        let value: Value = json!({
            "id": "n1",
            "name": "Node",
            "protocol": "vless",
            "server": "198.51.100.10",
            "port": 443,
            "extra": "ignored",
        });
        let node: Node = from_value(value).unwrap();
        assert_eq!(node.id, "n1");
        assert_eq!(node.protocol, Protocol::Vless);
        // A node without the insecure key defaults to verification-on.
        assert!(!node.insecure);
    }

    #[test]
    fn insecure_node_flag_round_trips() {
        // The core marks skip-cert-verify nodes with insecure:true; it must
        // survive deserialize -> re-serialize so the webview sees it.
        let node: Node = from_value(json!({
            "id": "n2",
            "name": "Sketchy",
            "protocol": "hysteria2",
            "server": "198.51.100.11",
            "port": 8443,
            "insecure": true,
        }))
        .unwrap();
        assert!(node.insecure);
        assert_eq!(to_value(&node).unwrap()["insecure"], json!(true));
    }
}
