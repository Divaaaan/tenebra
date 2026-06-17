import type { PingResult } from "../api";
import { useI18n } from "../i18n/I18nContext";
import { pingQuality } from "../lib/ping";

export function PingBadge({ result }: { result?: PingResult }) {
  const { t } = useI18n();

  if (!result) {
    return <span className="ping-badge ping-badge--empty">—</span>;
  }

  if (!result.ok) {
    return (
      <span
        className="ping-badge ping-badge--down"
        role="img"
        aria-label={t.state.error}
        title={t.state.error}
      >
        ×
      </span>
    );
  }

  return (
    <span className={`ping-badge ping-${pingQuality(result.rttMs)}`}>
      {result.rttMs}
      {t.units.ms}
    </span>
  );
}
