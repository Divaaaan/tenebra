//! The boundary the UI talks to.
//!
//! Today this is served by [`mock::MockBackend`], an in-process fake. The real
//! build will replace it with a client that speaks line-delimited JSON to the
//! core sidecar over a local socket (see `docs/control-protocol.md`). Both
//! implement the same [`Backend`] trait, so the Tauri command layer in `lib.rs`
//! never has to know which one is wired in.
//!
//! The structs below mirror the protocol's `State`, `Node`, `Profile` and
//! `PingResult` shapes. They serialize to exactly the JSON the front-end types
//! in `src/api/types.ts` expect.

pub mod mock;
pub mod sidecar;

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

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PingResult {
    /// The node id this result is for.
    pub node: String,
    pub rtt_ms: u32,
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
    fn remove_profile(&self, profile: String) -> Result<(), String>;
    fn refresh_subscription(&self, profile: String) -> Result<Profile, String>;
    fn connect(&self, profile: String, node: Option<String>) -> Result<State, String>;
    fn disconnect(&self) -> Result<State, String>;
    fn ping(&self, profile: String) -> Result<Vec<PingResult>, String>;
    fn set_routing(&self, mode: RoutingMode) -> Result<State, String>;
    fn set_split(&self, mode: SplitMode, apps: Vec<String>) -> Result<State, String>;
    fn leak_check(&self) -> Result<LeakCheck, String>;
}
