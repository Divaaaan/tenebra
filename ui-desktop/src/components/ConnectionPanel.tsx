import type { ConnectionState } from "../api";
import { useI18n } from "../i18n/I18nContext";
import { formatBytes } from "../lib/format";
import type { TrafficHistory } from "../lib/useTrafficHistory";
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
  /** Connect / disconnect / abort, depending on phase. */
  onPrimary: () => void;
  /** Focus the node search (the "change" affordance). */
  onChange: () => void;
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
  onPrimary,
  onChange,
}: ConnectionPanelProps) {
  const { t } = useI18n();
  const connected = phase === "connected";
  const pending = phase === "connecting";

  const word = t.state[phase];
  const buttonLabel = connected
    ? `▢ ${t.home.disconnect}`
    : pending
      ? `· · · ${t.conn.abort}`
      : `▶ ${t.home.connect}`;

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
          <div className="conn-eyebrow">{t.conn.eyebrow}</div>
          <div className={`conn-word ${phase}`} aria-live="polite">
            <span className="ind" aria-hidden="true" />
            {word}
          </div>
          <div className="conn-sub">{subLine}</div>
        </div>

        <button
          type="button"
          className={`connect-btn${connected ? " on" : ""}`}
          onClick={onPrimary}
        >
          {buttonLabel}
        </button>

        <div className="cur-server">
          <div className="node">{nodeCode || "—"}</div>
          <div className="ip selectable">
            {connected && exitServer ? exitServer : t.conn.exitIp}
          </div>
          {nodeCity && <div className="city">{nodeCity}</div>}
          <button type="button" className="swap" onClick={onChange}>
            ▶ {t.conn.change}
          </button>
        </div>

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
