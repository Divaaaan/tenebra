import type { NodeCheckResult } from "../api";
import { useI18n } from "../i18n/I18nContext";
import { dominantStage, isUsable, medianRtt, okCount } from "../lib/nodecheck";

interface ProbeBadgeProps {
  /** Probe outcome for this node; absent while it has not been checked yet. */
  result?: NodeCheckResult;
  /** True while this node's probe is in flight. */
  probing?: boolean;
  /** True on the node auto-selection picked. */
  best?: boolean;
}

/**
 * The per-node verdict of an end-to-end check.
 *
 * It deliberately shows more than a ping badge does. A node that accepts TCP and
 * then never handshakes looks *fastest* to a ping — that is the failure this
 * whole path exists to surface — so the badge names the stage that broke
 * (`no answer` / `handshake failed` / `no traffic`) instead of a red dot, and
 * shows coverage (how many control targets survived) next to the latency,
 * because a node carrying 4/5 destinations is a different thing from one
 * carrying 1/5 slightly faster.
 */
export function ProbeBadge({ result, probing, best }: ProbeBadgeProps) {
  const { t } = useI18n();

  if (probing) {
    return (
      <span className="probe-badge probe-badge--probing" aria-label={t.check.probing}>
        <span className="probe-badge__scan" aria-hidden="true" />
        {t.check.probing}
      </span>
    );
  }

  if (!result) {
    return <span className="probe-badge probe-badge--empty">—</span>;
  }

  const stage = dominantStage(result);

  if (!isUsable(result)) {
    const label = {
      dial: t.check.stageDial,
      handshake: t.check.stageHandshake,
      probe: t.check.stageProbe,
      ok: t.check.stageProbe,
    }[stage];
    return (
      <span className={`probe-badge probe-badge--fail probe-badge--${stage}`} title={label}>
        {label}
      </span>
    );
  }

  const rtt = medianRtt(result);
  return (
    <span
      className={`probe-badge probe-badge--ok${best ? " probe-badge--best" : ""}`}
      title={`${okCount(result)}/${result.targets.length} ${t.check.coverage}`}
    >
      {best && <span className="probe-badge__best">{t.check.best}</span>}
      {rtt !== null && (
        <span className="probe-badge__rtt">
          {rtt}
          {t.units.ms}
        </span>
      )}
      <span className="probe-badge__coverage">
        {okCount(result)}/{result.targets.length}
      </span>
    </span>
  );
}
