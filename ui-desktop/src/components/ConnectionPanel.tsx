import { useEffect, useState } from "react";

import type { AttemptsEvent, ConnectionState, RoutingMode } from "../api";
import { useI18n } from "../i18n/I18nContext";
import { formatBytes } from "../lib/format";
import { useScrambledText } from "../lib/useScrambledText";
import type { TrafficHistory } from "../lib/useTrafficHistory";
import { FallbackPanel } from "./FallbackPanel";
import { PingScale } from "./PingScale";
import { TrafficChart } from "./TrafficChart";

interface ConnectionPanelProps {
  phase: ConnectionState;
  /** Active/selected node display code (the node's own name). */
  nodeCode: string;
  /** Derived location subtitle; "" hides the line. */
  nodeCity: string;
  /** Configured exit address, shown once connected; null otherwise. */
  exitServer: string | null;
  /** Node transport, lowercased (e.g. "vless"). */
  protocolLabel: string;
  /** Session uptime as mm:ss. */
  uptime: string;
  /** Latest download throughput, bare Mbps number. */
  mbps: string;
  /** Active node round-trip in ms, or "—". */
  ping: string;
  history: TrafficHistory;
  /** Cumulative session bytes. */
  cumulativeDown: number;
  cumulativeUp: number;
  /**
   * Backend-supplied detail line: the failure reason in the error phase, or a
   * transport notice (e.g. reconnecting to the service) while connecting.
   */
  errorMsg?: string;
  /** Active routing mode, mirrored in the eyebrow; hides the route when absent. */
  routing?: RoutingMode;
  /** True when the exit is auto-picked rather than chosen by hand. */
  auto?: boolean;
  /**
   * The latest anti-DPI fallback-walk snapshot. When a walk is in flight (or just
   * settled) the panel replaces the node card with a live view of it.
   */
  attempts?: AttemptsEvent | null;
  /** Resolve a fallback candidate's node id to a display name. */
  resolveNodeName?: (nodeId: string) => string;
  /** Connect / disconnect / abort, depending on phase. */
  onPrimary: () => void;
  /** Focus the node search (the "change" affordance). */
  onChange: () => void;
}

// How long the fallback panel lingers after a walk comes up, before the node card
// takes back its place. A settle window, not an animation, so reduced-motion does
// not shorten it.
const OK_LINGER_MS = 2200;

/**
 * Whether the fallback panel should stand in for the node card: while a walk runs
 * (during connecting), briefly after it comes up (`okHold`), and for as long as an
 * exhausted walk keeps the panel in the error state. Pure, so the swap is testable
 * without timers.
 */
export function shouldShowFallback(
  attempts: AttemptsEvent | null | undefined,
  phase: ConnectionState,
  okHold: boolean,
): boolean {
  if (!attempts) {
    return false;
  }
  if (attempts.outcome === "") {
    return phase === "connecting";
  }
  if (attempts.outcome === "ok") {
    return okHold;
  }
  return phase === "error"; // exhausted — held until the next connect/disconnect
}

// Holds `true` for a short window after a walk settles "ok", so the success view
// lingers before the node card returns. Keyed on snapshot identity, so a fresh
// walk restarts it and any other outcome clears it at once.
function useOkHold(attempts: AttemptsEvent | null | undefined): boolean {
  const [hold, setHold] = useState(false);
  useEffect(() => {
    if (attempts?.outcome === "ok") {
      setHold(true);
      const id = setTimeout(() => setHold(false), OK_LINGER_MS);
      return () => clearTimeout(id);
    }
    setHold(false);
  }, [attempts]);
  return hold;
}

export function ConnectionPanel({
  phase,
  nodeCode,
  nodeCity,
  exitServer,
  protocolLabel,
  uptime,
  mbps,
  ping,
  history,
  cumulativeDown,
  cumulativeUp,
  errorMsg,
  routing,
  auto,
  attempts,
  resolveNodeName,
  onPrimary,
  onChange,
}: ConnectionPanelProps) {
  const { t } = useI18n();
  const connected = phase === "connected";
  const pending = phase === "connecting";

  const word = t.state[phase];
  const displayWord = useScrambledText(word);
  const buttonLabel = connected
    ? `▢ ${t.home.disconnect}`
    : pending
      ? `· · · ${t.conn.abort}`
      : `▶ ${t.home.connect}`;

  const routeName =
    routing === "global"
      ? t.settings.routingGlobal
      : routing === "direct"
        ? t.settings.routingDirect
        : t.settings.routingSmart;

  // A bare integer ping feeds the strength meter; "—" (no probe) shows neither
  // the meter nor a value — honest over decorative.
  const pingValue = /^\d+$/.test(ping) ? Number(ping) : null;

  const okHold = useOkHold(attempts);
  const showFallback = shouldShowFallback(attempts, phase, okHold);

  // The "change" affordance broadcasts a focus-search intent the server-list pane
  // listens for, so the two panes stay decoupled; the onChange callback is kept
  // for any host that still wires the search focus directly.
  const handleChange = () => {
    window.dispatchEvent(new CustomEvent("tenebra:focus-search"));
    onChange();
  };

  const subLine = connected ? (
    <span>
      {exitServer && <span className="b selectable">{exitServer}</span>}
      {exitServer && " · "}
      {protocolLabel} · <span className="b">{t.conn.subConnected}</span>
    </span>
  ) : pending ? (
    // A connecting state normally means a tunnel handshake (the generic
    // negotiating line); when the backend attaches a message — e.g. the GUI is
    // re-dialing a restarted service — that message is the honest thing to
    // show instead.
    <span className="sig">{errorMsg || t.conn.subPending}</span>
  ) : phase === "error" && errorMsg ? (
    <span className="sig">{errorMsg}</span>
  ) : (
    <span>{t.conn.subOff}</span>
  );

  const statDim = connected ? "" : " dim";

  return (
    <div className="pane conn">
      <div className="conn-inner">
        <div className="conn-status">
          <div className="conn-eyebrow">
            <span className="conn-eyebrow-label">{t.conn.eyebrow}</span>
            {routing && (
              <span className="conn-route">
                {t.bottom.routing} · {routeName}
              </span>
            )}
          </div>
          <div className={`conn-word ${phase}`}>
            <span className="ind" aria-hidden="true" />
            <span className="conn-word-text" aria-live="polite">
              {displayWord}
            </span>
          </div>
          {pending && (
            <div className="conn-rail" aria-hidden="true">
              <span className="conn-rail-run" />
            </div>
          )}
          <div className="conn-sub">{subLine}</div>
        </div>

        <div className="connect-row">
          <button
            type="button"
            className={`connect-btn${connected ? " on" : ""}${pending ? " pending" : ""}`}
            onClick={onPrimary}
          >
            {buttonLabel}
          </button>
          <span className="key-hint" aria-hidden="true">
            space
          </span>
        </div>

        {showFallback && attempts ? (
          <FallbackPanel attempts={attempts} resolveNodeName={resolveNodeName} />
        ) : (
          <div className="cur-server">
            <span className="tick tl" aria-hidden="true">
              +
            </span>
            <span className="tick tr" aria-hidden="true">
              +
            </span>
            <span className="tick bl" aria-hidden="true">
              +
            </span>
            <span className="tick br" aria-hidden="true">
              +
            </span>
            <div className="cur-head">
              <span className="node">{nodeCode || "—"}</span>
              {auto && <span className="auto-chip">{t.conn.autoTag}</span>}
            </div>
            <div className="ip selectable">
              {connected && exitServer ? exitServer : t.conn.exitIp}
            </div>
            <div className="cur-meta">
              {nodeCity && <span className="city">{nodeCity}</span>}
              {pingValue !== null && (
                <>
                  <PingScale rttMs={pingValue} />
                  <span className="cur-rtt">
                    {ping} {t.units.ms}
                  </span>
                </>
              )}
            </div>
            <button type="button" className="swap" onClick={handleChange}>
              ▶ {t.conn.change}
            </button>
          </div>
        )}

        <div className="conn-stats">
          <div className="stat">
            <div className="stat-lab">{t.conn.statSession}</div>
            <div className={`stat-val${statDim}`}>
              {connected ? uptime : "00:00"}
              <span className="u">{t.conn.unitMinSec}</span>
            </div>
          </div>
          <div className="stat">
            <div className="stat-lab">↓ {t.conn.statDown}</div>
            <div className={`stat-val${statDim}`}>
              {connected ? mbps : "0.0"}
              <span className="u">{t.conn.unitMbps}</span>
            </div>
          </div>
          <div className="stat">
            <div className="stat-lab">{t.conn.statPing}</div>
            <div className={`stat-val${statDim}`}>
              {connected ? ping : "—"}
              <span className="u">{t.units.ms}</span>
            </div>
          </div>
        </div>

        <div className="traffic">
          <div className="traffic-meta">
            <span className="down">
              <span className="swatch down" aria-hidden="true" />↓{" "}
              {t.home.download} · {formatBytes(connected ? cumulativeDown : 0)}
            </span>
            <span className="up">
              <span className="swatch up" aria-hidden="true" />↑ {t.home.upload}{" "}
              · {formatBytes(connected ? cumulativeUp : 0)}
            </span>
          </div>
          <div className="traffic-wrap">
            <TrafficChart
              down={history.down}
              up={history.up}
              active={connected}
            />
          </div>
        </div>
      </div>
    </div>
  );
}
