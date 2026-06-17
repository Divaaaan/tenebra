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

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct LeakCheck {
    pub ip: String,
    pub country: String,
    pub tunneled: bool,
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
    fn leak_check(&self) -> Result<LeakCheck, String>;
}
