import { useEffect, useRef, useState } from "react";

import {
  api,
  type DnsStatus,
  type LeakCheck,
  type LeakVerdict,
} from "../api";
import type { Tenebra } from "../state/useTenebra";
import { useI18n } from "../i18n/I18nContext";
import type { Strings } from "../i18n/strings";
import { buildDiagnostics } from "../lib/diagnostics";
import { formatTime } from "../lib/format";
import { pushToast } from "../lib/toast";

interface LogsScreenProps {
  tenebra: Tenebra;
}

/** Tone the result rows are styled with. "good"/"bad" carry colour; "neutral"
 *  is deliberately uncoloured so an undeterminable check never reads as safe. */
type Tone = "good" | "bad" | "neutral";

/** Map an IP verdict to its tone. Only a confirmed exit match is "good"; a
 *  probable leak is "bad"; idle/unknown/error stay neutral (no false pass). */
function ipTone(v: LeakVerdict): Tone {
  switch (v) {
    case "ok":
      return "good";
    case "warn":
      return "bad";
    default:
      return "neutral";
  }
}

/** Map a DNS status to its tone. Inconclusive and unavailable are neutral, never
 *  "good" — the core is explicit that they are not a pass, and the UI honours it. */
function dnsTone(s: DnsStatus): Tone {
  switch (s) {
    case "ok":
      return "good";
    case "leak":
      return "bad";
    default:
      return "neutral";
  }
}

function ipHeadline(t: Strings, v: LeakVerdict): string {
  switch (v) {
    case "ok":
      return t.logs.leakIpOk;
    case "warn":
      return t.logs.leakIpWarn;
    case "error":
      return t.logs.leakIpError;
    default:
      return t.logs.leakIpNeutral;
  }
}

function dnsHeadline(t: Strings, s: DnsStatus): string {
  switch (s) {
    case "ok":
      return t.logs.leakDnsOk;
    case "leak":
      return t.logs.leakDnsLeak;
    case "unavailable":
      return t.logs.leakDnsUnavailable;
    default:
      return t.logs.leakDnsInconclusive;
  }
}

export function LogsScreen({ tenebra }: LogsScreenProps) {
  const { t, lang } = useI18n();
  const { logs } = tenebra;

  const [checking, setChecking] = useState(false);
  const [leak, setLeak] = useState<LeakCheck | null>(null);

  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const panel = panelRef.current;
    if (panel) {
      panel.scrollTop = panel.scrollHeight;
    }
  }, [logs.length]);

  async function checkLeak() {
    setChecking(true);
    try {
      setLeak(await api.leakCheck());
    } catch {
      setLeak(null);
    } finally {
      setChecking(false);
    }
  }

  // Clear the console and confirm it. The button is disabled while empty, so this
  // only fires with lines present; the guard keeps that true if called otherwise.
  function clearLogs() {
    if (logs.length === 0) {
      return;
    }
    tenebra.clearLogs();
    pushToast(t.toast.logsCleared);
  }

  // Gather the on-screen context (versions, platform, the last leak check and the
  // log buffer) into one masked text block and put it on the clipboard, so a user
  // can paste it into a report. Nothing leaves the machine — the copy is the whole
  // action. writeText works in the WebView2/webkit shells without a Tauri
  // permission, matching the crash-report copy; if it is refused we stay quiet
  // rather than claim a copy that didn't happen.
  async function copyDiagnostics() {
    const bundle = buildDiagnostics({
      appVersion: __APP_VERSION__,
      daemonVersion: tenebra.state.daemon_version,
      platform: navigator.userAgent,
      when: new Date(),
      leak,
      logs,
    });
    try {
      await navigator.clipboard.writeText(bundle);
      pushToast(t.toast.diagnosticsCopied);
    } catch {
      // Clipboard blocked; nothing was copied, so we don't confirm.
    }
  }

  return (
    <section className="log-screen">
      <header className="log-head">
        <div className="log-head-titles">
          <span className="log-eyebrow">{t.logs.leakCheck}</span>
          <h1 className="log-title">{t.logs.title}</h1>
        </div>
        <button
          type="button"
          className="log-btn-primary"
          disabled={checking}
          onClick={checkLeak}
        >
          {checking ? t.logs.checking : t.logs.leakCheck}
        </button>
      </header>

      {checking && (
        <div className="log-rail" aria-hidden="true">
          <span className="log-rail-run" />
        </div>
      )}

      {leak && <LeakReport leak={leak} t={t} />}

      <section className="log-console-block">
        <div className="log-console-head">
          <span className="log-eyebrow">{t.logs.title}</span>
          <div className="log-console-actions">
            <button
              type="button"
              className="log-btn-ghost"
              onClick={copyDiagnostics}
            >
              {t.logs.copy}
            </button>
            <button
              type="button"
              className="log-btn-ghost"
              disabled={logs.length === 0}
              onClick={clearLogs}
            >
              {t.logs.clear}
            </button>
          </div>
        </div>

        <div
          className="log-console"
          role="log"
          aria-live="polite"
          ref={panelRef}
        >
          {logs.length === 0 ? (
            <p className="log-empty">{t.logs.empty}</p>
          ) : (
            logs.map((line) => (
              <div key={line.id} className="log-row">
                <span className="log-time">{formatTime(line.at, lang)}</span>
                <span className={`log-lvl log-lvl--${line.level}`}>
                  {line.level}
                </span>
                <span className="log-msg">{line.msg}</span>
              </div>
            ))
          )}
        </div>
      </section>
    </section>
  );
}

interface LeakReportProps {
  leak: LeakCheck;
  t: Strings;
}

/** The two-part leak finding: the public-IP verdict and the DNS assessment.
 *  role="status" announces the result once when it lands; each row carries its
 *  tone via a data attribute the stylesheet keys off. */
function LeakReport({ leak, t }: LeakReportProps) {
  const ipBlockTone = ipTone(leak.ip_verdict);
  const dnsBlockTone = dnsTone(leak.dns.status);

  return (
    <div className="log-spec" role="status">
      <section className="log-spec-row" data-tone={ipBlockTone}>
        <header className="log-spec-head">
          <span className="log-eyebrow">{t.logs.leakIpHeading}</span>
          <span className={`log-verdict log-verdict--${ipBlockTone}`}>
            {leak.ip_verdict}
          </span>
        </header>
        <p className="log-spec-line">{ipHeadline(t, leak.ip_verdict)}</p>
        {leak.public_ip && (
          <dl className="log-facts">
            <div className="log-fact">
              <dt>{t.logs.egressIp}</dt>
              <dd className="selectable">
                {leak.public_ip}
                {leak.country ? ` (${leak.country})` : ""}
                {leak.source ? (
                  <span className="log-fact-note">
                    {" "}
                    {t.logs.leakSource.replace("{source}", leak.source)}
                  </span>
                ) : null}
              </dd>
            </div>
            {leak.connected && leak.exit_server && (
              <div className="log-fact">
                <dt>{t.logs.leakExitServer}</dt>
                <dd className="selectable">{leak.exit_server}</dd>
              </div>
            )}
          </dl>
        )}
      </section>

      <section className="log-spec-row" data-tone={dnsBlockTone}>
        <header className="log-spec-head">
          <span className="log-eyebrow">{t.logs.leakDnsHeading}</span>
          <span className={`log-verdict log-verdict--${dnsBlockTone}`}>
            {leak.dns.status}
          </span>
        </header>
        <p className="log-spec-line">{dnsHeadline(t, leak.dns.status)}</p>
        <p className="log-spec-detail">{leak.dns.message}</p>
        {leak.dns.resolvers && leak.dns.resolvers.length > 0 && (
          <dl className="log-facts">
            <div className="log-fact">
              <dt>{t.logs.leakDnsResolvers}</dt>
              <dd className="selectable">{leak.dns.resolvers.join(", ")}</dd>
            </div>
          </dl>
        )}
      </section>
    </div>
  );
}
