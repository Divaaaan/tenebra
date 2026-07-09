import { invoke } from "@tauri-apps/api/core";
import { listen, type UnlistenFn } from "@tauri-apps/api/event";

import type {
  BatchImportResult,
  LogEvent,
  PingResult,
  Profile,
  RoutingMode,
  SplitMode,
  State,
  StateEvent,
  TrafficEvent,
  TunStack,
} from "./types";

// Typed wrapper over the Tauri command surface. The Rust shell forwards every
// call to whichever backend is wired in (mock today, the sidecar client later),
// so this file never needs to change when the real core arrives — the command
// names and payloads are the contract from docs/control-protocol.md.
//
// Commands take snake_case argument keys because that is what the Rust handlers
// declare; Tauri matches them verbatim.

export const api = {
  status(): Promise<State> {
    return invoke<State>("status");
  },

  listProfiles(): Promise<Profile[]> {
    return invoke<{ profiles: Profile[] }>("list_profiles").then(
      (r) => r.profiles,
    );
  },

  importSubscription(url: string, name: string): Promise<Profile> {
    return invoke<{ profile: Profile }>("import_subscription", {
      url,
      name,
    }).then((r) => r.profile);
  },

  importLink(link: string, name?: string): Promise<Profile> {
    return invoke<{ profile: Profile }>("import_link", { link, name }).then(
      (r) => r.profile,
    );
  },

  // Batch import: several share links (pasted block or a parsed .txt list) into a
  // single multi-server profile. The core splits, de-duplicates and skips
  // unparseable lines, returning how many it imported and skipped so the UI can
  // report the outcome. `links` may carry multi-line strings; an empty result
  // (no valid links) rejects.
  importLinks(links: string[], name?: string): Promise<BatchImportResult> {
    return invoke<BatchImportResult>("import_links", { links, name });
  },

  removeProfile(profile: string): Promise<void> {
    return invoke<void>("remove_profile", { profile });
  },

  refreshSubscription(profile: string): Promise<Profile> {
    return invoke<{ profile: Profile }>("refresh_subscription", {
      profile,
    }).then((r) => r.profile);
  },

  // `node` names an explicit exit; without one, `auto` chooses the core's
  // candidate ordering — true ranks nodes by measured ping (fastest first),
  // false (the default) keeps the protocol-fallback order. The core ignores
  // `auto` when a node is given.
  connect(profile: string, node?: string, auto?: boolean): Promise<State> {
    return invoke<State>("connect", { profile, node, auto });
  },

  disconnect(): Promise<State> {
    return invoke<State>("disconnect");
  },

  ping(profile: string): Promise<PingResult[]> {
    return invoke<{ results: PingResult[] }>("ping", { profile }).then(
      (r) => r.results,
    );
  },

  setRouting(mode: RoutingMode): Promise<State> {
    return invoke<State>("set_routing", { mode });
  },

  // Per-app split tunnelling. apps are executable file names (e.g. "chrome.exe").
  // The core normalizes them, so the returned State may reorder/lowercase them.
  setSplit(mode: SplitMode, apps: string[]): Promise<State> {
    return invoke<State>("set_split", { mode, apps });
  },

  // Arm/disarm the kill switch. The core persists the choice and, when a tunnel
  // is live, re-applies it in place (a brief connecting→connected dip while
  // sing-box hot-swaps on the same node — not a full reconnect).
  setKillSwitch(on: boolean): Promise<State> {
    return invoke<State>("set_kill_switch", { on });
  },

  // Switch the tun network stack. Same live re-apply semantics as the kill
  // switch; when idle the choice applies on the next connect.
  setTun(stack: TunStack): Promise<State> {
    return invoke<State>("set_tun", { stack });
  },

  // Arm/disarm connect-on-start. The core persists the choice and, when armed,
  // reconnects the last profile the next time the daemon itself starts (service
  // mode: at boot); nothing about a live tunnel changes.
  setAutoconnect(on: boolean): Promise<State> {
    return invoke<State>("set_autoconnect", { on });
  },

  /**
   * Run the IP / DNS leak check. The core performs the probes and returns the
   * assembled verdict (see {@link LeakCheck}); a live tunnel is needed for a
   * meaningful exit-match result.
   */
  leakCheck(): Promise<LeakCheck> {
    return invoke<LeakCheck>("leak_check");
  },

  /** Actually exit the app. Closing the window only hides it to the tray. */
  quit(): Promise<void> {
    return invoke<void>("quit_app");
  },
};

/** The IP-vs-exit comparison outcome. Mirror of the core's `ExitMatch`. */
export type ExitMatch = "match" | "mismatch" | "unknown";

/**
 * Severity of a leak-check finding the UI maps to pass/warn styling.
 * Mirror of the core's `Verdict`.
 */
export type LeakVerdict = "ok" | "warn" | "neutral" | "error";

/**
 * DNS leak assessment outcome. `inconclusive` and `unavailable` are NOT a pass;
 * the UI must never present them as "safe". Mirror of the core's `DNSStatus`.
 */
export type DnsStatus = "ok" | "leak" | "inconclusive" | "unavailable";

export interface DnsResult {
  status: DnsStatus;
  /** Observed resolver IPs, if any. Omitted (or empty) when none were seen. */
  resolvers?: string[];
  /** Human summary; always present. */
  message: string;
}

/**
 * Result of `leak_check`. Mirror of the core's `LeakCheck`
 * (docs/control-protocol.md): the observed public IP and a verdict on whether
 * traffic is leaving through the tunnel exit, plus a best-effort DNS assessment
 * that is honest about its limits.
 */
export interface LeakCheck {
  /** Public IP observed; omitted if every echo endpoint failed. */
  public_ip?: string;
  /** Best-effort ISO 3166-1 alpha-2 country for `public_ip`. */
  country?: string;
  /** The echo endpoint that answered, for transparency. */
  source?: string;
  /** Whether a tunnel was active at check time. */
  connected: boolean;
  /** The active node's configured exit address; present only when connected. */
  exit_server?: string;
  /** Verdict on whether the observed IP is the tunnel exit; omitted when idle. */
  exit_match?: ExitMatch;
  /** Overall severity of the IP finding, for styling. Always present. */
  ip_verdict: LeakVerdict;
  /** Short human summary of the IP finding. Always present. */
  ip_message: string;
  /** Best-effort DNS leak assessment. Always present. */
  dns: DnsResult;
}

// Event subscriptions. Each returns the Tauri unlisten handle; callers detach on
// unmount. Channel names match the protocol's event field exactly.

export function onState(
  handler: (e: StateEvent) => void,
): Promise<UnlistenFn> {
  return listen<StateEvent>("state", (event) => handler(event.payload));
}

export function onTraffic(
  handler: (e: TrafficEvent) => void,
): Promise<UnlistenFn> {
  return listen<TrafficEvent>("traffic", (event) => handler(event.payload));
}

export function onLog(handler: (e: LogEvent) => void): Promise<UnlistenFn> {
  return listen<LogEvent>("log", (event) => handler(event.payload));
}

// The core fires this (payload-less) when the stored profile set changes —
// notably after a background subscription refresh updates usage or node lists.
// The renderer responds by re-fetching the profile list so the view stays live.
export function onProfilesChanged(handler: () => void): Promise<UnlistenFn> {
  return listen("profiles", () => handler());
}

// Tray-originated events. The tray can disconnect on its own, but connecting and
// showing need the front end (it owns the selected profile and routing), so the
// Rust tray emits these for the renderer to act on. They carry no payload.

export function onTrayConnect(handler: () => void): Promise<UnlistenFn> {
  return listen("tray://connect", () => handler());
}

export function onTrayShow(handler: () => void): Promise<UnlistenFn> {
  return listen("tray://show", () => handler());
}

// Deep links (tenebra://). The Rust side parses and validates the URL — the one
// place that grammar lives — and hands the renderer an already-tagged action:
// `import` opens the import flow pre-filled with a subscription URL, `connect`
// connects a profile by id.

export type DeepLinkAction =
  | { action: "import"; url: string }
  | { action: "connect"; profile: string };

/**
 * Subscribe to deep links that arrive while the app is running. Mirrors the tray
 * events, but the payload carries the routed action.
 */
export function onDeepLink(
  handler: (action: DeepLinkAction) => void,
): Promise<UnlistenFn> {
  return listen<DeepLinkAction>("deep-link://action", (event) =>
    handler(event.payload),
  );
}

/**
 * Drain the deep links the app was launched with (cold start). At launch the
 * webview isn't listening yet, so the Rust side queues those links; the renderer
 * pulls them once on mount. The queue is cleared on read.
 */
export function takeLaunchDeepLinks(): Promise<DeepLinkAction[]> {
  return invoke<DeepLinkAction[]>("take_launch_deep_links");
}
