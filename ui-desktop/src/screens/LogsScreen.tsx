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
import { formatTime } from "../lib/format";

interface LogsScreenProps {
  tenebra: Tenebra;
}

/** Tone the result blocks are styled with. "good"/"bad" carry colour; "neutral"
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

  return (
    <section className="screen logs">
      <header className="screen-head">
        <h1>{t.logs.title}</h1>
        <div className="screen-head-actions">
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            disabled={checking}
            onClick={checkLeak}
          >
            {checking ? t.logs.checking : t.logs.leakCheck}
          </button>
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            disabled={logs.length === 0}
            onClick={tenebra.clearLogs}
          >
            {t.logs.clear}
          </button>
        </div>
      </header>

      {leak && <LeakReport leak={leak} t={t} />}

      <div className="log-panel" role="log" aria-live="polite" ref={panelRef}>
        {logs.length === 0 ? (
          <p className="log-empty muted">{t.logs.empty}</p>
        ) : (
          logs.map((line) => (
            <div key={line.id} className="log-line">
              <span className="log-time muted">{formatTime(line.at, lang)}</span>
              <span className={`log-level log-${line.level}`}>{line.level}</span>
              <span className="log-msg">{line.msg}</span>
            </div>
          ))
        )}
      </div>
    </section>
  );
}

interface LeakReportProps {
  leak: LeakCheck;
  t: Strings;
}

/** The two-part leak finding: the public-IP verdict and the DNS assessment.
 *  role="status" announces the result once when it lands; each block carries its
 *  tone via a data attribute the stylesheet keys off. */
function LeakReport({ leak, t }: LeakReportProps) {
  const ipBlockTone = ipTone(leak.ip_verdict);
  const dnsBlockTone = dnsTone(leak.dns.status);

  return (
    <div className="leak-report" role="status">
      <section className="leak-block" data-tone={ipBlockTone}>
        <header className="leak-block-head">
          <h2 className="leak-block-title">{t.logs.leakIpHeading}</h2>
          <span className={`leak-tag leak-tag--${ipBlockTone}`}>
            {leak.ip_verdict}
          </span>
        </header>
        <p className="leak-headline">{ipHeadline(t, leak.ip_verdict)}</p>
        {leak.public_ip && (
          <dl className="leak-facts">
            <div className="leak-fact">
              <dt>{t.logs.egressIp}</dt>
              <dd className="leak-mono">
                {leak.public_ip}
                {leak.country ? ` (${leak.country})` : ""}
                {leak.source ? (
                  <span className="leak-source muted">
                    {" "}
                    {t.logs.leakSource.replace("{source}", leak.source)}
                  </span>
                ) : null}
              </dd>
            </div>
            {leak.connected && leak.exit_server && (
              <div className="leak-fact">
                <dt>{t.logs.leakExitServer}</dt>
                <dd className="leak-mono">{leak.exit_server}</dd>
              </div>
            )}
          </dl>
        )}
      </section>

      <section className="leak-block" data-tone={dnsBlockTone}>
        <header className="leak-block-head">
          <h2 className="leak-block-title">{t.logs.leakDnsHeading}</h2>
          <span className={`leak-tag leak-tag--${dnsBlockTone}`}>
            {leak.dns.status}
          </span>
        </header>
        <p className="leak-headline">{dnsHeadline(t, leak.dns.status)}</p>
        <p className="leak-detail muted">{leak.dns.message}</p>
        {leak.dns.resolvers && leak.dns.resolvers.length > 0 && (
          <dl className="leak-facts">
            <div className="leak-fact">
              <dt>{t.logs.leakDnsResolvers}</dt>
              <dd className="leak-mono">{leak.dns.resolvers.join(", ")}</dd>
            </div>
          </dl>
        )}
      </section>
    </div>
  );
}
