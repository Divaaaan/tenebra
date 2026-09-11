import { useI18n } from "../i18n/I18nContext";
import { describeCoreError } from "../i18n/strings";

export function ConnectionError({ error, onReport }: { error: string; onReport: () => void }) {
  const { t } = useI18n();
  const lower = error.toLowerCase();
  const explanation = lower.includes("all protocols failed") || lower.includes("handshake")
    ? t.errors.protocolFailed
    : /\b(profile|node)\b.*(not found|missing|no longer)|no (usable )?nodes/.test(lower)
      ? t.errors.selectionFailed
      : /\bpipe\b|\bipc\b|tenebra[- ](core|service)|background service|core.*(down|unreachable)/.test(lower)
        ? t.errors.serviceFailed
        : describeCoreError(error, t) !== t.daemon.commandFailed
          ? describeCoreError(error, t) : t.errors.connectFailed;
  return <div className="connection-error" role="alert">
    <p>{explanation}</p>
    <details><summary>{t.errors.details}</summary><pre className="selectable">{error}</pre></details>
    <button className="prof-ghost" onClick={onReport}>{t.report.title}</button>
  </div>;
}
