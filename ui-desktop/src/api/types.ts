// Mirror of docs/control-protocol.md. These shapes are the boundary between the
// desktop UI and the core sidecar; keep them byte-for-byte aligned with the Go
// side. Anything the protocol calls optional is optional here.

export type ConnectionState = "idle" | "connecting" | "connected" | "error";

export type RoutingMode = "smart" | "global" | "direct";

export type SplitMode = "off" | "exclude" | "include";

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
  error?: string;
}

export interface Node {
  id: string;
  name: string;
  protocol: NodeProtocol;
  server: string;
  port: number;
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
}

export interface PingResult {
  node: string;
  rttMs: number;
  ok: boolean;
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
