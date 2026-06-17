//! In-process fake of the core. It holds a little state, invents two clearly
//! demo profiles, and drives a connecting → connected transition on a timer
//! while dribbling out fake traffic. Nothing here touches the network or a real
//! tunnel; it exists so the UI can be built and demoed before the sidecar lands.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

use super::{
    Backend, ConnectionState, EventSink, LeakCheck, Node, PingResult, Profile, Protocol,
    RoutingMode, Source, State,
};

/// How long the fake "dial" takes before flipping to connected.
const CONNECT_DELAY: Duration = Duration::from_millis(1500);
/// Cadence of the fake traffic ticker.
const TRAFFIC_TICK: Duration = Duration::from_millis(1000);

struct Inner {
    state: State,
    profiles: Vec<Profile>,
    /// Bumped on every connect/disconnect so a stale timer or ticker from a
    /// previous attempt can tell it has been superseded and bail out.
    generation: u64,
    up_total: u64,
    down_total: u64,
}

/// State shared between the backend façade and its background threads. Holding
/// this in an `Arc` lets the timers outlive any single method call without the
/// trait having to traffic in `Arc<Self>`.
struct Shared {
    inner: Mutex<Inner>,
    sink: Arc<dyn EventSink>,
}

impl Shared {
    fn emit_state(&self, state: &State) {
        self.sink.state(state);
    }

    fn emit_profiles(&self) {
        self.sink.profiles();
    }
}

pub struct MockBackend {
    shared: Arc<Shared>,
}

impl MockBackend {
    pub fn new(sink: Arc<dyn EventSink>) -> Self {
        let inner = Inner {
            state: State {
                state: ConnectionState::Idle,
                node: None,
                profile: None,
                routing: Some(RoutingMode::Smart),
                error: None,
            },
            profiles: demo_profiles(),
            generation: 0,
            up_total: 0,
            down_total: 0,
        };
        Self {
            shared: Arc::new(Shared {
                inner: Mutex::new(inner),
                sink,
            }),
        }
    }
}

/// Spawn the timer that completes a connection and then streams traffic for as
/// long as this generation stays current.
fn spawn_connect(shared: &Arc<Shared>, generation: u64, node_name: String) {
    let shared = Arc::clone(shared);
    thread::spawn(move || {
        thread::sleep(CONNECT_DELAY);

        {
            let mut inner = shared.inner.lock().unwrap();
            if inner.generation != generation {
                return; // a newer connect/disconnect took over
            }
            inner.state.state = ConnectionState::Connected;
            inner.state.error = None;
            inner.up_total = 0;
            inner.down_total = 0;
            let snapshot = inner.state.clone();
            drop(inner);
            shared.emit_state(&snapshot);
        }
        shared
            .sink
            .log("info", &format!("tunnel up via {node_name}"));

        run_traffic(&shared, generation);
    });
}

/// Emit a believable-looking traffic sample once a second until the connection
/// ends or is replaced.
fn run_traffic(shared: &Arc<Shared>, generation: u64) {
    let shared = Arc::clone(shared);
    thread::spawn(move || {
        // A tiny LCG keeps the rates lively without pulling in an rng crate.
        let mut seed: u64 = 0x9e37_79b9_7f4a_7c15 ^ generation.wrapping_mul(2_654_435_761);
        loop {
            thread::sleep(TRAFFIC_TICK);

            let mut inner = shared.inner.lock().unwrap();
            let live =
                inner.generation == generation && inner.state.state == ConnectionState::Connected;
            if !live {
                return;
            }

            seed = next_rand(seed);
            let down_rate = 40_000 + (seed >> 33) % 220_000;
            seed = next_rand(seed);
            let up_rate = 8_000 + (seed >> 33) % 60_000;

            inner.down_total += down_rate;
            inner.up_total += up_rate;
            let (up, down) = (inner.up_total, inner.down_total);
            drop(inner);

            shared.sink.traffic(up, down, up_rate, down_rate);
        }
    });
}

impl Backend for MockBackend {
    fn status(&self) -> Result<State, String> {
        Ok(self.shared.inner.lock().unwrap().state.clone())
    }

    fn list_profiles(&self) -> Result<Vec<Profile>, String> {
        Ok(self.shared.inner.lock().unwrap().profiles.clone())
    }

    fn import_subscription(&self, url: String, name: String) -> Result<Profile, String> {
        let name = name.trim();
        if name.is_empty() {
            return Err("name is required".into());
        }
        if url.trim().is_empty() {
            return Err("subscription url is required".into());
        }
        let profile = Profile {
            id: new_id("sub"),
            name: name.to_string(),
            source: Source::Subscription,
            url: Some(url),
            nodes: synth_nodes(4),
            updated_at: now_rfc3339(),
            expires_at: Some(in_days(30)),
            traffic_used: Some(7 * GIB),
            traffic_total: Some(100 * GIB),
        };
        self.shared
            .inner
            .lock()
            .unwrap()
            .profiles
            .push(profile.clone());
        self.shared
            .sink
            .log("info", &format!("imported subscription \"{name}\""));
        self.shared.emit_profiles();
        Ok(profile)
    }

    fn import_link(&self, link: String, name: Option<String>) -> Result<Profile, String> {
        if link.trim().is_empty() {
            return Err("link is required".into());
        }
        let protocol = protocol_from_link(&link);
        let name = name
            .map(|n| n.trim().to_string())
            .filter(|n| !n.is_empty())
            .unwrap_or_else(|| format!("{} server", protocol_label(protocol)));
        let profile = Profile {
            id: new_id("man"),
            name: name.clone(),
            source: Source::Manual,
            url: None,
            nodes: vec![Node {
                id: new_id("n"),
                name: name.clone(),
                protocol,
                server: "203.0.113.10".into(),
                port: 443,
            }],
            updated_at: now_rfc3339(),
            expires_at: None,
            traffic_used: None,
            traffic_total: None,
        };
        self.shared
            .inner
            .lock()
            .unwrap()
            .profiles
            .push(profile.clone());
        self.shared
            .sink
            .log("info", &format!("imported link \"{name}\""));
        self.shared.emit_profiles();
        Ok(profile)
    }

    fn remove_profile(&self, profile: String) -> Result<(), String> {
        let mut inner = self.shared.inner.lock().unwrap();
        let before = inner.profiles.len();
        inner.profiles.retain(|p| p.id != profile);
        if inner.profiles.len() == before {
            return Err("profile not found".into());
        }
        // Tear down the tunnel if the active profile just disappeared.
        if inner.state.profile.as_deref() == Some(profile.as_str()) {
            inner.generation += 1;
            inner.state.state = ConnectionState::Idle;
            inner.state.node = None;
            inner.state.profile = None;
            let snapshot = inner.state.clone();
            drop(inner);
            self.shared.emit_state(&snapshot);
        } else {
            drop(inner);
        }
        // The profile list changed; tell the UI to reload it.
        self.shared.emit_profiles();
        Ok(())
    }

    fn refresh_subscription(&self, profile: String) -> Result<Profile, String> {
        let mut inner = self.shared.inner.lock().unwrap();
        let p = inner
            .profiles
            .iter_mut()
            .find(|p| p.id == profile)
            .ok_or("profile not found")?;
        if p.source != Source::Subscription {
            return Err("only subscriptions can be refreshed".into());
        }
        p.updated_at = now_rfc3339();
        p.expires_at = Some(in_days(30));
        p.traffic_used = Some(p.traffic_used.unwrap_or(0) + GIB / 2);
        let updated = p.clone();
        drop(inner);
        self.shared.emit_profiles();
        Ok(updated)
    }

    fn connect(&self, profile: String, node: Option<String>) -> Result<State, String> {
        let mut inner = self.shared.inner.lock().unwrap();
        let p = inner
            .profiles
            .iter()
            .find(|p| p.id == profile)
            .ok_or("profile not found")?
            .clone();
        if p.nodes.is_empty() {
            return Err("profile has no nodes".into());
        }
        // Honour the requested node, else fall back to the first — the protocol
        // says the core picks the lowest-ping node, which for the mock is just
        // the head of the list.
        let chosen = match node {
            Some(id) if p.nodes.iter().any(|n| n.id == id) => id,
            Some(id) => return Err(format!("node {id} not in profile")),
            None => p.nodes[0].id.clone(),
        };
        let node_name = p
            .nodes
            .iter()
            .find(|n| n.id == chosen)
            .map(|n| n.name.clone())
            .unwrap_or_else(|| chosen.clone());

        inner.generation += 1;
        let generation = inner.generation;
        inner.state.state = ConnectionState::Connecting;
        inner.state.node = Some(chosen);
        inner.state.profile = Some(profile);
        inner.state.error = None;
        let snapshot = inner.state.clone();
        drop(inner);

        self.shared.emit_state(&snapshot);
        self.shared
            .sink
            .log("info", &format!("dialing {node_name}…"));
        spawn_connect(&self.shared, generation, node_name);
        Ok(snapshot)
    }

    fn disconnect(&self) -> Result<State, String> {
        let mut inner = self.shared.inner.lock().unwrap();
        inner.generation += 1; // invalidate any in-flight connect/ticker
        inner.state.state = ConnectionState::Idle;
        inner.state.node = None;
        inner.state.error = None;
        let snapshot = inner.state.clone();
        drop(inner);
        self.shared.emit_state(&snapshot);
        self.shared.sink.log("info", "tunnel down");
        Ok(snapshot)
    }

    fn ping(&self, profile: String) -> Result<Vec<PingResult>, String> {
        let inner = self.shared.inner.lock().unwrap();
        let p = inner
            .profiles
            .iter()
            .find(|p| p.id == profile)
            .ok_or("profile not found")?;
        let mut seed: u64 = 0x2545_f491_4f6c_dd1d ^ (p.nodes.len() as u64);
        let results = p
            .nodes
            .iter()
            .map(|n| {
                seed = next_rand(seed);
                let r = (seed >> 33) % 100;
                // ~1 in 12 nodes looks unreachable, the rest 20–260 ms.
                let ok = r % 12 != 0;
                PingResult {
                    node: n.id.clone(),
                    rtt_ms: if ok { 20 + (r as u32 * 24 % 240) } else { 0 },
                    ok,
                }
            })
            .collect();
        Ok(results)
    }

    fn set_routing(&self, mode: RoutingMode) -> Result<State, String> {
        let mut inner = self.shared.inner.lock().unwrap();
        inner.state.routing = Some(mode);
        let snapshot = inner.state.clone();
        drop(inner);
        self.shared.emit_state(&snapshot);
        Ok(snapshot)
    }

    fn leak_check(&self) -> Result<LeakCheck, String> {
        let tunneled = {
            let inner = self.shared.inner.lock().unwrap();
            inner.state.state == ConnectionState::Connected
        };
        // Obviously-fake documentation-range addresses (RFC 5737).
        Ok(if tunneled {
            LeakCheck {
                ip: "198.51.100.24".into(),
                country: "NL".into(),
                tunneled: true,
            }
        } else {
            LeakCheck {
                ip: "192.0.2.7".into(),
                country: "RU".into(),
                tunneled: false,
            }
        })
    }
}

// --- demo data and small helpers ----------------------------------------------

const GIB: u64 = 1024 * 1024 * 1024;

/// Tiny LCG step shared by the traffic and ping fakes.
fn next_rand(seed: u64) -> u64 {
    seed.wrapping_mul(6_364_136_223_846_793_005)
        .wrapping_add(1_442_695_040_888_963_407)
}

fn demo_profiles() -> Vec<Profile> {
    vec![
        Profile {
            id: "demo-sub".into(),
            name: "Demo subscription".into(),
            source: Source::Subscription,
            url: Some("https://example.invalid/sub".into()),
            nodes: vec![
                node_lit(
                    "demo-nl",
                    "Amsterdam · REALITY",
                    Protocol::Vless,
                    "198.51.100.10",
                    443,
                ),
                node_lit(
                    "demo-de",
                    "Frankfurt · Hysteria2",
                    Protocol::Hysteria2,
                    "198.51.100.20",
                    8443,
                ),
                node_lit(
                    "demo-fi",
                    "Helsinki · AmneziaWG",
                    Protocol::Amneziawg,
                    "198.51.100.30",
                    51820,
                ),
            ],
            updated_at: now_rfc3339(),
            expires_at: Some(in_days(21)),
            traffic_used: Some(12 * GIB),
            traffic_total: Some(200 * GIB),
        },
        Profile {
            id: "demo-manual".into(),
            name: "Demo manual node".into(),
            source: Source::Manual,
            url: None,
            nodes: vec![node_lit(
                "demo-shadow",
                "Demo · Shadowsocks",
                Protocol::Shadowsocks,
                "203.0.113.5",
                8388,
            )],
            updated_at: now_rfc3339(),
            expires_at: None,
            traffic_used: None,
            traffic_total: None,
        },
    ]
}

fn node_lit(id: &str, name: &str, protocol: Protocol, server: &str, port: u16) -> Node {
    Node {
        id: id.to_string(),
        name: name.to_string(),
        protocol,
        server: server.to_string(),
        port,
    }
}

fn synth_nodes(count: usize) -> Vec<Node> {
    let cities = [
        ("Amsterdam", Protocol::Vless, "198.51.100.40"),
        ("Frankfurt", Protocol::Hysteria2, "198.51.100.41"),
        ("Stockholm", Protocol::Trojan, "198.51.100.42"),
        ("Warsaw", Protocol::Vmess, "198.51.100.43"),
        ("Helsinki", Protocol::Amneziawg, "198.51.100.44"),
    ];
    (0..count)
        .map(|i| {
            let (city, proto, ip) = cities[i % cities.len()];
            node_lit(
                &new_id("n"),
                &format!("{city} · {}", protocol_label(proto)),
                proto,
                ip,
                443,
            )
        })
        .collect()
}

fn protocol_label(p: Protocol) -> &'static str {
    match p {
        Protocol::Vless => "VLESS",
        Protocol::Hysteria2 => "Hysteria2",
        Protocol::Amneziawg => "AmneziaWG",
        Protocol::Shadowsocks => "Shadowsocks",
        Protocol::Trojan => "Trojan",
        Protocol::Vmess => "VMess",
    }
}

fn protocol_from_link(link: &str) -> Protocol {
    let scheme = link.split("://").next().unwrap_or("").to_ascii_lowercase();
    match scheme.as_str() {
        "hysteria2" | "hy2" => Protocol::Hysteria2,
        "ss" => Protocol::Shadowsocks,
        "trojan" => Protocol::Trojan,
        "vmess" => Protocol::Vmess,
        "wireguard" | "amneziawg" => Protocol::Amneziawg,
        _ => Protocol::Vless,
    }
}

// Unique-enough id without pulling in uuid; good enough for a mock.
fn new_id(prefix: &str) -> String {
    static COUNTER: AtomicU64 = AtomicU64::new(1);
    let n = COUNTER.fetch_add(1, Ordering::Relaxed);
    let t = unix_millis();
    format!("{prefix}-{t:x}{n:x}")
}

fn now_rfc3339() -> String {
    rfc3339_from_unix(unix_secs())
}

fn in_days(days: u64) -> String {
    rfc3339_from_unix(unix_secs() + days * 86_400)
}

fn unix_secs() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

fn unix_millis() -> u128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0)
}

// Minimal UTC RFC3339 formatter (civil-from-days algorithm by Howard Hinnant).
// Avoids a chrono/time dependency for what the mock needs: a valid timestamp.
fn rfc3339_from_unix(secs: u64) -> String {
    let days = (secs / 86_400) as i64;
    let rem = secs % 86_400;
    let (hour, min, sec) = (rem / 3600, (rem % 3600) / 60, rem % 60);

    let z = days + 719_468;
    let era = z / 146_097;
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = if month <= 2 { y + 1 } else { y };

    format!("{year:04}-{month:02}-{day:02}T{hour:02}:{min:02}:{sec:02}Z")
}
