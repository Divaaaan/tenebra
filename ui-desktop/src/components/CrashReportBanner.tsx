import { useI18n } from "../i18n/I18nContext";

interface CrashReportBannerProps {
  onView: () => void;
  onCreateIssue: () => void;
  onDismiss: () => void;
}

/**
 * "Tenebra crashed last time" banner, shown when the user has opted in and a
 * crash file from a previous run is present and not yet acknowledged. Offers to
 * review the report, open a pre-filled GitHub issue, or dismiss it — nothing is
 * sent without one of those explicit clicks.
 */
export function CrashReportBanner({
  onView,
  onCreateIssue,
  onDismiss,
}: CrashReportBannerProps) {
  const { t } = useI18n();
  return (
    <div className="crash-banner" role="status">
      <span className="crash-banner-text">{t.crash.detectedTitle}</span>
      <div className="crash-banner-actions">
        <button type="button" className="crash-banner-primary" onClick={onView}>
          {t.crash.detectedView}
        </button>
        <button type="button" onClick={onCreateIssue}>
          {t.crash.detectedIssue}
        </button>
        <button type="button" onClick={onDismiss}>
          {t.crash.detectedDismiss}
        </button>
      </div>
    </div>
  );
}
