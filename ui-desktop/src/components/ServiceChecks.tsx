import type { ServiceCheck } from "../api";
import { useI18n } from "../i18n/I18nContext";

interface ServiceChecksProps {
  /** What the last check measured; empty before one has run. */
  checks: ServiceCheck[];
  /** True while a check is in flight. */
  checking: boolean;
}

/**
 * Three lines answering the only question a user has after pressing connect:
 * does video work, does voice work, are games still fast.
 *
 * It shows a latency for the ones that worked rather than a bare tick, because
 * "works" and "works at 240ms" are different answers for voice and games — and
 * the whole point of the routing split is the difference between them. A failed
 * check names its destination so the failure can be repeated by hand instead of
 * argued about.
 */
export function ServiceChecks({ checks, checking }: ServiceChecksProps) {
  const { t } = useI18n();
  if (!checking && checks.length === 0) return null;

  const label = (service: string) => {
    switch (service) {
      case "video":
        return t.checks.video;
      case "voice":
        return t.checks.voice;
      case "games":
        return t.checks.games;
      default:
        return service;
    }
  };

  return (
    <ul className="svc-checks" aria-busy={checking}>
      {checking && checks.length === 0 && (
        <li className="svc-check is-pending">
          <span className="svc-mark">…</span>
          <span className="svc-name">{t.checks.running}</span>
        </li>
      )}
      {checks.map((c) => (
        <li
          key={c.service}
          className={`svc-check ${c.ok ? "is-ok" : "is-bad"}`}
          title={c.detail}
        >
          <span className="svc-mark" aria-hidden="true">
            {c.ok ? "✓" : "✕"}
          </span>
          <span className="svc-name">{label(c.service)}</span>
          <span className="svc-rtt">
            {c.ok ? `${c.rttMs} ${t.units.ms}` : t.checks.failed}
          </span>
        </li>
      ))}
    </ul>
  );
}
