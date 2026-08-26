import { invoke } from "@tauri-apps/api/core";
import { listen, type UnlistenFn } from "@tauri-apps/api/event";

import type {
  ZapretPick,
  ZapretBundle,
  ZapretUpdate,
  AttemptsEvent,
  BatchImportResult,
  ConnectionMode,
  CrashReport,
  LogEvent,
  NodeCheck,
  PickProgressEvent,
  PingResult,
  ServiceChecks,
  Profile,
  RoutingMode,
  SavedDiagnostics,
  SpeedTestResult,
  SplitMode,
  State,
  StateEvent,
  StunResult,
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
  // `allowTunConflict` overrides the core's refusal to raise a tun while another
  // VPN owns the default route. It is per-connect and never sticky: whether two
  // tunnels overlap is a fact about the machine right now.
  connect(
    profile: string,
    node?: string,
    auto?: boolean,
    allowTunConflict?: boolean,
  ): Promise<State> {
    return invoke<State>("connect", {
      profile,
      node,
      auto,
      allowTunConflict,
    });
  },

  disconnect(): Promise<State> {
    return invoke<State>("disconnect");
  },

  ping(profile: string): Promise<PingResult[]> {
    return invoke<{ results: PingResult[] }>("ping", { profile }).then(
      (r) => r.results,
    );
  },

  /**
   * Measure what actually survives each node, and which one to connect to.
   *
   * Unlike {@link ping} this opens real connections through every node to
   * several destinations, so it takes seconds, not milliseconds — call it behind
   * a visible "checking" state. It is also the only one of the two whose answer
   * can be trusted for picking an exit: a node whose proxy handshake has stopped
   * answering still completes a TCP dial instantly and therefore *wins* a
   * latency-ranked pick while carrying nothing.
   *
   * `best` is empty when nothing works, which must be surfaced as such rather
   * than falling back to the least-bad node.
   */
  checkNodes(profile: string): Promise<NodeCheck> {
    return invoke<NodeCheck>("check_nodes", { profile });
  },

  /**
   * Check whether video, voice and game latency work right now.
   *
   * The three probes run concurrently in the core, so this costs about one
   * timeout rather than three — but it is still seconds, not milliseconds, and
   * belongs behind a visible "checking" state.
   */
  checkServices(): Promise<ServiceChecks> {
    return invoke<ServiceChecks>("check_services");
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

  // Arm/disarm forced TLS ClientHello fragmentation — the unconditional
  // DPI-obfuscation override. Same live re-apply semantics as the kill switch (a
  // brief connecting→connected dip while sing-box hot-swaps on the same node);
  // when idle it applies on the next connect.
  setTlsFragment(on: boolean): Promise<State> {
    return invoke<State>("set_tls_fragment", { on });
  },

  // Set the multihop chain: enable/disable the two-hop route and name the entry
  // and exit servers (by stable id, within `profile`) it runs through — entry
  // first. The core validates the pair when enabling and, when a tunnel is live,
  // re-applies it in place (a brief connecting→connected dip on the same node).
  // Disabling ignores the ids but keeps them recorded so the pick can be
  // re-enabled. The snake_case keys match the command's rename_all="snake_case"
  // on the Rust side (see the header note).
  setMultihop(
    profile: string,
    enabled: boolean,
    entryId: string,
    exitId: string,
  ): Promise<State> {
    return invoke<State>("set_multihop", {
      profile,
      enabled,
      entry_id: entryId,
      exit_id: exitId,
    });
  },

  // Switch the tun network stack. Same live re-apply semantics as the kill
  // switch; when idle the choice applies on the next connect.
  setTun(stack: TunStack): Promise<State> {
    return invoke<State>("set_tun", { stack });
  },

  // Switch the connection mode between "tun" and "system-proxy". Same live
  // re-apply semantics as the kill switch (a brief connecting→connected dip while
  // sing-box hot-swaps on the same node); when idle it applies on the next
  // connect. Switching to system-proxy points the OS proxy at the loopback mixed
  // inbound once connected; switching to tun clears it.
  setProxyMode(mode: ConnectionMode): Promise<State> {
    return invoke<State>("set_proxy_mode", { mode });
  },

  // Arm/disarm connect-on-start. The core persists the choice and, when armed,
  // reconnects the last profile the next time the daemon itself starts (service
  // mode: at boot); nothing about a live tunnel changes.
  setAutoconnect(on: boolean): Promise<State> {
    return invoke<State>("set_autoconnect", { on });
  },

  // Arm/disarm the health-failover watchdog (reconnects to another node when the
  // active one degrades). The core persists the choice; unlike the kill switch it
  // changes nothing about a live tunnel — the watchdog re-reads the flag on its
  // next tick, so a mid-session toggle takes effect without a reconnect.
  setAutoFailover(on: boolean): Promise<State> {
    return invoke<State>("set_auto_failover", { on });
  },

  // Record the crash-report consent (opt in or out). The core persists it and
  // echoes the tri-state back in `State`; like autoconnect it changes nothing
  // about a live tunnel, and nothing is ever sent — it only governs whether the
  // GUI offers to surface a locally saved crash report on the next launch.
  setCrashReports(on: boolean): Promise<State> {
    return invoke<State>("set_crash_reports", { on });
  },

  // Set the DNS preferences: the ad/tracker-block toggle, the IPv4-only toggle,
  // plus the two custom resolvers (the encrypted proxied resolver and the direct
  // one). The core persists them and, when a tunnel is live, re-applies them in
  // place (a brief connecting→connected dip while sing-box hot-swaps on the same
  // node). An empty resolver resets that one to the core's default; the returned
  // State echoes the effective values back. The snake_case keys match the
  // command's rename_all="snake_case" on the Rust side (see the header note).
  setDns(
    adBlock: boolean,
    dnsRemote: string,
    dnsDirect: string,
    ipv4Only: boolean,
  ): Promise<State> {
    return invoke<State>("set_dns", {
      ad_block: adBlock,
      dns_remote: dnsRemote,
      dns_direct: dnsDirect,
      ipv4_only: ipv4Only,
    });
  },

  // Set the custom domain-suffix routing rules and the RU direct-rule presets:
  // `rulesDirect` pins destinations to the direct outbound, `rulesProxy` to the
  // tunnel, and the presets add bundled banking / government direct rules. The
  // core validates each suffix (a malformed one rejects the whole call),
  // normalizes them, persists the set, and — when a tunnel is live — re-applies
  // it in place (a brief connecting→connected dip on the same node). The returned
  // State echoes the normalized values back. The snake_case keys match the
  // command's rename_all="snake_case" on the Rust side.
  setRules(
    rulesDirect: string[],
    rulesProxy: string[],
    presetRuBanking: boolean,
    presetRuGov: boolean,
  ): Promise<State> {
    return invoke<State>("set_rules", {
      rules_direct: rulesDirect,
      rules_proxy: rulesProxy,
      preset_ru_banking: presetRuBanking,
      preset_ru_gov: presetRuGov,
    });
  },

  // Toggle the bundled routing presets: `games` pins game clients to the direct
  // outbound by executable name, `voice` sends real-time UDP (ports
  // 50000-65535) direct, and `services` pins the commonly-censored domains to
  // the tunnel ahead of the geo split.
  //
  // Every field is optional and an omitted one leaves that preset alone, which
  // is why they travel in an object rather than as three positional booleans: a
  // caller that had to restate all three to change one would eventually restate
  // one wrong, and two of the three decide whether a class of traffic leaves the
  // tunnel. Undefined keys are dropped rather than sent, so "not named" reaches
  // the core as absent.
  setPresets(presets: {
    games?: boolean;
    voice?: boolean;
    services?: boolean;
  }): Promise<State> {
    const args: Record<string, boolean> = {};
    if (presets.games !== undefined) {
      args.games = presets.games;
    }
    if (presets.voice !== undefined) {
      args.voice = presets.voice;
    }
    if (presets.services !== undefined) {
      args.services = presets.services;
    }
    return invoke<State>("set_presets", args);
  },

  /**
   * Run the IP / DNS leak check. The core performs the probes and returns the
   * assembled verdict (see {@link LeakCheck}); a live tunnel is needed for a
   * meaningful exit-match result.
   */
  leakCheck(): Promise<LeakCheck> {
    return invoke<LeakCheck>("leak_check");
  },

  /** The installed bundle's strategies; empty when nothing is installed yet. */
  listZapret(): Promise<ZapretBundle> {
    return invoke<ZapretBundle>("list_zapret");
  },

  /**
   * Probe every installed strategy and report which one to use.
   *
   * Slow by nature — each strategy needs the packet filter attached, the control
   * requests made, and a clean detach before the next — so the caller should
   * show progress rather than block silently.
   */
  pickZapret(): Promise<ZapretPick> {
    return invoke<ZapretPick>("pick_zapret");
  },

  /**
   * Turn the bypass on: the named strategy, or the one the last probe picked.
   *
   * Separate from picking on purpose — the measurement answers "which one", but
   * the user still needs a switch that just turns it on without waiting minutes
   * for a re-probe.
   */
  startZapret(name?: string): Promise<{ active: string }> {
    return invoke<{ active: string }>("start_zapret", name ? { name } : {});
  },

  /** Turn the bypass off. */
  stopZapret(): Promise<void> {
    return invoke<void>("stop_zapret").then(() => undefined);
  },

  /**
   * Install the newest published bundle, or a first one when none is installed.
   *
   * Worth a button of its own because a stale bypass does not degrade, it stops
   * working — and it fails exactly like a dead node or a broken subscription, so
   * "am I running the current one" is the question that separates those. Answers
   * with the versions before and after and whether anything changed.
   */
  updateZapret(): Promise<ZapretUpdate> {
    return invoke<ZapretUpdate>("update_zapret");
  },

  /**
   * Arm or disarm the background bundle updater. On by default: a bypass a few
   * releases behind is the most common way this stops working, and noticing that
   * yourself means first suspecting everything else.
   */
  setZapretAutoUpdate(on: boolean): Promise<void> {
    return invoke<void>("set_zapret_auto_update", { on }).then(() => undefined);
  },

  /**
   * Probe the current network path with a STUN Binding Request: whether outbound
   * UDP works, the reflexive public IP, and a best-effort NAT classification (see
   * {@link StunResult}). Not gated on a connection.
   */
  runStunCheck(): Promise<StunResult> {
    return invoke<StunResult>("run_stun_check");
  },

  /**
   * Measure download throughput through the active tunnel (see
   * {@link SpeedTestResult}). Gated on a live connection — the core rejects it
   * while idle, which surfaces here as a rejected promise.
   */
  runSpeedTest(): Promise<SpeedTestResult> {
    return invoke<SpeedTestResult>("run_speed_test");
  },

  /**
   * Ask the core to assemble a support bundle, save it beside the crash log, and
   * resolve with the file's absolute path and its text.
   *
   * Nothing is sent anywhere: the core reports what it already knows (state,
   * versions, the machine's default routes, the last connect walk, the tail of
   * the log) with subscription tokens and node credentials already masked, and
   * the shell writes it to a file the user can attach to a report themselves.
   * The text comes back with it because the webview cannot read that file — it
   * is what the report flow puts on the clipboard.
   */
  collectDiagnostics(): Promise<SavedDiagnostics> {
    return invoke<SavedDiagnostics>("collect_diagnostics");
  },

  /** Actually exit the app. Closing the window only hides it to the tray. */
  quit(): Promise<void> {
    return invoke<void>("quit_app");
  },

  // --- crash diagnostics (local only, never networked) ---

  /**
   * Append an uncaught webview error to the local crash file. The webview can't
   * write files (CSP/capability), so the global error handlers and the
   * ErrorBoundary hand the message and a short stack excerpt to the core, which
   * appends them next to any native panic. Best-effort; the promise resolves
   * once written and is safe to ignore.
   */
  recordWebCrash(message: string, stackExcerpt: string): Promise<void> {
    return invoke<void>("record_web_crash", {
      message,
      stack_excerpt: stackExcerpt,
    });
  },

  /**
   * Read the local crash file, if any. Resolves to null in the healthy case
   * (no file, or blank). Reading is local file I/O in the core — no network.
   */
  checkCrashReport(): Promise<CrashReport | null> {
    return invoke<CrashReport | null>("check_crash_report");
  },

  /**
   * Open a pre-filled GitHub issue for the recorded crash in the user's default
   * browser. The core builds the whole URL (fixed repo host + a short title from
   * the local file) and opens it from Rust — the webview has no shell:open
   * capability. The full report is pasted by the user from the Copy button.
   */
  openReportUrl(): Promise<void> {
    return invoke<void>("open_report_url");
  },

  /**
   * Open the new-issue form for a problem the user is reporting by hand — no
   * crash file behind it, no consent gate.
   *
   * Like {@link openReportUrl} the whole URL is built in Rust from constants and
   * the version/OS it reads itself; the report travels on the clipboard. Call it
   * only from the user's own "open the issue form" click: assembling a report
   * must never open anything.
   */
  openProblemUrl(): Promise<void> {
    return invoke<void>("open_problem_url");
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

export function onState(handler: (e: StateEvent) => void): Promise<UnlistenFn> {
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

// The core pushes a full fallback-walk snapshot on every status change (and on a
// status re-sync while a walk is live), so the UI can show the anti-DPI attempt
// sequence — which protocols were tried, blocked, and which came up.
export function onAttempts(
  handler: (e: AttemptsEvent) => void,
): Promise<UnlistenFn> {
  return listen<AttemptsEvent>("attempts", (event) => handler(event.payload));
}

// The core narrates a bypass strategy probe run step by step: one event as the
// run opens, then one per measured strategy. It is a structured event rather
// than a line in the log stream because the screen that started the run needs
// the numbers, and digging them back out of a localized sentence breaks the
// first time the sentence is reworded.
export function onPickProgress(
  handler: (e: PickProgressEvent) => void,
): Promise<UnlistenFn> {
  return listen<PickProgressEvent>("pick_progress", (event) =>
    handler(event.payload),
  );
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
  { action: "import"; url: string } | { action: "connect"; profile: string };

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
