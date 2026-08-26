import { useState } from "react";

import { api, type NatType, type SpeedTestResult, type StunResult } from "../api";
import { useI18n } from "../i18n/I18nContext";
import type { Strings } from "../i18n/strings";
import { REPORT_PROBLEM_EVENT } from "../lib/useProblemReport";

interface DiagnosticsPanelProps {
  /**
   * Whether a tunnel is live. The speed test measures throughput *through* the
   * tunnel, so the core rejects it while idle; the button mirrors that gate.
   */
  connected: boolean;
}

/** Localize a NAT-mapping class the core reported. */
function natLabel(t: Strings, nat: NatType): string {
  switch (nat) {
    case "open":
      return t.settings.natOpen;
    case "endpoint-independent":
      return t.settings.natEndpointIndependent;
    case "endpoint-dependent":
      return t.settings.natEndpointDependent;
    case "blocked":
      return t.settings.natBlocked;
    default:
      return t.settings.natUnknown;
  }
}

/**
 * The Diagnostics section body: two on-demand probes. The STUN/NAT check runs on
 * any network; the speed test is gated on a live connection. Each call goes
 * straight to the core (like the leak check on the Logs screen) — the results are
 * ephemeral view state, never part of the global connection State. Nothing runs
 * until the user asks, and nothing leaves the machine beyond the probe itself.
 */
export function DiagnosticsPanel({ connected }: DiagnosticsPanelProps) {
  const { t } = useI18n();

  const [stun, setStun] = useState<StunResult | null>(null);
  const [stunBusy, setStunBusy] = useState(false);
  const [stunError, setStunError] = useState(false);

  const [speed, setSpeed] = useState<SpeedTestResult | null>(null);
  const [speedBusy, setSpeedBusy] = useState(false);
  const [speedError, setSpeedError] = useState(false);

  const [bundlePath, setBundlePath] = useState<string | null>(null);
  const [bundleBusy, setBundleBusy] = useState(false);
  const [bundleError, setBundleError] = useState(false);

  async function runStun() {
    if (stunBusy) {
      return;
    }
    setStunBusy(true);
    setStunError(false);
    try {
      setStun(await api.runStunCheck());
    } catch {
      setStun(null);
      setStunError(true);
    } finally {
      setStunBusy(false);
    }
  }

  async function runSpeed() {
    // The core rejects a speed test while idle; keep the button honest and never
    // fire one that would just error.
    if (speedBusy || !connected) {
      return;
    }
    setSpeedBusy(true);
    setSpeedError(false);
    try {
      setSpeed(await api.runSpeedTest());
    } catch {
      setSpeed(null);
      setSpeedError(true);
    } finally {
      setSpeedBusy(false);
    }
  }

  async function saveBundle() {
    // Not gated on anything: the report is most useful precisely when nothing
    // works, and it reads state the core holds whether or not a tunnel is up.
    if (bundleBusy) {
      return;
    }
    setBundleBusy(true);
    setBundleError(false);
    try {
      // Only the path matters here: this button is "keep a copy", and the text
      // it wrote is the report flow's business, not this row's.
      setBundlePath((await api.collectDiagnostics()).path);
    } catch {
      setBundlePath(null);
      setBundleError(true);
    } finally {
      setBundleBusy(false);
    }
  }

  // Ask the app shell to run the report flow rather than running one here.
  // The shell owns that surface for two reasons: it can put the app's own log
  // buffer into the report (this panel never sees it), and the report survives
  // the settings overlay being closed on top of it. Same one-line custom-event
  // handshake simple mode uses to hand the shell back its full view.
  function askForReport() {
    window.dispatchEvent(new CustomEvent(REPORT_PROBLEM_EVENT));
  }

  const sampleMb = speed ? (speed.sample_bytes / (1024 * 1024)).toFixed(1) : "";
  const sampleSecs = speed ? (speed.duration_ms / 1000).toFixed(1) : "";

  return (
    <div className="set-diag">
      {/* STUN / NAT probe — not gated on a connection. */}
      <div className="set-diag-probe">
        <div className="set-row">
          <span className="set-row-text">
            <span className="set-row-label">{t.settings.stunCheck}</span>
          </span>
          <button
            type="button"
            className="set-btn"
            onClick={() => void runStun()}
            disabled={stunBusy}
          >
            {stunBusy ? t.settings.stunChecking : t.settings.stunCheck}
          </button>
        </div>

        {stunError && (
          <p className="set-error" role="alert">
            {t.settings.stunError}
          </p>
        )}

        {stun && !stunError && (
          <dl className="set-facts" role="status">
            <div
              className="set-fact"
              data-tone={stun.udp_ok ? "good" : "signal"}
            >
              <dt>{t.settings.stunUdp}</dt>
              <dd>{stun.udp_ok ? t.settings.stunUdpOk : t.settings.stunUdpBlocked}</dd>
            </div>
            <div className="set-fact">
              <dt>{t.settings.stunNat}</dt>
              <dd>{natLabel(t, stun.nat_type)}</dd>
            </div>
            <div className="set-fact">
              <dt>{t.settings.stunIp}</dt>
              <dd className="selectable">{stun.external_ip ?? "—"}</dd>
            </div>
          </dl>
        )}
      </div>

      {/* Tunnel speed test — gated on a live connection. */}
      <div className="set-diag-probe">
        <div className="set-row">
          <span className="set-row-text">
            <span className="set-row-label">{t.settings.speedTest}</span>
            {!connected && (
              <span id="speed-gate" className="set-row-hint">
                {t.settings.speedGateHint}
              </span>
            )}
          </span>
          <button
            type="button"
            className="set-btn"
            onClick={() => void runSpeed()}
            disabled={!connected || speedBusy}
            aria-describedby={!connected ? "speed-gate" : undefined}
          >
            {speedBusy ? t.settings.speedTesting : t.settings.speedTest}
          </button>
        </div>

        {speedError && (
          <p className="set-error" role="alert">
            {t.settings.speedError}
          </p>
        )}

        {speed && !speedError && (
          <dl className="set-facts" role="status">
            <div className="set-fact">
              <dt>{t.settings.speedResult}</dt>
              <dd>
                <span className="set-fact-kpi">{speed.mbps.toFixed(1)}</span>{" "}
                <span className="set-fact-unit">{t.conn.unitMbps}</span>
                <span className="set-fact-note">
                  {" · "}
                  {t.settings.speedSample
                    .replace("{mb}", sampleMb)
                    .replace("{s}", sampleSecs)}
                </span>
              </dd>
            </div>
          </dl>
        )}
      </div>

      {/* Support bundle — never gated: it is worth most when nothing works. */}
      <div className="set-diag-probe">
        <div className="set-row">
          <span className="set-row-text">
            <span className="set-row-label">{t.settings.supportBundle}</span>
            <span id="bundle-hint" className="set-row-hint">
              {t.settings.supportBundleHint}
            </span>
          </span>
          <button
            type="button"
            className="set-btn"
            onClick={() => void saveBundle()}
            disabled={bundleBusy}
            aria-describedby="bundle-hint"
          >
            {bundleBusy
              ? t.settings.supportBundleWorking
              : t.settings.supportBundle}
          </button>
        </div>

        {bundleError && (
          <p className="set-error" role="alert">
            {t.settings.supportBundleError}
          </p>
        )}

        {bundlePath && !bundleError && (
          <dl className="set-facts" role="status">
            <div className="set-fact" data-tone="good">
              <dt>{t.settings.supportBundleSaved}</dt>
              <dd className="selectable">{bundlePath}</dd>
            </div>
          </dl>
        )}
      </div>

      {/* Reporting the problem, as its own action. Saving a bundle answers
          "keep a copy"; it never answered "who do I tell", and a file the user
          is left holding is not a bug report. */}
      <div className="set-diag-probe">
        <div className="set-row">
          <span className="set-row-text">
            <span className="set-row-label">{t.report.action}</span>
            <span id="report-hint" className="set-row-hint">
              {t.report.panelHint}
            </span>
          </span>
          <button
            type="button"
            className="set-btn"
            onClick={askForReport}
            aria-describedby="report-hint"
          >
            {t.report.action}
          </button>
        </div>
      </div>
    </div>
  );
}
