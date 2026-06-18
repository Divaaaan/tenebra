import { useEffect, useRef, useState } from "react";

import type { Profile } from "../api";
import type { Tenebra } from "../state/useTenebra";
import { useI18n } from "../i18n/I18nContext";
import { formatBytes, formatRate } from "../lib/format";
import { RollingNumber } from "../components/RollingNumber";
import { Sparkline } from "../components/Sparkline";

// How many rate samples the Home sparkline keeps. At one traffic tick per
// second this is roughly a minute of history — enough to read the shape of a
// burst without the line turning to noise.
const SPARK_WINDOW = 60;

interface HomeScreenProps {
  tenebra: Tenebra;
  selectedProfile: Profile | null;
  onSelectProfile: (id: string) => void;
  onGoToProfiles: () => void;
}

/** A small directional chevron marking download vs upload. Decorative: the
 *  adjacent text label carries the meaning for assistive tech. */
function TrafficArrow({ direction }: { direction: "down" | "up" }) {
  return (
    <svg
      className="traffic-arrow"
      viewBox="0 0 16 16"
      width="14"
      height="14"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {direction === "down" ? (
        <path d="M8 3v10M4 9l4 4 4-4" />
      ) : (
        <path d="M8 13V3M4 7l4-4 4 4" />
      )}
    </svg>
  );
}

export function HomeScreen({
  tenebra,
  selectedProfile,
  onSelectProfile,
  onGoToProfiles,
}: HomeScreenProps) {
  const { t } = useI18n();
  const { state, traffic, profiles } = tenebra;

  // "" means auto — let the core pick the lowest-ping node.
  const [nodeId, setNodeId] = useState("");
  const [busy, setBusy] = useState(false);

  // Rolling rate history feeding the two sparklines. Sampled off the live
  // traffic counters: one entry per traffic tick, bounded to SPARK_WINDOW.
  // Reset whenever a session ends so the next connection starts from a clean
  // line rather than replaying the last session's tail.
  const phase = state.state;
  const [history, setHistory] = useState<{ down: number[]; up: number[] }>({
    down: [],
    up: [],
  });
  const lastSample = useRef<number | null>(null);

  useEffect(() => {
    if (phase !== "connected") {
      // Clear once on leaving the connected state; the guard avoids setState on
      // every render while idle.
      if (lastSample.current !== null) {
        lastSample.current = null;
        setHistory({ down: [], up: [] });
      }
      return;
    }
    // Coalesce by the upstream sample identity (rates only change when the core
    // emits a new traffic event), so equal back-to-back renders don't pad the
    // window with duplicates.
    const stamp = traffic.downRate + traffic.upRate * 1e-3;
    if (lastSample.current === stamp) {
      return;
    }
    lastSample.current = stamp;
    setHistory((prev) => ({
      down: [...prev.down, traffic.downRate].slice(-SPARK_WINDOW),
      up: [...prev.up, traffic.upRate].slice(-SPARK_WINDOW),
    }));
  }, [phase, traffic.downRate, traffic.upRate]);

  if (profiles.length === 0) {
    return (
      <section className="home home--empty">
        <div className="empty-state">
          <h1>{t.home.noProfile}</h1>
          <p className="muted">{t.home.noProfileHint}</p>
          <button type="button" className="btn btn-primary" onClick={onGoToProfiles}>
            {t.nav.profiles}
          </button>
        </div>
      </section>
    );
  }

  const isConnected = phase === "connected";
  const isConnecting = phase === "connecting";
  const locked = isConnecting || isConnected;

  const activeProfile =
    profiles.find((p) => p.id === state.profile) ?? selectedProfile;
  const activeNode = activeProfile?.nodes.find((n) => n.id === state.node);

  async function handlePrimary() {
    if (busy) return;
    setBusy(true);
    try {
      if (phase === "connected" || phase === "connecting") {
        await tenebra.disconnect();
      } else if (selectedProfile) {
        await tenebra.connect(selectedProfile.id, nodeId || undefined);
      }
    } catch {
      // The state event reflects failures; swallow the rejection so a transient
      // error doesn't tear down the view.
    } finally {
      setBusy(false);
    }
  }

  const buttonLabel = isConnected
    ? t.home.disconnect
    : isConnecting
      ? t.state.connecting
      : t.home.connect;

  return (
    <section className={`home home--${phase}`}>
      <div className="home-dial">
        <button
          type="button"
          className={`dial dial--${phase}`}
          onClick={handlePrimary}
          disabled={busy && !locked}
          title={isConnecting ? t.home.cancel : buttonLabel}
        >
          <span className="dial-bloom" aria-hidden="true" />
          <span className="dial-aura" aria-hidden="true" />
          <span className="dial-spokes" aria-hidden="true" />
          <span className="dial-track" aria-hidden="true" />
          <span className="dial-ring" aria-hidden="true" />
          <span className="dial-well" aria-hidden="true" />
          <span className="dial-core" aria-hidden="true" />
          <span className="dial-label">
            <svg
              className="dial-glyph"
              viewBox="0 0 24 24"
              width="26"
              height="26"
              fill="none"
              stroke="currentColor"
              strokeWidth="2.4"
              strokeLinecap="round"
              aria-hidden="true"
            >
              {/* The universal power mark: an open arc with a vertical stem. */}
              <path d="M12 3v8" />
              <path d="M7.5 6.3a6.5 6.5 0 1 0 9 0" />
            </svg>
            {buttonLabel}
          </span>
        </button>

        <p className="home-status" aria-live="polite">
          <span className="home-status-word">{t.state[phase]}</span>
        </p>

        {phase === "error" && state.error && (
          <div className="home-error" role="alert">
            {state.error}
          </div>
        )}
      </div>

      <dl className="home-meta">
        <div className="home-meta-row">
          <dt>{t.home.activeProfile}</dt>
          <dd>{activeProfile?.name ?? "—"}</dd>
        </div>
        <div className="home-meta-row">
          <dt>{t.home.activeNode}</dt>
          <dd>{activeNode?.name ?? (locked ? t.home.autoNode : "—")}</dd>
        </div>
      </dl>

      {isConnected && (
        <div className="home-traffic">
          <div className="traffic-stat traffic-stat--down">
            <span className="traffic-head">
              <TrafficArrow direction="down" />
              <span className="traffic-label">{t.home.download}</span>
            </span>
            <RollingNumber
              className="traffic-rate"
              value={traffic.downRate}
              format={(n) => formatRate(n, t.units.perSecond)}
            />
            <Sparkline
              points={history.down}
              width={130}
              height={34}
              aria-label={t.home.download}
            />
            <RollingNumber
              className="traffic-total muted"
              value={traffic.down}
              format={(n) => `${t.home.sessionTraffic}: ${formatBytes(n)}`}
            />
          </div>
          <div className="traffic-stat traffic-stat--up">
            <span className="traffic-head">
              <TrafficArrow direction="up" />
              <span className="traffic-label">{t.home.upload}</span>
            </span>
            <RollingNumber
              className="traffic-rate"
              value={traffic.upRate}
              format={(n) => formatRate(n, t.units.perSecond)}
            />
            <Sparkline
              points={history.up}
              width={130}
              height={34}
              aria-label={t.home.upload}
            />
            <RollingNumber
              className="traffic-total muted"
              value={traffic.up}
              format={(n) => `${t.home.sessionTraffic}: ${formatBytes(n)}`}
            />
          </div>
        </div>
      )}

      <div className="home-quick">
        <p className="home-quick-title">{t.home.quickConnect}</p>
        <div className="home-quick-row">
          <label className="field">
            <span className="field-label">{t.home.activeProfile}</span>
            <select
              className="control"
              value={selectedProfile?.id ?? ""}
              disabled={locked}
              onChange={(e) => {
                onSelectProfile(e.target.value);
                setNodeId("");
              }}
            >
              {profiles.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>

          <label className="field">
            <span className="field-label">{t.home.activeNode}</span>
            <select
              className="control"
              value={nodeId}
              disabled={locked || !selectedProfile}
              onChange={(e) => setNodeId(e.target.value)}
            >
              <option value="">{t.home.autoNode}</option>
              {selectedProfile?.nodes.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name}
                </option>
              ))}
            </select>
          </label>
        </div>
      </div>
    </section>
  );
}
