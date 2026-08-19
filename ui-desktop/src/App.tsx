import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { UnlistenFn } from "@tauri-apps/api/event";

import { TopBar } from "./components/TopBar";
import { ConnectionPanel } from "./components/ConnectionPanel";
import { ServerList, type ServerRow } from "./components/ServerList";
import { BottomBar } from "./components/BottomBar";
import { BlocklistPanel, type BlocklistSource } from "./components/BlocklistPanel";
import { readBlocklistFiles } from "./lib/blocklist";
import { looksLikeZapretBundle } from "./lib/zapret";
import { UpdateBanner } from "./components/UpdateBanner";
import { UpdateConfirm } from "./components/UpdateConfirm";
import { DaemonSkewBanner } from "./components/DaemonSkewBanner";
import { CrashConsentBanner } from "./components/CrashConsentBanner";
import { CrashReportBanner } from "./components/CrashReportBanner";
import { CrashReportModal } from "./components/CrashReportModal";
import { DeepLinkConfirm } from "./components/DeepLinkConfirm";
import { ProfilesScreen } from "./screens/ProfilesScreen";
import { SettingsScreen } from "./screens/SettingsScreen";
import { LogsScreen } from "./screens/LogsScreen";
import { ToastHost } from "./components/ToastHost";
import { SimpleView } from "./components/SimpleView";
import { SimpleSetup } from "./components/SimpleSetup";
import { EclipseOverlay } from "./components/EclipseOverlay";
import { useTenebra } from "./state/useTenebra";
import { useI18n } from "./i18n/I18nContext";
import { describeCoreError, isTunConflict } from "./i18n/strings";
import { pushToast } from "./lib/toast";
import type { RoutingMode } from "./api";
import {
  api,
  onDeepLink,
  onTrayConnect,
  onTrayShow,
  takeLaunchDeepLinks,
} from "./api";
import { dispatchDeepLink, type DeepLinkHandlers } from "./lib/deepLink";
import { locate, type Region } from "./lib/region";
import { useNodeCheck } from "./lib/useNodeCheck";
import { useNodePings } from "./lib/useNodePings";
import { useServiceChecks } from "./lib/useServiceChecks";
import { useSessionClock, formatUptime } from "./lib/useSessionClock";
import { useTrafficHistory } from "./lib/useTrafficHistory";
import { useUpdateCheck } from "./lib/useUpdateCheck";
import { useDaemonSkew } from "./lib/useDaemonSkew";
import { useCrashReport } from "./lib/useCrashReport";
import { useActionToasts } from "./lib/useActionToasts";
import { formatMbps } from "./lib/format";
import { getAutoFastest, migrateLegacyAutoconnect } from "./lib/settings";

type Overlay = "profiles" | "settings" | "logs" | null;

// Renderer-owned preference, written by the Settings toggle. Simple mode swaps the
// full shell for the one-button SimpleView. Tolerant of both the codebase's "1"/"0"
// convention and a plain "true", so a divergent writer can't silently disable it.
const SIMPLE_MODE_KEY = "tenebra.simpleMode";
function readSimpleMode(): boolean {
  const v = localStorage.getItem(SIMPLE_MODE_KEY);
  // The full shell is the default. Simple mode stays available for anyone who
  // wants one button and nothing else, but the shell is not the problem it was
  // taken for: what a first-run user lacked was not fewer controls, it was the
  // two setup steps being somewhere else. Those now live on the main screen
  // (see SimpleSetup), so the rich view is approachable without being stripped.
  return v === "1" || v === "true";
}

export function App() {
  const tenebra = useTenebra();
  const { t } = useI18n();
  const { state, traffic, profiles } = tenebra;
  const phase = state.state;
  const connected = phase === "connected";

  const [selectedProfileId, setSelectedProfileId] = useState<string | null>(
    null,
  );
  // "" means auto — let the core pick the lowest-ping node.
  const [selectedNodeId, setSelectedNodeId] = useState("");
  const [region, setRegion] = useState<Region | null>(null);
  const [query, setQuery] = useState("");
  const [overlay, setOverlay] = useState<Overlay>(null);
  const [busy, setBusy] = useState(false);

  // Blocklist import. It lives on the main screen rather than inside Settings
  // because it is something the user does with a file in hand, not something
  // they go looking for while configuring — burying it two screens deep is how
  // an import feature ends up unused.
  const [blocklistOpen, setBlocklistOpen] = useState(false);
  const [blocklists, setBlocklists] = useState<BlocklistSource[]>([]);

  const importBlocklist = useCallback(async (files: File[]) => {
    // A zapret bundle is a different thing from a blocklist and is recognised
    // by content, not by name: it is a zip carrying bin/winws.exe and a set of
    // strategy .bat files. Sending it to the core is what makes "drop the
    // archive into the VPN" actually work, instead of the reader trying to
    // parse an executable as a list of domains.
    if (files.length === 1 && (await looksLikeZapretBundle(files[0]))) {
      const bytes = new Uint8Array(await files[0].arrayBuffer());
      const bundle = await api.importZapret(bytes, files[0].name);
      await afterZapretImport(files[0].name, bundle.strategies?.length ?? 0);
      return;
    }

    const parsed = await readBlocklistFiles(files);
    // One drop is one entry, however many files it held: dropping an unpacked
    // release means dropping a dozen files, and listing each would bury the one
    // number that matters — how many rules are now loaded.
    const label =
      files.length === 1
        ? files[0].name
        : `${files[0].name} + ${files.length - 1}`;
    setBlocklists((prev) => [
      // Re-importing the same source replaces it instead of stacking duplicates,
      // which is what happens when a user re-drops an updated list.
      ...prev.filter((s) => s.label !== label),
      { id: label, label, rules: parsed.rules.length },
    ]);
  }, []);

  /**
   * Shared tail of a bundle import: record it, then find the strategy that
   * works here.
   *
   * The probe is started automatically because the answer is not guessable — a
   * bundle ships ~20 strategies precisely because which one defeats a given
   * ISP's DPI cannot be known in advance. Leaving the user to pick from a list
   * of names would hand them the exact problem the import was meant to solve.
   */
  const afterZapretImport = useCallback(async (label: string, strategies: number) => {
    setBlocklists((prev) => [
      ...prev.filter((s) => s.label !== label),
      { id: label, label, rules: strategies },
    ]);
    pushToast(`zapret: ${strategies} стратегий, подбираю рабочую…`);

    try {
      const pick = await api.pickZapret();
      if (pick.improved && pick.best) {
        // The core leaves the winner running, so reflect that here rather than
        // showing the bypass as off while it is actually on.
        setZapretActive(pick.best);
        pushToast(`zapret: включена ${pick.best}`);
      } else {
        // Saying "nothing helped" is more useful than silently keeping the
        // least-bad option and letting the user believe the block is handled.
        pushToast(
          `zapret: ни одна стратегия не улучшила (уже работает ${pick.baseline}/${pick.targets})`,
        );
      }
    } catch (e) {
      pushToast(describeCoreError(e, t));
    }
  }, [t]);

  /**
   * Import from paths — an archive or an already-unpacked folder.
   *
   * The core decides what a path is: it checks for bin/winws.exe and strategy
   * .bat files rather than trusting the name, so "zapret", "zapret (1)" and a
   * folder the user renamed all work the same. A path that is not a bundle
   * comes back as an error the panel shows.
   */
  const importFromPaths = useCallback(
    async (paths: string[]) => {
      if (paths.length === 0) return;
      const bundle = await api.importZapretPath(paths[0]);
      const label = paths[0].split(/[\\/]/).pop() ?? paths[0];
      await afterZapretImport(label, bundle.strategies?.length ?? 0);
    },
    [afterZapretImport],
  );

  // Which bypass strategy is running, or "" when it is off. Held here so the
  // panel can name it and the button knows which way it flips.
  const [zapretActive, setZapretActive] = useState("");

  const enableZapret = useCallback(async () => {
    const r = await api.startZapret();
    setZapretActive(r.active);
    pushToast(`zapret: включён (${r.active})`);
  }, []);

  const disableZapret = useCallback(async () => {
    await api.stopZapret();
    setZapretActive("");
    pushToast("zapret: выключен");
  }, []);

  /**
   * Import a subscription from a link pasted on the simple screen.
   *
   * Named after the profile's own host so the user sees something recognisable
   * instead of an untitled entry: they pasted a link, not a name, and asking
   * for one would be a step for nothing.
   */
  const handleSimpleSubscribe = useCallback(async (url: string) => {
    let name = "VPN";
    try {
      name = new URL(url).hostname || name;
    } catch {
      // Not a URL the parser likes — the core will reject it with a better
      // message than anything guessed here.
    }
    await api.importSubscription(url, name);
  }, []);

  const removeBlocklist = useCallback(
    (id: string) => setBlocklists((prev) => prev.filter((s) => s.id !== id)),
    [],
  );

  // Simple mode: the Settings toggle writes `tenebra.simpleMode`; we mirror it here
  // and swap the whole shell for SimpleView when it's on. A cross-window write
  // arrives as a `storage` event; the same-window toggle can nudge us with a
  // `tenebra:simple-mode` custom event. Either way we re-read the source of truth.
  const [simpleMode, setSimpleMode] = useState(readSimpleMode);
  useEffect(() => {
    const sync = () => setSimpleMode(readSimpleMode());
    const onStorage = (e: StorageEvent) => {
      if (e.key === SIMPLE_MODE_KEY || e.key === null) sync();
    };
    window.addEventListener("storage", onStorage);
    window.addEventListener("tenebra:simple-mode", sync);
    return () => {
      window.removeEventListener("storage", onStorage);
      window.removeEventListener("tenebra:simple-mode", sync);
    };
  }, []);

  // A brief "eclipse" over the main screen, played by three taps on the wordmark
  // (see TopBar). Purely cosmetic; the overlay is inert and clears itself. A
  // stable ender keeps the overlay's timer from restarting on unrelated
  // re-renders (traffic ticks, etc.).
  const [eclipse, setEclipse] = useState(false);
  const endEclipse = useCallback(() => setEclipse(false), []);
  const playEclipse = useCallback(() => {
    setEclipse(true);
    // A quiet line for anyone with the console open.
    console.info("%c◐ eclipse · in tenebris lux", "color:#ff3d00");
  }, []);

  // Deep-link (tenebra://) intents. `importPreset` carries a subscription URL to
  // pre-fill the import flow with; `pendingConnect` names a profile to connect
  // once the profile list is in (a cold-start link can land before it loads).
  const [importPreset, setImportPreset] = useState<string | null>(null);
  const [pendingConnect, setPendingConnect] = useState<string | null>(null);
  const clearImportPreset = useCallback(() => setImportPreset(null), []);

  // A connect deep link never fires on arrival: it can be handed to the app by
  // any visited web page. `connectRequest` holds the profile a link asked to
  // connect until the user approves it in the confirmation prompt; only then is
  // it promoted to `pendingConnect`, which the effect below acts on. Import
  // already needs a click (it just opens the pre-filled dialog) — this makes
  // connect parallel.
  const [connectRequest, setConnectRequest] = useState<string | null>(null);
  const confirmConnectRequest = useCallback(() => {
    setConnectRequest((profile) => {
      if (profile) setPendingConnect(profile);
      return null;
    });
  }, []);
  const cancelConnectRequest = useCallback(() => setConnectRequest(null), []);

  // The kill switch is core-owned: the daemon persists it, arms strict_route in
  // the tunnel config, and restarts a tunnel whose process dies while armed.
  // The UI just reflects the reported state and toggles it over the protocol.
  const killSwitch = state.kill_switch ?? false;

  // Launch update check: offers a found release in the banner strip, or — when
  // the auto-install preference is on — installs it silently and relaunches.
  // Gated on the tunnel state (the live `phase`): a relaunch would drop an
  // active VPN, so auto-install waits for the tunnel to go down and a manual
  // install while it is up asks first.
  const update = useUpdateCheck(phase);

  // Daemon build vs app build, latched from state snapshots. A skew means the
  // privileged daemon predates this UI — nothing the app installs itself
  // refreshes it, neither the macOS LaunchDaemon nor the Linux system service —
  // so toggles of newer commands silently do nothing; surface it instead.
  // Dismissal is session-only: the degradation is real, so it may re-prompt on
  // the next launch.
  const daemonSkew = useDaemonSkew(state, __APP_VERSION__);
  const [skewDismissed, setSkewDismissed] = useState(false);

  // Crash reporting is core-owned and opt-in. `crashAsked` gates the one-time
  // consent banner; `crashConsent` (true only when explicitly enabled) gates
  // whether a crash saved from a previous run is surfaced at all.
  const crashConsent = state.crash_reports === true;
  const crashAsked = state.crash_reports_asked ?? false;
  const crash = useCrashReport(crashConsent, tenebra.ready);
  const [viewingReport, setViewingReport] = useState(false);

  // The core-owned controls the shell drives directly. Their drawn position is
  // the state the daemon echoes back, so a refused command leaves the control
  // exactly where it was — and these used to discard the error, which made that
  // the whole of what the user got to see. Same reporting as the settings
  // screen: the toast bus, naming an out-of-date service when that is the cause.
  const reportRefusal = useCallback(
    (e: unknown) => pushToast(describeCoreError(e, t)),
    [t],
  );

  const enableCrashReports = useCallback(() => {
    void tenebra.setCrashReports(true).catch(reportRefusal);
  }, [tenebra, reportRefusal]);
  const declineCrashReports = useCallback(() => {
    void tenebra.setCrashReports(false).catch(reportRefusal);
  }, [tenebra, reportRefusal]);
  const createCrashIssue = useCallback(() => {
    // The core builds the whole URL and opens it from Rust; failures surface on
    // the log channel the UI already renders.
    void api.openReportUrl().catch(() => {});
  }, []);

  const searchRef = useRef<HTMLInputElement>(null);

  // Keep the selection valid: default to the connected profile, then the first
  // available, so the panes always have a target.
  useEffect(() => {
    if (selectedProfileId && profiles.some((p) => p.id === selectedProfileId)) {
      return;
    }
    setSelectedProfileId(state.profile ?? profiles[0]?.id ?? null);
  }, [profiles, state.profile, selectedProfileId]);

  const selectedProfile = useMemo(
    () => profiles.find((p) => p.id === selectedProfileId) ?? null,
    [profiles, selectedProfileId],
  );
  const connectedProfile = useMemo(
    () => profiles.find((p) => p.id === state.profile) ?? null,
    [profiles, state.profile],
  );
  const metaProfile = connectedProfile ?? selectedProfile;

  // Latency probes for the browsed profile, feeding the per-row ping + the
  // dead flag, and the live ping stat for the connected node.
  const pings = useNodePings(selectedProfileId);
  // What actually survives each node, measured on demand — the connect button
  // runs it before choosing an exit (see handlePrimary).
  const nodeCheck = useNodeCheck();
  // And, once connected, whether the three things the user came for actually
  // work: video, voice, game latency.
  const services = useServiceChecks(phase);
  const sessionSecs = useSessionClock(phase);
  const history = useTrafficHistory(phase, traffic.downRate, traffic.upRate);

  const nodes = selectedProfile?.nodes ?? [];

  const rows = useMemo<ServerRow[]>(
    () =>
      nodes.map((n) => {
        const loc = locate(n.name);
        const probe = pings.results.get(n.id);
        return {
          id: n.id,
          name: n.name,
          city: loc.label,
          region: loc.region,
          protocol: n.protocol,
          rttMs: probe ? probe.rttMs : null,
          dead: probe ? !probe.ok : false,
          insecure: n.insecure ?? false,
        };
      }),
    [nodes, pings.results],
  );

  // Lowest-ping live node, used as the auto target and the idle "current node".
  const bestNodeId = useMemo(() => {
    let best: string | null = null;
    let bestRtt = Infinity;
    for (const n of nodes) {
      const probe = pings.results.get(n.id);
      if (probe?.ok && probe.rttMs < bestRtt) {
        bestRtt = probe.rttMs;
        best = n.id;
      }
    }
    return best ?? nodes[0]?.id ?? null;
  }, [nodes, pings.results]);

  const targetNodeId = selectedNodeId || bestNodeId || "";
  const displayedNode = connected
    ? (connectedProfile?.nodes.find((n) => n.id === state.node) ?? null)
    : (selectedProfile?.nodes.find((n) => n.id === targetNodeId) ?? null);

  const liveNodeId = connected ? state.node : targetNodeId;
  const livePing = liveNodeId ? pings.results.get(liveNodeId)?.rttMs : undefined;

  // Confirm the App-level actions the user takes (reaching connected, arming the
  // kill switch, changing routing) with a toast. The initial status load is
  // silent; only genuine transitions speak.
  useActionToasts(
    { ready: tenebra.ready, phase, killSwitch, routing: state.routing },
    connected && displayedNode
      ? { name: displayedNode.name, protocol: displayedNode.protocol }
      : null,
    t,
  );

  // Whether the primary button has anything to act on. It mirrors the branches
  // of handlePrimary below exactly: a live (or in-flight) tunnel can always be
  // taken down, otherwise a connect needs a profile. Without a profile the
  // handler ran no branch at all — no invoke, no error, no toast — while the
  // button stayed fully live, so a click on a fresh install (or with the core
  // unreachable, which leaves the list empty) was swallowed in silence.
  // Disabling it is the smallest honest fix and matches SimpleView, which has
  // always gated its own button on having a profile.
  const canPrimary =
    connected ||
    phase === "connecting" ||
    phase === "health_reconnecting" ||
    selectedProfileId !== null;

  const handlePrimary = useCallback(() => {
    if (busy) return;
    setBusy(true);
    void (async () => {
      try {
        if (
          connected ||
          phase === "connecting" ||
          phase === "health_reconnecting"
        ) {
          // A click during an auto-recovery aborts it too, rather than racing a
          // fresh connect against the watchdog's in-flight reconnect.
          await tenebra.disconnect();
        } else if (selectedProfileId) {
          // No explicit node → let the core choose. The persisted "auto-select
          // fastest" preference decides between ping-ranked and protocol-fallback
          // order; it is read fresh (like autoconnect) so a Settings toggle takes
          // effect on the next connect without prop-threading. When a node is
          // selected, auto is moot — the core honours the explicit exit.
          let node = selectedNodeId || undefined;
          let auto = node ? undefined : getAutoFastest();

          // Before letting latency decide, find out what actually carries
          // traffic. A node whose proxy handshake has stopped answering still
          // completes a TCP dial instantly, so it reads as the *fastest* node and
          // wins a latency-ranked pick while every request through it hangs —
          // which is precisely how a working-looking connect left the user with
          // no internet. Measuring first costs seconds; picking blind costs the
          // session.
          if (!node) {
            const best = await nodeCheck.run(selectedProfileId);
            if (best) {
              node = best;
              auto = undefined;
            } else {
              // Nothing passed. Say so — and still try: the core's fallback walk
              // tries nodes in turn and may get through where a one-shot probe
              // did not, and refusing to connect at all would be a worse answer
              // than a slow one.
              pushToast(t.servers.noneUsable);
            }
          }
          try {
            await tenebra.connect(selectedProfileId, node, auto);
          } catch (e) {
            // The guard refuses to raise our tun while another VPN owns the
            // default route. That refusal is correct by default — two tunnels
            // routing everything leave the machine offline — but it must not be
            // a dead end: the user is the only one who knows whether the other
            // tunnel overlaps, so ask, and honour the answer for this connect
            // only.
            if (!isTunConflict(e)) throw e;
            pushToast(describeCoreError(e, t));
            if (!window.confirm(t.daemon.tunConflictOverride)) throw e;
            await tenebra.connect(selectedProfileId, node, auto, true);
          }
        }
      } catch (e) {
        // Say why nothing happened. A refused connect leaves the button exactly
        // where it was, and swallowing the reason (the old behaviour) turned
        // every refusal — a guard, a vanished node, a core that will not answer —
        // into "the button does not work", which is unanswerable from the outside.
        pushToast(describeCoreError(e, t));
      } finally {
        setBusy(false);
      }
    })();
  }, [busy, connected, phase, tenebra, selectedProfileId, selectedNodeId]);

  const handleSelectNode = useCallback(
    (id: string) => {
      setSelectedNodeId(id);
      // Re-handshake onto the chosen node when already connected.
      if (connected && selectedProfileId) {
        void tenebra.connect(selectedProfileId, id).catch(() => {});
      }
    },
    [connected, selectedProfileId, tenebra],
  );

  const handleSelectProfile = useCallback((id: string) => {
    setSelectedProfileId(id);
    setSelectedNodeId("");
  }, []);

  // The AUTO row: drop any hand-pinned node so the core picks the exit. When
  // already connected, re-handshake straight away onto the fastest node, the
  // node-click counterpart for auto.
  const handleSelectAuto = useCallback(() => {
    setSelectedNodeId("");
    if (connected && selectedProfileId) {
      void tenebra
        .connect(selectedProfileId, undefined, getAutoFastest())
        .catch(() => {});
    }
  }, [connected, selectedProfileId, tenebra]);

  const handleSetRouting = useCallback(
    (mode: RoutingMode) => {
      void tenebra.setRouting(mode).catch(reportRefusal);
    },
    [tenebra, reportRefusal],
  );

  const handleToggleKill = useCallback(() => {
    void tenebra.setKillSwitch(!killSwitch).catch(reportRefusal);
  }, [tenebra, killSwitch, reportRefusal]);

  const focusSearch = useCallback(() => {
    searchRef.current?.focus();
  }, []);

  // Close overlays on Escape.
  useEffect(() => {
    if (!overlay) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOverlay(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [overlay]);

  // Global shortcuts layered on Esc: Space is the primary connect / disconnect /
  // abort, and "/" jumps to node search. Both stand down while a field owns the
  // keyboard or a modal / overlay is up (Esc owns those layers) — the same reach
  // as the Esc handler above.
  //
  // The action and gate flags ride a ref that is refreshed every render, so the
  // listener is subscribed once and always reads current values. Re-subscribing
  // on each change (a naive dep array) leaves a window where a key landing
  // between a state update and the effect re-run runs against a stale closure —
  // e.g. Space pressed just as the profile list arrives would see no selected
  // profile and silently do nothing.
  const shortcutRef = useRef({
    handlePrimary,
    overlay,
    connectRequest,
    viewingReport,
  });
  shortcutRef.current = {
    handlePrimary,
    overlay,
    connectRequest,
    viewingReport,
  };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== " " && e.key !== "/") return;

      const {
        handlePrimary: primary,
        overlay: ov,
        connectRequest: cr,
        viewingReport: vr,
      } = shortcutRef.current;

      const el = e.target as HTMLElement | null;
      const tag = el?.tagName;
      const typing =
        tag === "INPUT" ||
        tag === "TEXTAREA" ||
        tag === "SELECT" ||
        el?.isContentEditable === true;
      if (typing || ov || cr || vr) return;

      if (e.key === "/") {
        e.preventDefault();
        // Focus the node search. The listener lives in the ServerList zone; this
        // fires the agreed cross-zone event ("tenebra:focus-search") so the
        // shortcut and the input stay decoupled.
        window.dispatchEvent(new CustomEvent("tenebra:focus-search"));
        return;
      }

      // Space = connect / disconnect / abort. Defer to a focused control that
      // activates on Space itself (buttons, links, the role="button" node rows),
      // so the key is never handled twice.
      const role = el?.getAttribute("role");
      if (
        tag === "BUTTON" ||
        tag === "A" ||
        role === "button" ||
        role === "link"
      ) {
        return;
      }
      e.preventDefault();
      primary();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Tray → front end. "Connect" runs the selected-profile flow; "Show" closes
  // any overlay so the main panel is in view.
  const connectRef = useRef(handlePrimary);
  connectRef.current = handlePrimary;
  useEffect(() => {
    let active = true;
    const unlisteners: UnlistenFn[] = [];
    void (async () => {
      const subs = await Promise.all([
        onTrayConnect(() => {
          if (!connected && phase !== "connecting") connectRef.current();
        }),
        onTrayShow(() => setOverlay(null)),
      ]);
      if (!active) {
        subs.forEach((u) => u());
        return;
      }
      unlisteners.push(...subs);
    })();
    return () => {
      active = false;
      unlisteners.forEach((u) => u());
    };
  }, []);

  // Autoconnect is core-owned: the daemon reconnects its last profile when it
  // starts (spawned sidecar or the boot-time service alike), so the renderer no
  // longer sends a launch connect. What remains is a one-shot migration of the
  // legacy localStorage flag — an old "on" arms the core once, then the stale
  // keys are dropped (kept on failure so the next launch retries).
  const migrationTried = useRef(false);
  useEffect(() => {
    if (migrationTried.current || !tenebra.ready) {
      return;
    }
    migrationTried.current = true;
    void migrateLegacyAutoconnect(state.autoconnect ?? false, () =>
      tenebra.setAutoconnect(true),
    ).catch(() => {});
  }, [tenebra, state.autoconnect]);

  // A deep-link "connect" names a profile by id, and only reaches here once the
  // user has approved the confirmation prompt. The link can arrive before the
  // profile list is loaded (cold start), so hold the id and connect once the
  // profile appears, honouring the fastest-node preference like a manual connect.
  useEffect(() => {
    if (!pendingConnect || !tenebra.ready) return;
    if (!profiles.some((p) => p.id === pendingConnect)) return;
    const id = pendingConnect;
    setPendingConnect(null);
    setSelectedProfileId(id);
    setSelectedNodeId("");
    void tenebra.connect(id, undefined, getAutoFastest()).catch(() => {});
  }, [pendingConnect, tenebra, profiles]);

  // Deep links (tenebra://). Links the app was launched with (cold start) are
  // drained once on mount; links opened while it runs arrive as events. Both go
  // through the same dispatch: import opens the pre-filled import flow, connect
  // hands off to the pending-connect effect above.
  useEffect(() => {
    let active = true;
    const unlisteners: UnlistenFn[] = [];
    const handlers: DeepLinkHandlers = {
      onImport: (url) => {
        setImportPreset(url);
        setOverlay("profiles");
      },
      // Don't connect on arrival — surface a confirmation the user must approve.
      onConnect: (profile) => setConnectRequest(profile),
    };
    void (async () => {
      try {
        const launched = await takeLaunchDeepLinks();
        if (active) launched.forEach((a) => dispatchDeepLink(a, handlers));
      } catch {
        // No launch links, or the host command is unavailable; nothing to drain.
      }
      const unlisten = await onDeepLink((a) => dispatchDeepLink(a, handlers));
      if (!active) {
        unlisten();
        return;
      }
      unlisteners.push(unlisten);
    })();
    return () => {
      active = false;
      unlisteners.forEach((u) => u());
    };
  }, []);

  // Simple mode: one calm screen instead of the full shell. It reads the same
  // connection state and shares the same actions, so the two never disagree. The
  // eclipse easter egg still rides along; the console/toast layers do too.
  if (simpleMode) {
    return (
      <div className="app app--simple" data-conn={phase}>
        <SimpleView
          phase={phase}
          busy={busy}
          onPrimary={handlePrimary}
          nodeName={displayedNode?.name ?? ""}
          profiles={profiles}
          selectedProfileId={selectedProfileId}
          onSelectProfile={handleSelectProfile}
          nodes={nodes}
          selectedNodeId={selectedNodeId}
          onSelectNode={handleSelectNode}
          onSelectAuto={handleSelectAuto}
          hasBypass={blocklists.length > 0}
          bypassActive={zapretActive}
          onSubscribe={handleSimpleSubscribe}
          onBypassFiles={importBlocklist}
          onBypassPaths={importFromPaths}
          serviceChecks={services.checks}
          serviceChecking={services.checking}
        />
        <EclipseOverlay active={eclipse} onDone={endEclipse} />
        <ToastHost />
      </div>
    );
  }

  return (
    <div className="app" data-conn={phase}>
      <TopBar activeProfile={metaProfile} onEclipse={playEclipse} />

      {connected && killSwitch && (
        <div className="kill-banner" role="status">
          ⚠ {t.bottom.killBanner}
        </div>
      )}

      {tenebra.coreError && (
        // The core never answered, so nothing on this screen is backed by
        // anything: no profiles, no real state, every action doomed. Say it in
        // the banner strip the update and skew notices already use (no new
        // visual language), and let the hook's retry clear it on its own.
        <div className="update-banner" role="alert">
          <span className="update-banner-text">⚠ {t.daemon.unreachable}</span>
        </div>
      )}

      {update.available && (
        <UpdateBanner
          version={update.available}
          installing={update.installing}
          deferred={update.deferred}
          progress={update.progress}
          onInstall={update.install}
          onDismiss={update.dismiss}
        />
      )}

      {/* Only once a snapshot has actually landed: the skew check latches its
          verdict from the hook's placeholder "idle" state, so a core that never
          answered used to read as a *stale* one — the wrong diagnosis, and it
          would now contradict the unreachable banner right above. */}
      {tenebra.ready && daemonSkew.stale && !skewDismissed && (
        <DaemonSkewBanner
          daemonVersion={daemonSkew.daemonVersion}
          appVersion={__APP_VERSION__}
          onDismiss={() => setSkewDismissed(true)}
        />
      )}

      {tenebra.ready && !crashAsked && (
        <CrashConsentBanner
          onEnable={enableCrashReports}
          onDecline={declineCrashReports}
        />
      )}

      {crash.report && (
        <CrashReportBanner
          onView={() => setViewingReport(true)}
          onCreateIssue={createCrashIssue}
          onDismiss={crash.dismiss}
        />
      )}

      {/* The two setup steps live on the main screen, not behind a menu: what a
          first-run user lacked was never fewer controls, it was these being
          somewhere else. The strip removes itself the moment both are done, so
          it costs a returning user nothing. */}
      <SimpleSetup
        hasProfile={profiles.length > 0}
        hasBypass={blocklists.length > 0}
        onSubscribe={handleSimpleSubscribe}
        onBypassFiles={importBlocklist}
        onBypassPaths={importFromPaths}
      />

      <div className="app-body">
        <ConnectionPanel
          phase={phase}
          routing={state.routing ?? "smart"}
          auto={!selectedNodeId}
          attempts={tenebra.attempts}
          resolveNodeName={(id) =>
            (connectedProfile ?? selectedProfile)?.nodes.find((n) => n.id === id)
              ?.name ?? id
          }
          nodeCode={displayedNode?.name ?? ""}
          nodeCity={displayedNode ? locate(displayedNode.name).label : ""}
          exitServer={connected ? (displayedNode?.server ?? null) : null}
          protocolLabel={displayedNode?.protocol ?? ""}
          uptime={formatUptime(sessionSecs)}
          mbps={formatMbps(traffic.downRate)}
          ping={livePing !== undefined ? String(livePing) : "—"}
          history={history}
          cumulativeDown={traffic.down}
          cumulativeUp={traffic.up}
          errorMsg={state.error}
          onPrimary={handlePrimary}
          disabled={!canPrimary}
          onChange={focusSearch}
        />

        <ServerList
          ref={searchRef}
          profiles={profiles}
          selectedProfileId={selectedProfileId}
          onSelectProfile={handleSelectProfile}
          rows={rows}
          activeNodeId={connected ? (state.node ?? null) : selectedNodeId || null}
          auto={!selectedNodeId}
          onSelectAuto={handleSelectAuto}
          region={region}
          onRegion={setRegion}
          query={query}
          onQuery={setQuery}
          onSelectNode={handleSelectNode}
          onAddSubscription={() => setOverlay("profiles")}
          pinging={pings.pinging}
        />
      </div>

      <BottomBar
        routing={state.routing ?? "smart"}
        onSetRouting={handleSetRouting}
        killSwitch={killSwitch}
        onToggleKillSwitch={handleToggleKill}
        onLeakCheck={() => setOverlay("logs")}
        onSettings={() => setOverlay("settings")}
        onBlocklist={() => setBlocklistOpen(true)}
        blocklistCount={blocklists.length}
      />

      {blocklistOpen && (
        <BlocklistPanel
          sources={blocklists}
          onImportFiles={importBlocklist}
          onImportPaths={importFromPaths}
          onEnable={enableZapret}
          onDisable={disableZapret}
          active={zapretActive}
          onRemove={removeBlocklist}
          onClose={() => setBlocklistOpen(false)}
        />
      )}

      {overlay && (
        <div
          className="overlay"
          role="dialog"
          aria-modal="true"
          onClick={(e) => {
            if (e.target === e.currentTarget) setOverlay(null);
          }}
        >
          <div className="overlay-panel">
            <button
              type="button"
              className="overlay-close"
              onClick={() => setOverlay(null)}
              aria-label={t.home.cancel}
            >
              ✕
            </button>
            <div className="overlay-body">
              {overlay === "profiles" && (
                <ProfilesScreen
                  tenebra={tenebra}
                  selectedProfileId={selectedProfileId}
                  onSelectProfile={setSelectedProfileId}
                  initialImport={importPreset}
                  onImportConsumed={clearImportPreset}
                  onConnected={() => setOverlay(null)}
                />
              )}
              {overlay === "settings" && <SettingsScreen tenebra={tenebra} />}
              {overlay === "logs" && <LogsScreen tenebra={tenebra} />}
            </div>
          </div>
        </div>
      )}

      {connectRequest && (
        <DeepLinkConfirm
          profile={
            profiles.find((p) => p.id === connectRequest)?.name ?? connectRequest
          }
          onConfirm={confirmConnectRequest}
          onCancel={cancelConnectRequest}
        />
      )}

      {update.confirming && (
        <UpdateConfirm
          onConfirm={update.confirmInstall}
          onCancel={update.cancelInstall}
        />
      )}

      {viewingReport && crash.report && (
        <CrashReportModal
          report={crash.report.text}
          onClose={() => setViewingReport(false)}
        />
      )}

      <EclipseOverlay active={eclipse} onDone={endEclipse} />

      <ToastHost />
    </div>
  );
}
