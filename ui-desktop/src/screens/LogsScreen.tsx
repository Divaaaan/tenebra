import { useEffect, useRef, useState } from "react";

import { api, type LeakCheck } from "../api";
import type { Tenebra } from "../state/useTenebra";
import { useI18n } from "../i18n/I18nContext";
import { formatTime } from "../lib/format";

interface LogsScreenProps {
  tenebra: Tenebra;
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

      {leak && (
        <div
          className={`leak-result${leak.tunneled ? " is-ok" : " is-bad"}`}
          role="status"
        >
          <p className="leak-headline">
            {leak.tunneled ? t.logs.leakResultTunneled : t.logs.leakResultExposed}
          </p>
          <p className="leak-detail muted">
            {t.logs.egressIp}: {leak.ip} ({leak.country})
          </p>
        </div>
      )}

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
