// Mirror of docs/control-protocol.md. These shapes are the boundary between the
// desktop UI and the core sidecar; keep them byte-for-byte aligned with the Go
// side. Anything the protocol calls optional is optional here.

export type ConnectionState = "idle" | "connecting" | "connected" | "error";

export type RoutingMode = "smart" | "global" | "direct";

export type SplitMode = "off" | "exclude" | "include";

/**
 * The tun network stack sing-box runs: the kernel's own TCP/IP ("system",
 * fastest, the default), a userspace stack ("gvisor", slower but immune to tun
 * driver quirks), or TCP-on-system / UDP-on-gvisor ("mixed").
 */
export type TunStack = "system" | "gvisor" | "mixed";

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
  /** Per-app split mode; omitted (treated as "off") when no split is active. */
  split?: SplitMode;
  /** Normalized executable names the split applies to; omitted when off. */
  split_apps?: string[];
  /** Whether the kill switch is armed; omitted (treated as off) when it isn't. */
  kill_switch?: boolean;
  /** The tun network stack the current or next tunnel uses. */
  tun_stack?: TunStack;
  /**
   * Whether the core reconnects the last profile when the daemon starts
   * (service mode: at boot). Omitted (treated as off) when it doesn't.
   */
  autoconnect?: boolean;
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
 */
export interface Attempt {
  seq: number;
  protocol: NodeProtocol;
  node: string;
  status: AttemptStatus;
  last_good: boolean;
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
