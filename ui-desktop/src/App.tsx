import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { UnlistenFn } from "@tauri-apps/api/event";

import { TopBar } from "./components/TopBar";
import { ConnectionPanel } from "./components/ConnectionPanel";
import { ServerList, type ServerRow } from "./components/ServerList";
import { BottomBar } from "./components/BottomBar";
import { UpdateBanner } from "./components/UpdateBanner";
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
import { EclipseOverlay } from "./components/EclipseOverlay";
import { useTenebra } from "./state/useTenebra";
import { useI18n } from "./i18n/I18nContext";
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
import { useNodePings } from "./lib/useNodePings";
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
  const update = useUpdateCheck();

  // Daemon build vs app build, latched from state snapshots. A skew means the
  // privileged daemon predates this UI (the macOS LaunchDaemon is never touched
  // by the in-app updater), so toggles of newer commands silently do nothing —
  // surface it instead. Dismissal is session-only: the degradation is real, so
  // it may re-prompt on the next launch.
  const daemonSkew = useDaemonSkew(state, __APP_VERSION__);
  const [skewDismissed, setSkewDismissed] = useState(false);

  // Crash reporting is core-owned and opt-in. `crashAsked` gates the one-time
  // consent banner; `crashConsent` (true only when explicitly enabled) gates
  // whether a crash saved from a previous run is surfaced at all.
  const crashConsent = state.crash_reports === true;
  const crashAsked = state.crash_reports_asked ?? false;
  const crash = useCrashReport(crashConsent, tenebra.ready);
  const [viewingReport, setViewingReport] = useState(false);

  const enableCrashReports = useCallback(() => {
    void tenebra.setCrashReports(true).catch(() => {});
  }, [tenebra]);
  const declineCrashReports = useCallback(() => {
    void tenebra.setCrashReports(false).catch(() => {});
  }, [tenebra]);
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
          const node = selectedNodeId || undefined;
          const auto = node ? undefined : getAutoFastest();
          await tenebra.connect(selectedProfileId, node, auto);
        }
      } catch {
        // Surfaced on the state/log channels.
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
      void tenebra.setRouting(mode).catch(() => {});
    },
    [tenebra],
  );

  const handleToggleKill = useCallback(() => {
    // Failures surface on the state/log channels the UI already renders.
    void tenebra.setKillSwitch(!killSwitch).catch(() => {});
  }, [tenebra, killSwitch]);

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

      {update.available && (
        <UpdateBanner
          version={update.available}
          installing={update.installing}
          progress={update.progress}
          onInstall={update.install}
          onDismiss={update.dismiss}
        />
      )}

      {daemonSkew.stale && !skewDismissed && (
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
      />

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
