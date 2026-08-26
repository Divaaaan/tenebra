import { useI18n } from "../i18n/I18nContext";

interface ReportNudgeProps {
  /** Open the report-a-problem flow. */
  onReport: () => void;
  /** Put the prompt away. */
  onDismiss: () => void;
}

/**
 * The one prompt the app raises by itself: the post-connect check has said twice
 * running that video isn't getting through, so it offers the report flow instead
 * of waiting for the user to go looking for it.
 *
 * It names the failure rather than asking "is something wrong?" — the user
 * already knows something is wrong, and a prompt that repeats their own question
 * back at them reads as the app not knowing either. Nothing is collected or sent
 * by this being on screen; the button starts the same two-click flow every other
 * entry point does.
 */
export function ReportNudge({ onReport, onDismiss }: ReportNudgeProps) {
  const { t } = useI18n();
  return (
    <div className="report-nudge" role="status">
      <span className="report-nudge-text">⚠ {t.report.nudgeText}</span>
      <div className="report-nudge-actions">
        <button type="button" className="report-nudge-primary" onClick={onReport}>
          {t.report.action}
        </button>
        <button type="button" onClick={onDismiss}>
          {t.report.nudgeDismiss}
        </button>
      </div>
    </div>
  );
}
