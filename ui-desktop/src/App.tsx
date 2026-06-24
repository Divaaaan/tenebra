import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { UnlistenFn } from "@tauri-apps/api/event";

import { TopBar } from "./components/TopBar";
import { ConnectionPanel } from "./components/ConnectionPanel";
import { ServerList, type ServerRow } from "./components/ServerList";
import { BottomBar } from "./components/BottomBar";
import { ProfilesScreen } from "./screens/ProfilesScreen";
import { SettingsScreen } from "./screens/SettingsScreen";
import { LogsScreen } from "./screens/LogsScreen";
import { useTenebra } from "./state/useTenebra";
import { useI18n } from "./i18n/I18nContext";
import type { RoutingMode } from "./api";
import { onTrayConnect, onTrayShow } from "./api";
import { locate, type Region } from "./lib/region";
import { useNodePings } from "./lib/useNodePings";
import { useSessionClock, formatUptime } from "./lib/useSessionClock";
import { useTrafficHistory } from "./lib/useTrafficHistory";
import { formatMbps } from "./lib/format";
import {
  getAutoconnect,
  getKillSwitch,
  getLastProfileId,
  setKillSwitch as persistKillSwitch,
  setLastProfileId,
} from "./lib/settings";

type Overlay = "profiles" | "settings" | "logs" | null;

// The kill-switch UI exists, but the core doesn't yet honour it at connect time
// (it's a routing option with no control-protocol command). Until that's wired,
// the toggle is shown disabled and the "armed" banner is suppressed — we never
// claim a protection we don't actually deliver. Flip this when the wiring lands.
const KILL_SWITCH_WIRED = false;

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
  const [killSwitch, setKillSwitchState] = useState(getKillSwitch);
  const [overlay, setOverlay] = useState<Overlay>(null);
  const [busy, setBusy] = useState(false);

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

  const handlePrimary = useCallback(() => {
    if (busy) return;
    setBusy(true);
    void (async () => {
      try {
        if (connected || phase === "connecting") {
          await tenebra.disconnect();
        } else if (selectedProfileId) {
          await tenebra.connect(selectedProfileId, selectedNodeId || undefined);
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

  const handleSetRouting = useCallback(
    (mode: RoutingMode) => {
      void tenebra.setRouting(mode).catch(() => {});
    },
    [tenebra],
  );

  const handleToggleKill = useCallback(() => {
    setKillSwitchState((prev) => {
      const next = !prev;
      persistKillSwitch(next);
      return next;
    });
  }, []);

  const focusSearch = useCallback(() => {
    searchRef.current?.focus();
  }, []);

  // Remember the last profile that connected, for autoconnect-on-launch.
  useEffect(() => {
    if (connected && state.profile) {
      setLastProfileId(state.profile);
    }
  }, [connected, state.profile]);

  // Close overlays on Escape.
  useEffect(() => {
    if (!overlay) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOverlay(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [overlay]);

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

  // Autoconnect on launch: once the snapshot and profiles are in, if enabled and
  // nothing is connected, connect the last profile. Fires at most once per run.
  const autoconnectTried = useRef(false);
  useEffect(() => {
    if (autoconnectTried.current || !tenebra.ready || profiles.length === 0) {
      return;
    }
    autoconnectTried.current = true;
    if (!getAutoconnect() || phase !== "idle") {
      return;
    }
    const lastId = getLastProfileId();
    if (lastId && profiles.some((p) => p.id === lastId)) {
      void tenebra.connect(lastId).catch(() => {});
    }
  }, [tenebra.ready, profiles, phase, tenebra]);

  return (
    <div className="app" data-conn={phase}>
      <TopBar activeProfile={metaProfile} />

      {connected && killSwitch && KILL_SWITCH_WIRED && (
        <div className="kill-banner" role="status">
          ⚠ {t.bottom.killBanner}
        </div>
      )}

      <div className="app-body">
        <ConnectionPanel
          phase={phase}
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
        killSwitchDisabled={!KILL_SWITCH_WIRED}
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
                />
              )}
              {overlay === "settings" && <SettingsScreen tenebra={tenebra} />}
              {overlay === "logs" && <LogsScreen tenebra={tenebra} />}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
