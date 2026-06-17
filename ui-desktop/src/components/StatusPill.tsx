import type { ConnectionState } from "../api";
import { useI18n } from "../i18n/I18nContext";

export function StatusPill({ state }: { state: ConnectionState }) {
  const { t } = useI18n();
  return (
    <span className={`status-pill status-pill--${state}`}>
      <span className="status-dot" aria-hidden="true" />
      {t.state[state]}
    </span>
  );
}
