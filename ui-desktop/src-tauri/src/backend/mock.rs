//! In-process fake of the core. It holds a little state, invents two clearly
//! demo profiles, and drives a connecting → connected transition on a timer
//! while dribbling out fake traffic. Nothing here touches the network or a real
//! tunnel; it exists so the UI can be built and demoed before the sidecar lands.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

use super::{
    Backend, ConnectionState, DnsResult, DnsStatus, EventSink, ExitMatch, ImportLinksResult,
    LeakCheck, Node, PingResult, Profile, Protocol, RoutingMode, Source, SplitMode, State,
    TunStack, Verdict,
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
                split: None,
                split_apps: None,
                kill_switch: None,
                // The core always names the stack once normalized; mirror that.
                tun_stack: Some(TunStack::System),
                autoconnect: None,
                ad_block: None,
                // The core reports the effective resolvers once normalized; mirror
                // the defaults so the UI's custom-DNS inputs come up prefilled.
                dns_remote: Some("tls://1.1.1.1".into()),
                dns_direct: Some("https://77.88.8.8/dns-query".into()),
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

    fn import_links(
        &self,
        links: Vec<String>,
        name: Option<String>,
    ) -> Result<ImportLinksResult, String> {
        // Mirror the core's batch parsing well enough for the demo: split each
        // entry on newlines, trim, drop blanks/comments and duplicates, then count
        // a line as a server if it looks like a link (has a scheme) or skip it.
        // The mock can't truly parse a link, so "looks like a link" stands in for
        // the core's per-protocol parser.
        let (nodes, skipped) = parse_links_mock(&links);
        if nodes.is_empty() {
            return Err(format!("no valid links found ({skipped} skipped)"));
        }
        let imported = nodes.len() as u32;
        let name = name
            .map(|n| n.trim().to_string())
            .filter(|n| !n.is_empty())
            .unwrap_or_else(|| "Imported links".to_string());
        let profile = Profile {
            id: new_id("man"),
            name: name.clone(),
            source: Source::Manual,
            url: None,
            nodes,
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
        self.shared.sink.log(
            "info",
            &format!("imported {imported} link(s) into \"{name}\" ({skipped} skipped)"),
        );
        self.shared.emit_profiles();
        Ok(ImportLinksResult {
            profile,
            imported,
            skipped,
        })
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

    fn connect(&self, profile: String, node: Option<String>, auto: bool) -> Result<State, String> {
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
        // Honour an explicit node. Without one, `auto` decides: the fastest node
        // by the same synthetic ping the `ping` command reports (so the demo's
        // "auto" actually reflects the latencies the user sees), or just the head
        // of the list for the default protocol-fallback order. An explicit node
        // overrides `auto`, mirroring the core.
        let chosen = match node {
            Some(id) if p.nodes.iter().any(|n| n.id == id) => id,
            Some(id) => return Err(format!("node {id} not in profile")),
            None if auto => fastest_node(&p).unwrap_or_else(|| p.nodes[0].id.clone()),
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
        Ok(synth_ping(p))
    }

    fn set_routing(&self, mode: RoutingMode) -> Result<State, String> {
        let mut inner = self.shared.inner.lock().unwrap();
        inner.state.routing = Some(mode);
        let snapshot = inner.state.clone();
        drop(inner);
        self.shared.emit_state(&snapshot);
        Ok(snapshot)
    }

    fn set_split(&self, mode: SplitMode, apps: Vec<String>) -> Result<State, String> {
        // Mirror the core's normalization so the mock behaves like the real
        // thing: trim/lowercase/dedupe/sort, and collapse an empty list to off.
        let apps = normalize_split_apps(apps);
        let mode = if apps.is_empty() {
            SplitMode::Off
        } else {
            mode
        };

        let mut inner = self.shared.inner.lock().unwrap();
        if mode == SplitMode::Off {
            inner.state.split = None;
            inner.state.split_apps = None;
        } else {
            inner.state.split = Some(mode);
            inner.state.split_apps = Some(apps);
        }
        let snapshot = inner.state.clone();
        drop(inner);
        self.shared.emit_state(&snapshot);
        Ok(snapshot)
    }

    fn set_kill_switch(&self, on: bool) -> Result<State, String> {
        // Mirror the core: record and report; a live "tunnel" would hot-swap,
        // which the mock abbreviates to just the state change.
        let mut inner = self.shared.inner.lock().unwrap();
        inner.state.kill_switch = if on { Some(true) } else { None };
        let snapshot = inner.state.clone();
        drop(inner);
        self.shared.emit_state(&snapshot);
        self.shared.sink.log(
            "info",
            if on {
                "kill switch armed"
            } else {
                "kill switch disarmed"
            },
        );
        Ok(snapshot)
    }

    fn set_tun(&self, stack: TunStack) -> Result<State, String> {
        let mut inner = self.shared.inner.lock().unwrap();
        inner.state.tun_stack = Some(stack);
        let snapshot = inner.state.clone();
        drop(inner);
        self.shared.emit_state(&snapshot);
        Ok(snapshot)
    }

    fn set_autoconnect(&self, on: bool) -> Result<State, String> {
        // Mirror the core: record and report. The real daemon acts on this only
        // at its own next start, so there is nothing more for the mock to do.
        let mut inner = self.shared.inner.lock().unwrap();
        inner.state.autoconnect = if on { Some(true) } else { None };
        let snapshot = inner.state.clone();
        drop(inner);
        self.shared.emit_state(&snapshot);
        Ok(snapshot)
    }

    fn set_dns(
        &self,
        ad_block: bool,
        dns_remote: String,
        dns_direct: String,
    ) -> Result<State, String> {
        // Mirror the core: an empty resolver falls back to the default; a live
        // tunnel would hot-swap, which the mock abbreviates to the state change.
        let mut inner = self.shared.inner.lock().unwrap();
        inner.state.ad_block = if ad_block { Some(true) } else { None };
        inner.state.dns_remote = Some(if dns_remote.is_empty() {
            "tls://1.1.1.1".into()
        } else {
            dns_remote
        });
        inner.state.dns_direct = Some(if dns_direct.is_empty() {
            "https://77.88.8.8/dns-query".into()
        } else {
            dns_direct
        });
        let snapshot = inner.state.clone();
        drop(inner);
        self.shared.emit_state(&snapshot);
        Ok(snapshot)
    }

    fn leak_check(&self) -> Result<LeakCheck, String> {
        // Resolve the demo "exit": the configured server of the connected node,
        // so a connected check reads as a clean match against it. When idle there
        // is no exit and the result is neutral. Addresses are documentation-range
        // (RFC 5737), like the rest of the demo data.
        let exit = {
            let inner = self.shared.inner.lock().unwrap();
            if inner.state.state != ConnectionState::Connected {
                None
            } else {
                let node = inner.state.node.clone();
                inner
                    .profiles
                    .iter()
                    .flat_map(|p| &p.nodes)
                    .find(|n| Some(&n.id) == node.as_ref())
                    .map(|n| n.server.clone())
            }
        };

        // The mock mirrors the core's honesty: a DNS leak/no-leak verdict is out
        // of scope, so the demo reports it as inconclusive, never a pass.
        let dns = DnsResult {
            status: DnsStatus::Inconclusive,
            resolvers: vec!["198.51.100.53".into()],
            message: "Observed resolver shown. A reliable DNS leak verdict needs a full test, \
                      so this is reported as inconclusive rather than a pass."
                .into(),
        };

        Ok(match exit {
            Some(exit) => LeakCheck {
                public_ip: Some(exit.clone()),
                country: Some("NL".into()),
                source: Some("ipify".into()),
                connected: true,
                exit_server: Some(exit.clone()),
                exit_match: Some(ExitMatch::Match),
                ip_verdict: Verdict::Ok,
                ip_message: format!("Public IP {exit} matches the tunnel exit."),
                dns,
            },
            None => LeakCheck {
                public_ip: Some("192.0.2.7".into()),
                country: Some("RU".into()),
                source: Some("ipify".into()),
                connected: false,
                exit_server: None,
                exit_match: None,
                ip_verdict: Verdict::Neutral,
                ip_message: "Not connected. Current public IP is 192.0.2.7.".into(),
                dns,
            },
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

/// Deterministic synthetic ping for a profile's nodes: ~1 in 12 looks
/// unreachable, the rest land in 20–260 ms. Factored out so `ping` and the
/// auto-connect node pick (`fastest_node`) read the same latencies — otherwise
/// the demo could "auto-select fastest" a node the ping panel shows as slow.
fn synth_ping(p: &Profile) -> Vec<PingResult> {
    let mut seed: u64 = 0x2545_f491_4f6c_dd1d ^ (p.nodes.len() as u64);
    p.nodes
        .iter()
        .map(|n| {
            seed = next_rand(seed);
            let r = (seed >> 33) % 100;
            let ok = r % 12 != 0;
            PingResult {
                node: n.id.clone(),
                rtt_ms: if ok { 20 + (r as i64 * 24 % 240) } else { 0 },
                ok,
            }
        })
        .collect()
}

/// The id of the reachable node with the lowest synthetic ping, or `None` when
/// none answered. Mirrors the core's auto strategy for the mock: rank by RTT,
/// skip the unreachable. Ties resolve to the earlier node (first wins).
fn fastest_node(p: &Profile) -> Option<String> {
    synth_ping(p)
        .into_iter()
        .filter(|r| r.ok)
        .min_by_key(|r| r.rtt_ms)
        .map(|r| r.node)
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

/// Trim, lowercase, de-duplicate and sort executable names, matching the core's
/// split-app normalization so the mock and the real backend agree.
fn normalize_split_apps(apps: Vec<String>) -> Vec<String> {
    let mut out: Vec<String> = apps
        .into_iter()
        .map(|a| a.trim().to_ascii_lowercase())
        .filter(|a| !a.is_empty())
        .collect();
    out.sort();
    out.dedup();
    out
}

/// A line "looks like" a share link if it has a `scheme://` prefix. The mock
/// uses this as a stand-in for the core's real parser when counting batch
/// imports, so a junk line is skipped rather than silently turned into a node.
fn looks_like_link(line: &str) -> bool {
    match line.split_once("://") {
        Some((scheme, rest)) => !scheme.is_empty() && !rest.is_empty(),
        None => false,
    }
}

/// Demo-grade batch link parser mirroring the core's `ParseLinks` shape: split on
/// newlines, trim, drop blanks / `#` / `//` comments and exact duplicates, build a
/// node per link-shaped line and count the rest as skipped. Returns
/// `(nodes, skipped)`.
fn parse_links_mock(links: &[String]) -> (Vec<Node>, u32) {
    let mut nodes = Vec::new();
    let mut skipped = 0u32;
    let mut seen = std::collections::HashSet::new();
    for raw in links {
        for line in raw.split('\n') {
            let line = line.trim();
            if line.is_empty() || line.starts_with('#') || line.starts_with("//") {
                continue;
            }
            if !seen.insert(line.to_string()) {
                continue; // exact duplicate
            }
            if looks_like_link(line) {
                let protocol = protocol_from_link(line);
                nodes.push(node_lit(
                    &new_id("n"),
                    &format!("{} server", protocol_label(protocol)),
                    protocol,
                    "203.0.113.10",
                    443,
                ));
            } else {
                skipped += 1;
            }
        }
    }
    (nodes, skipped)
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

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::AtomicUsize;

    /// A recording sink so behavior tests can assert what the backend emitted
    /// without a Tauri app. Counts the signal-only events and keeps the payloads
    /// the assertions actually inspect.
    #[derive(Default)]
    struct Rec {
        states: Mutex<Vec<State>>,
        logs: Mutex<Vec<(String, String)>>,
        traffic: AtomicUsize,
        profiles: AtomicUsize,
    }

    impl Rec {
        fn last_state(&self) -> Option<State> {
            self.states.lock().unwrap().last().cloned()
        }
        fn profiles_count(&self) -> usize {
            self.profiles.load(Ordering::SeqCst)
        }
        fn state_count(&self) -> usize {
            self.states.lock().unwrap().len()
        }
    }

    impl EventSink for Rec {
        fn state(&self, state: &State) {
            self.states.lock().unwrap().push(state.clone());
        }
        fn traffic(&self, _up: u64, _down: u64, _up_rate: u64, _down_rate: u64) {
            self.traffic.fetch_add(1, Ordering::SeqCst);
        }
        fn log(&self, level: &str, msg: &str) {
            self.logs
                .lock()
                .unwrap()
                .push((level.to_string(), msg.to_string()));
        }
        fn profiles(&self) {
            self.profiles.fetch_add(1, Ordering::SeqCst);
        }
    }

    /// A backend plus the sink it reports to, so tests can drive one and inspect
    /// the other.
    fn backend() -> (MockBackend, Arc<Rec>) {
        let sink = Arc::new(Rec::default());
        let backend = MockBackend::new(sink.clone());
        (backend, sink)
    }

    // --- pure helpers ----------------------------------------------------------

    #[test]
    fn normalize_split_apps_trims_lowercases_dedupes_sorts() {
        let out = normalize_split_apps(vec![
            "  Chrome.exe ".into(),
            "STEAM.exe".into(),
            "chrome.exe".into(),
            "".into(),
            "   ".into(),
        ]);
        assert_eq!(out, vec!["chrome.exe", "steam.exe"]);
    }

    #[test]
    fn normalize_split_apps_empty_is_empty() {
        assert!(normalize_split_apps(vec![]).is_empty());
        assert!(normalize_split_apps(vec!["  ".into(), "".into()]).is_empty());
    }

    #[test]
    fn protocol_from_link_maps_known_schemes() {
        assert_eq!(protocol_from_link("vless://x"), Protocol::Vless);
        assert_eq!(protocol_from_link("hysteria2://x"), Protocol::Hysteria2);
        assert_eq!(protocol_from_link("hy2://x"), Protocol::Hysteria2);
        assert_eq!(protocol_from_link("ss://x"), Protocol::Shadowsocks);
        assert_eq!(protocol_from_link("trojan://x"), Protocol::Trojan);
        assert_eq!(protocol_from_link("vmess://x"), Protocol::Vmess);
        assert_eq!(protocol_from_link("wireguard://x"), Protocol::Amneziawg);
        assert_eq!(protocol_from_link("amneziawg://x"), Protocol::Amneziawg);
    }

    #[test]
    fn protocol_from_link_is_case_insensitive_and_defaults_to_vless() {
        assert_eq!(protocol_from_link("VLESS://x"), Protocol::Vless);
        assert_eq!(protocol_from_link("Hysteria2://x"), Protocol::Hysteria2);
        // Anything unrecognized (including a bare http URL) falls back to vless.
        assert_eq!(protocol_from_link("http://x"), Protocol::Vless);
        assert_eq!(protocol_from_link("garbage"), Protocol::Vless);
    }

    #[test]
    fn rfc3339_from_unix_anchors() {
        assert_eq!(rfc3339_from_unix(0), "1970-01-01T00:00:00Z");
        // A verified non-zero anchor pins the civil-from-days math.
        assert_eq!(rfc3339_from_unix(1_700_000_000), "2023-11-14T22:13:20Z");
        // And the shape holds for an arbitrary value.
        let s = rfc3339_from_unix(1_500_000_123);
        assert!(regex_like_rfc3339(&s), "unexpected rfc3339 shape: {s}");
    }

    /// Cheap structural check (no regex crate): `YYYY-MM-DDTHH:MM:SSZ`.
    fn regex_like_rfc3339(s: &str) -> bool {
        let b = s.as_bytes();
        s.len() == 20
            && b[4] == b'-'
            && b[7] == b'-'
            && b[10] == b'T'
            && b[13] == b':'
            && b[16] == b':'
            && b[19] == b'Z'
            && s.chars()
                .enumerate()
                .all(|(i, c)| matches!(i, 4 | 7 | 10 | 13 | 16 | 19) || c.is_ascii_digit())
    }

    // --- backend behavior ------------------------------------------------------

    #[test]
    fn status_starts_idle_with_smart_routing() {
        let (b, _) = backend();
        let s = b.status().unwrap();
        assert_eq!(s.state, ConnectionState::Idle);
        assert_eq!(s.routing, Some(RoutingMode::Smart));
        assert_eq!(s.node, None);
        assert_eq!(s.split, None);
    }

    #[test]
    fn list_profiles_returns_the_two_demos() {
        let (b, _) = backend();
        let profiles = b.list_profiles().unwrap();
        assert_eq!(profiles.len(), 2);

        let sub = &profiles[0];
        assert_eq!(sub.id, "demo-sub");
        assert_eq!(sub.source, Source::Subscription);
        assert_eq!(sub.nodes.len(), 3);

        let manual = &profiles[1];
        assert_eq!(manual.id, "demo-manual");
        assert_eq!(manual.source, Source::Manual);
        assert_eq!(manual.nodes.len(), 1);
    }

    #[test]
    fn set_routing_updates_state_and_emits() {
        let (b, sink) = backend();
        let s = b.set_routing(RoutingMode::Global).unwrap();
        assert_eq!(s.routing, Some(RoutingMode::Global));
        assert_eq!(
            sink.last_state().unwrap().routing,
            Some(RoutingMode::Global)
        );
    }

    #[test]
    fn set_split_normalizes_apps() {
        let (b, _) = backend();
        let s = b
            .set_split(
                SplitMode::Exclude,
                vec!["B.exe".into(), "a.exe".into(), "a.exe".into()],
            )
            .unwrap();
        assert_eq!(s.split, Some(SplitMode::Exclude));
        assert_eq!(s.split_apps, Some(vec!["a.exe".into(), "b.exe".into()]));
    }

    #[test]
    fn set_split_empty_list_collapses_to_off() {
        let (b, _) = backend();
        // Exclude with an all-whitespace list normalizes to empty, so the mode is
        // forced off and both split fields clear.
        let s = b
            .set_split(SplitMode::Exclude, vec!["   ".into(), "".into()])
            .unwrap();
        assert_eq!(s.split, None);
        assert_eq!(s.split_apps, None);
    }

    #[test]
    fn set_split_off_clears_regardless_of_apps() {
        let (b, _) = backend();
        let s = b.set_split(SplitMode::Off, vec!["x.exe".into()]).unwrap();
        assert_eq!(s.split, None);
        assert_eq!(s.split_apps, None);
    }

    #[test]
    fn set_kill_switch_arms_and_disarms() {
        let (b, sink) = backend();
        let s = b.set_kill_switch(true).unwrap();
        assert_eq!(s.kill_switch, Some(true));
        assert_eq!(sink.last_state().unwrap().kill_switch, Some(true));

        // Disarming drops the field entirely (off is reported as absent).
        let s = b.set_kill_switch(false).unwrap();
        assert_eq!(s.kill_switch, None);
    }

    #[test]
    fn set_autoconnect_arms_and_disarms() {
        let (b, sink) = backend();
        let s = b.set_autoconnect(true).unwrap();
        assert_eq!(s.autoconnect, Some(true));
        assert_eq!(sink.last_state().unwrap().autoconnect, Some(true));

        // Disarming drops the field entirely (off is reported as absent).
        let s = b.set_autoconnect(false).unwrap();
        assert_eq!(s.autoconnect, None);
    }

    #[test]
    fn set_tun_updates_the_stack_and_emits() {
        let (b, sink) = backend();
        // The mock starts on the default stack, like the core reports it.
        assert_eq!(b.status().unwrap().tun_stack, Some(TunStack::System));

        let s = b.set_tun(TunStack::Gvisor).unwrap();
        assert_eq!(s.tun_stack, Some(TunStack::Gvisor));
        assert_eq!(sink.last_state().unwrap().tun_stack, Some(TunStack::Gvisor));
    }

    #[test]
    fn set_dns_records_toggle_and_resolvers_and_emits() {
        let (b, sink) = backend();
        // The mock starts on the default resolvers, like the core reports them.
        let start = b.status().unwrap();
        assert_eq!(start.dns_remote.as_deref(), Some("tls://1.1.1.1"));

        let s = b
            .set_dns(true, "tls://9.9.9.9".into(), "udp://8.8.8.8".into())
            .unwrap();
        assert_eq!(s.ad_block, Some(true));
        assert_eq!(s.dns_remote.as_deref(), Some("tls://9.9.9.9"));
        assert_eq!(s.dns_direct.as_deref(), Some("udp://8.8.8.8"));
        assert_eq!(sink.last_state().unwrap().ad_block, Some(true));

        // Disarming drops ad_block, and an empty resolver falls back to the default.
        let s = b
            .set_dns(false, String::new(), "udp://8.8.8.8".into())
            .unwrap();
        assert_eq!(s.ad_block, None);
        assert_eq!(s.dns_remote.as_deref(), Some("tls://1.1.1.1"));
    }

    #[test]
    fn import_link_creates_manual_profile_and_signals() {
        let (b, sink) = backend();
        let before = sink.profiles_count();
        let p = b.import_link("vless://example".into(), None).unwrap();
        assert_eq!(p.source, Source::Manual);
        assert_eq!(p.nodes.len(), 1);
        assert_eq!(p.nodes[0].protocol, Protocol::Vless);
        assert_eq!(sink.profiles_count(), before + 1);
        // The new profile is now listed.
        assert!(b.list_profiles().unwrap().iter().any(|x| x.id == p.id));
    }

    #[test]
    fn import_link_rejects_blank_and_honours_name_and_scheme() {
        let (b, _) = backend();
        assert!(b.import_link("".into(), None).is_err());
        assert!(b.import_link("   ".into(), None).is_err());

        let p = b
            .import_link("ss://server".into(), Some("My SS".into()))
            .unwrap();
        assert_eq!(p.name, "My SS");
        assert_eq!(p.nodes[0].protocol, Protocol::Shadowsocks);
    }

    #[test]
    fn parse_links_mock_counts_and_dedupes() {
        // A block with two good links, a comment, a blank, a junk line and a
        // duplicate: two nodes, one skipped, the dup collapsed.
        let block = "# header\nvless://a@h:443\n\n// note\ngarbage\nvless://a@h:443\nss://b@h:8388";
        let (nodes, skipped) = parse_links_mock(&[block.to_string()]);
        assert_eq!(nodes.len(), 2, "two unique link-shaped lines");
        assert_eq!(skipped, 1, "one junk line skipped");
    }

    #[test]
    fn import_links_creates_one_multi_node_profile_and_signals() {
        let (b, sink) = backend();
        let before = sink.profiles_count();
        let res = b
            .import_links(
                vec!["vless://a@h:443\ntrojan://b@h:443\nnot-a-link".into()],
                Some("Batch".into()),
            )
            .unwrap();
        assert_eq!(res.imported, 2);
        assert_eq!(res.skipped, 1);
        assert_eq!(res.profile.source, Source::Manual);
        assert_eq!(res.profile.nodes.len(), 2);
        assert_eq!(res.profile.name, "Batch");
        assert_eq!(sink.profiles_count(), before + 1);
        assert!(b
            .list_profiles()
            .unwrap()
            .iter()
            .any(|p| p.id == res.profile.id));
    }

    #[test]
    fn import_links_defaults_name_and_rejects_all_invalid() {
        let (b, _) = backend();
        // No name -> a non-empty default.
        let res = b
            .import_links(vec!["vless://a@h:443".into()], None)
            .unwrap();
        assert!(!res.profile.name.is_empty());
        // Nothing parseable -> an error, not an empty profile.
        assert!(b
            .import_links(vec!["nope".into(), "also-bad".into()], None)
            .is_err());
    }

    #[test]
    fn import_subscription_creates_subscription_and_signals() {
        let (b, sink) = backend();
        let before = sink.profiles_count();
        let p = b
            .import_subscription("https://example.invalid/sub".into(), "Provider".into())
            .unwrap();
        assert_eq!(p.source, Source::Subscription);
        assert_eq!(p.name, "Provider");
        assert!(!p.nodes.is_empty());
        assert_eq!(sink.profiles_count(), before + 1);
    }

    #[test]
    fn import_subscription_validates_name_and_url() {
        let (b, _) = backend();
        assert!(b
            .import_subscription("https://x".into(), "  ".into())
            .is_err());
        assert!(b.import_subscription("   ".into(), "Name".into()).is_err());
    }

    #[test]
    fn connect_transitions_to_connecting() {
        let (b, sink) = backend();
        let s = b.connect("demo-sub".into(), None, false).unwrap();
        assert_eq!(s.state, ConnectionState::Connecting);
        // Default (non-auto) node selection picks the head of the list.
        assert_eq!(s.node, Some("demo-nl".into()));
        assert_eq!(s.profile, Some("demo-sub".into()));
        // The synchronous transition was emitted.
        assert_eq!(
            sink.last_state().unwrap().state,
            ConnectionState::Connecting
        );
        // Stop the background dial so it can't race a later test's timing.
        let _ = b.disconnect();
    }

    #[test]
    fn connect_honours_an_explicit_node() {
        let (b, _) = backend();
        let s = b
            .connect("demo-sub".into(), Some("demo-de".into()), false)
            .unwrap();
        assert_eq!(s.node, Some("demo-de".into()));
        let _ = b.disconnect();
    }

    #[test]
    fn connect_explicit_node_overrides_auto() {
        // With both an explicit node and auto set, the explicit node wins —
        // matching the core, where auto is ignored when a node is named.
        let (b, _) = backend();
        let s = b
            .connect("demo-sub".into(), Some("demo-de".into()), true)
            .unwrap();
        assert_eq!(s.node, Some("demo-de".into()));
        let _ = b.disconnect();
    }

    #[test]
    fn connect_auto_picks_the_lowest_ping_node() {
        // Auto must select the reachable node with the smallest synthetic ping,
        // exactly what the ping panel would surface for the same profile.
        let (b, _) = backend();
        let profiles = b.list_profiles().unwrap();
        let sub = profiles.iter().find(|p| p.id == "demo-sub").unwrap();
        let expected = fastest_node(sub).expect("at least one reachable demo node");

        let s = b.connect("demo-sub".into(), None, true).unwrap();
        assert_eq!(s.node, Some(expected));
        let _ = b.disconnect();
    }

    #[test]
    fn connect_rejects_unknown_node_and_profile() {
        assert!(b_connect_err(Some("nope".into())));
        let (b, _) = backend();
        assert!(b.connect("does-not-exist".into(), None, false).is_err());
    }

    /// Helper: a connect to demo-sub with the given node that is expected to
    /// fail, returning whether it did. Keeps the rejection test terse.
    fn b_connect_err(node: Option<String>) -> bool {
        let (b, _) = backend();
        b.connect("demo-sub".into(), node, false).is_err()
    }

    #[test]
    fn disconnect_returns_to_idle() {
        let (b, _) = backend();
        let _ = b.connect("demo-sub".into(), None, false).unwrap();
        let s = b.disconnect().unwrap();
        assert_eq!(s.state, ConnectionState::Idle);
        assert_eq!(s.node, None);
    }

    #[test]
    fn remove_profile_drops_it_and_signals() {
        let (b, sink) = backend();
        let before = sink.profiles_count();
        b.remove_profile("demo-manual".into()).unwrap();
        assert!(!b
            .list_profiles()
            .unwrap()
            .iter()
            .any(|p| p.id == "demo-manual"));
        assert_eq!(sink.profiles_count(), before + 1);
        assert!(b.remove_profile("missing".into()).is_err());
    }

    #[test]
    fn refresh_subscription_only_for_subscriptions() {
        let (b, _) = backend();
        assert!(b.refresh_subscription("demo-sub".into()).is_ok());
        // A manual profile can't be refreshed, and a missing one errors.
        assert!(b.refresh_subscription("demo-manual".into()).is_err());
        assert!(b.refresh_subscription("missing".into()).is_err());
    }

    #[test]
    fn ping_returns_a_result_per_node() {
        let (b, _) = backend();
        let results = b.ping("demo-sub".into()).unwrap();
        assert_eq!(results.len(), 3);
        let profiles = b.list_profiles().unwrap();
        let ids: Vec<&str> = profiles[0].nodes.iter().map(|n| n.id.as_str()).collect();
        for r in &results {
            assert!(
                ids.contains(&r.node.as_str()),
                "ping for unknown node {r:?}"
            );
        }
        assert!(b.ping("missing".into()).is_err());
    }

    #[test]
    fn leak_check_idle_is_neutral_and_never_a_dns_pass() {
        let (b, _) = backend();
        let leak = b.leak_check().unwrap();
        assert!(!leak.connected);
        assert_eq!(leak.exit_match, None);
        assert_eq!(leak.ip_verdict, Verdict::Neutral);
        // The honesty invariant: an idle store must not report a DNS pass.
        assert_eq!(leak.dns.status, DnsStatus::Inconclusive);
        assert_ne!(leak.dns.status, DnsStatus::Ok);
    }

    #[test]
    fn removing_the_active_profile_tears_down_the_tunnel() {
        let (b, sink) = backend();
        let _ = b.connect("demo-sub".into(), None, false).unwrap();
        let states_before = sink.state_count();
        b.remove_profile("demo-sub".into()).unwrap();
        // Removing the connected profile emits an extra idle state event.
        assert!(sink.state_count() > states_before);
        assert_eq!(b.status().unwrap().state, ConnectionState::Idle);
        assert_eq!(b.status().unwrap().profile, None);
    }
}
