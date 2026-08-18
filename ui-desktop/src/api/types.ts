// Mirror of docs/control-protocol.md. These shapes are the boundary between the
// desktop UI and the core sidecar; keep them byte-for-byte aligned with the Go
// side. Anything the protocol calls optional is optional here.

/**
 * `health_reconnecting` is the one-shot state the core announces right before an
 * automatic health failover reconnects to a different node, so the UI can tell it
 * apart from a user connect; the ordinary `connecting` → `connected` sequence to
 * the new node follows.
 */
export type ConnectionState =
  | "idle"
  | "connecting"
  | "connected"
  | "error"
  | "health_reconnecting";

export type RoutingMode = "smart" | "global" | "direct";

export type SplitMode = "off" | "exclude" | "include";

/**
 * The tun network stack sing-box runs: the kernel's own TCP/IP ("system",
 * fastest, the default), a userspace stack ("gvisor", slower but immune to tun
 * driver quirks), or TCP-on-system / UDP-on-gvisor ("mixed").
 */
export type TunStack = "system" | "gvisor" | "mixed";

/**
 * The connection mode the core runs: "tun" carries all traffic through a tun
 * device with auto_route (the default, needs the tun driver), or "system-proxy"
 * skips the tun and exposes a loopback mixed inbound the client points the OS
 * proxy at (for machines without the rights to install or run a tun).
 */
export type ConnectionMode = "tun" | "system-proxy";

export type NodeProtocol =
  | "vless"
  | "hysteria2"
  | "amneziawg"
  | "shadowsocks"
  | "trojan"
  | "vmess";

export interface State {
  state: ConnectionState;
  node?: string;
  profile?: string;
  routing?: RoutingMode;
  /**
   * The daemon build's release version, stamped into every core snapshot.
   * Omitted by daemons predating the field (≤0.4.3) — read that as "older than
   * the app", not "current": the hand-installed macOS LaunchDaemon is never
   * refreshed by the in-app updater, so it skews behind until reinstalled.
   * Also omitted from the synthetic reconnecting/lost states the Rust backend
   * pushes while the daemon is away, so only trust it on stable snapshots.
   */
  daemon_version?: string;
  /** Per-app split mode; omitted (treated as "off") when no split is active. */
  split?: SplitMode;
  /** Normalized executable names the split applies to; omitted when off. */
  split_apps?: string[];
  /** Whether the kill switch is armed; omitted (treated as off) when it isn't. */
  kill_switch?: boolean;
  /**
   * Whether forced TLS ClientHello fragmentation is armed (the DPI-obfuscation
   * override); omitted (treated as off) when it isn't.
   */
  tls_fragment?: boolean;
  /**
   * The two-hop chain selection: whether it is enabled and which entry/exit
   * servers (by stable id) it chains through. Omitted until the user has picked a
   * pair; carried even when disabled so the UI can prefill its selectors.
   */
  multihop?: Multihop;
  /** The tun network stack the current or next tunnel uses. */
  tun_stack?: TunStack;
  /**
   * The connection mode the current or next tunnel uses ("tun" / "system-proxy").
   * Present once the core has normalized it, so the mode selector can render from a
   * status alone.
   */
  proxy_mode?: ConnectionMode;
  /**
   * The loopback port the mixed inbound binds in system-proxy mode (inert in tun
   * mode). Present alongside proxy_mode so a future port control can prefill it.
   */
  proxy_port?: number;
  /**
   * Whether the core reconnects the last profile when the daemon starts
   * (service mode: at boot). Omitted (treated as off) when it doesn't.
   */
  autoconnect?: boolean;
  /**
   * Whether the health-failover watchdog is armed — it reconnects to another node
   * when the active one degrades. On by default in the core, so it is normally
   * present as `true`; omitted (treated as off) once the user disarms it.
   */
  auto_failover?: boolean;
  /** Whether DNS ad/tracker blocking is armed; omitted (treated as off) when it isn't. */
  ad_block?: boolean;
  /**
   * The resolvers the current or next tunnel uses: the encrypted resolver
   * reached over the proxy, and the direct one. Present once the core has
   * normalized them, so the UI can prefill the custom-DNS inputs with the
   * effective values (the configured resolvers, or the defaults when unset).
   */
  dns_remote?: string;
  dns_direct?: string;
  /**
   * Whether the DNS strategy is pinned to IPv4-only (A records, no AAAA);
   * omitted (treated as off) when it isn't.
   */
  ipv4_only?: boolean;
  /**
   * Custom domain-suffix routing rules: destinations pinned to the direct
   * outbound, and to the proxy (tunnel). Normalized by the core; omitted when
   * empty, like the split fields.
   */
  rules_direct?: string[];
  rules_proxy?: string[];
  /**
   * Whether the bundled Russian banking / government direct-rule presets are on;
   * omitted (treated as off) when they aren't.
   */
  preset_ru_banking?: boolean;
  preset_ru_gov?: boolean;
  /**
   * Crash-report consent as a tri-state: `undefined` (omitted) when the user has
   * not been asked yet, `true` opted in, `false` declined. Distinct from
   * `crash_reports_asked` so "declined" reads apart from "not asked".
   */
  crash_reports?: boolean;
  /**
   * Whether the crash-report consent has been answered at all — the first-run
   * prompt shows only while this is falsy. Omitted (treated as false) when the
   * user has not been asked.
   */
  crash_reports_asked?: boolean;
  error?: string;
}

/**
 * The two-hop chain selection mirrored from the core: whether it is enabled and
 * the stable ids of the entry and exit servers it chains through (entry first).
 * `entry_id`/`exit_id` are omitted until the user picks them.
 */
export interface Multihop {
  enabled: boolean;
  entry_id?: string;
  exit_id?: string;
}

/**
 * The local crash report the core read back from `crash-gui.txt`, plus a cheap
 * change signature (file size + mtime) the UI stores on dismissal so the same
 * crash isn't re-offered next launch, but a newer one (different signature) is.
 */
export interface CrashReport {
  text: string;
  signature: string;
}

export interface Node {
  id: string;
  name: string;
  protocol: NodeProtocol;
  server: string;
  port: number;
  /**
   * TLS certificate verification is off on this node (the subscription's
   * skip-cert-verify, passed through to sing-box's insecure:true). Omitted by
   * the core when verification is on, so a missing flag means secure.
   */
  insecure?: boolean;
}

export interface Profile {
  id: string;
  name: string;
  source: "subscription" | "manual";
  /** Subscription URL, kept locally and never logged. */
  url?: string;
  nodes: Node[];
  /** RFC3339 timestamp of the last refresh. */
  updatedAt: string;
  /** From the subscription user-info header, when present. */
  expiresAt?: string;
  /** Bytes used, from the subscription user-info header. */
  trafficUsed?: number;
  /** Bytes included, from the subscription user-info header. */
  trafficTotal?: number;
  /**
   * Whether the core recognised this as a managed subscription — one served by
   * the operator's own infrastructure (host + path shape). Presentation only (it
   * drives a badge), never a capability. Omitted (treated as false) when it isn't.
   */
  managed?: boolean;
  /**
   * Entitlement tier the core resolved for a managed subscription. Omitted when
   * unknown or not applicable. UX only — it drives a badge; any real premium
   * capability is derived from server data, not this label.
   */
  tier?: "premium" | "free";
}

export interface PingResult {
  node: string;
  rttMs: number;
  ok: boolean;
}

/**
 * How far a probe got before it failed. Mirrors the core's `nodecheck.Stage`.
 *
 * The distinction is what a plain ping cannot express: `dial` means the address
 * never answered, `handshake` means it answered TCP and then never completed the
 * proxy handshake — a node in that state passes a TCP ping and looks like the
 * *fastest* one — and `probe` means the tunnel came up but traffic did not
 * survive it.
 */
export type CheckStage = "ok" | "dial" | "handshake" | "probe";

/** One control request through one node. */
export interface CheckTarget {
  target: string;
  stage: CheckStage;
  /** Time to first byte in ms; meaningful only when stage is "ok". */
  rttMs: number;
}

/**
 * Everything measured through a single node. A node counts as usable only when a
 * strict majority of its targets succeeded — one lucky destination is exactly
 * how a black-holed node passes a naive check.
 */
export interface NodeCheckResult {
  node: string;
  targets: CheckTarget[];
}

/**
 * The full verdict of a `check_nodes` run: every node ranked best-first, plus the
 * one to connect to.
 *
 * `best` is empty when nothing works, and the UI must say exactly that. Falling
 * back to the least-bad node is the failure the command exists to prevent — the
 * node that broke on 2026-08-18 was the one a "least bad" rule would have picked.
 */
export interface NodeCheck {
  results: NodeCheckResult[];
  best: string;
}

/**
 * Result of a batch link import (`import_links`): the single profile built from
 * all the valid links, plus how many links were imported and how many were
 * skipped because they didn't parse. Mirror of the core's wrapped response — the
 * UI surfaces "imported N, skipped M".
 */
export interface BatchImportResult {
  profile: Profile;
  imported: number;
  skipped: number;
}

/**
 * Best-effort NAT-mapping classification from a STUN probe, aimed at peer-to-peer
 * reachability rather than the full RFC 3489 cone taxonomy. `open` (the reflexive
 * address is one of our own interfaces — no NAT), `endpoint-independent` (both
 * servers saw the same mapping — cone-like, P2P-friendly), `endpoint-dependent`
 * (different mappings — symmetric, P2P-hostile), `unknown` (only one server
 * answered — too little to classify), `blocked` (no answer). Mirror of the core's
 * nat types.
 */
export type NatType =
  | "open"
  | "endpoint-independent"
  | "endpoint-dependent"
  | "unknown"
  | "blocked";

/**
 * Result of `run_stun_check`. Mirror of the core's STUN probe result: whether any
 * STUN server answered over UDP, the reflexive public IP one observed, and a
 * best-effort NAT classification. `external_ip` is omitted when no server answered
 * (then `udp_ok` is false and `nat_type` is `"blocked"`).
 */
export interface StunResult {
  /** Whether any STUN server answered over UDP. */
  udp_ok: boolean;
  /** Best-effort NAT-mapping classification for peer-to-peer reachability. */
  nat_type: NatType;
  /** The reflexive public IP a STUN server observed for us, when one answered. */
  external_ip?: string;
}

/**
 * Result of `run_speed_test`. Mirror of the core's throughput measurement through
 * the active tunnel: the download rate in megabits per second, the bytes the
 * sample actually read, and how long that took. Gated on a live connection — the
 * core rejects it while idle.
 */
export interface SpeedTestResult {
  /** Download throughput in megabits per second. */
  mbps: number;
  /** The bytes the sample actually read. */
  sample_bytes: number;
  /** How long the sample took, in milliseconds. */
  duration_ms: number;
}

// Events the core pushes without being asked.

export type LogLevel = "info" | "warn" | "error";

export interface StateEvent {
  state: ConnectionState;
  node?: string;
  error?: string;
}

export interface TrafficEvent {
  /** Total bytes sent this session. */
  up: number;
  /** Total bytes received this session. */
  down: number;
  /** Instantaneous send rate, bytes per second. */
  up_rate: number;
  /** Instantaneous receive rate, bytes per second. */
  down_rate: number;
}

export interface LogEvent {
  level: LogLevel;
  msg: string;
}

/**
 * One candidate's status within a fallback walk. `waiting` until the walk
 * reaches it, `trying` while its connectivity is probed, then `ok` (it came up)
 * or `blocked` (it failed and the walk moved on).
 */
export type AttemptStatus = "waiting" | "trying" | "blocked" | "ok";

/**
 * One line of a fallback-walk snapshot: a candidate's place in the plan, the
 * protocol and node it targets, its status, and whether it is the profile's
 * last-good node (the one the walk leads with).
 *
 * `strategy` and `reason` annotate the adaptive-transport walk and are both
 * optional (the core omits them when empty), so a candidate that connected on its
 * native parameters carries neither. `strategy` names the non-default transport
 * variation a candidate is being tried under or came up on — a reshaped handshake
 * on the same node — and is absent while it is on its own parameters. `reason`
 * carries the failure classification when a candidate was abandoned because its
 * handshake looked interfered with (`"censored"`), giving the UI the "why" behind
 * a block.
 */
export interface Attempt {
  seq: number;
  protocol: NodeProtocol;
  node: string;
  status: AttemptStatus;
  last_good: boolean;
  strategy?: string;
  reason?: string;
}

/**
 * A full snapshot of the anti-DPI fallback walk. The core re-emits it on every
 * status change, so the latest snapshot is always the complete picture.
 * `outcome` is "" while the walk is in progress and settles to "ok" (a candidate
 * came up) or "exhausted" (every candidate failed).
 */
export interface AttemptsEvent {
  items: Attempt[];
  outcome: "" | "ok" | "exhausted";
}

/** An installed zapret bundle: where it lives and what strategies it offers. */
export interface ZapretBundle {
  dir: string;
  /** Strategy names, best-guess default first; empty when nothing is installed. */
  strategies: string[] | null;
}

/** One strategy's measured outcome. */
export interface ZapretResult {
  strategy: string;
  /** Whether winws actually came up — distinct from "came up and did not help". */
  started: boolean;
  targets: { target: string; ok: boolean; rtt_ms: number }[] | null;
}

/**
 * The outcome of probing every strategy.
 *
 * `baseline` is how many targets already worked with zapret off; `improved` is
 * false when nothing beat it, which means the block is elsewhere — or there is
 * no block — and enabling a packet filter would buy nothing.
 */
export interface ZapretPick {
  baseline: number;
  targets: number;
  best?: string;
  improved: boolean;
  results: ZapretResult[] | null;
}