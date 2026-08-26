import { useI18n } from "../i18n/I18nContext";

interface UpdateStaleBannerProps {
  /** Check again right now, regardless of where the schedule stands. */
  onCheckNow: () => void;
  onDismiss: () => void;
}

// One-line strip under the top bar for the other half of the update story: not
// "a new release is out" but "we have not been able to find out for the best
// part of a day". Same classes as the update and daemon-skew strips — same
// severity, same row, no visual language of its own.
//
// It appears only after a run of failures (see lib/updateSchedule): a single
// failed check is a closed lid or a train tunnel and says nothing, and the
// update flow is deliberately silent about it. What is worth saying is that
// this copy has stopped hearing from the release host altogether, because the
// user cannot tell that from the inside — a client that never finds an update
// looks exactly like a client that is up to date.
export function UpdateStaleBanner({
  onCheckNow,
  onDismiss,
}: UpdateStaleBannerProps) {
  const { t } = useI18n();

  return (
    <div className="update-banner" role="status">
      <span className="update-banner-text">⚠ {t.update.stalled}</span>
      <div className="update-banner-actions">
        <button
          type="button"
          className="update-banner-install"
          onClick={onCheckNow}
        >
          ▶ {t.update.checkNow}
        </button>
        <button type="button" onClick={onDismiss}>
          ✕ {t.update.hide}
        </button>
      </div>
    </div>
  );
}
