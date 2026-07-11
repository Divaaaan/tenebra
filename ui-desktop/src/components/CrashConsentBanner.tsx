import { useI18n } from "../i18n/I18nContext";

interface CrashConsentBannerProps {
  onEnable: () => void;
  onDecline: () => void;
}

/**
 * First-run consent for crash reporting, shown once until the user answers. A
 * banner, not a modal: it informs and offers but never blocks. Modeled on the
 * update banner, but left in normal case since the copy is a sentence. Both
 * choices are explicit — there is no default-on.
 */
export function CrashConsentBanner({
  onEnable,
  onDecline,
}: CrashConsentBannerProps) {
  const { t } = useI18n();
  return (
    <div className="crash-banner" role="status">
      <span className="crash-banner-text">{t.crash.consentText}</span>
      <div className="crash-banner-actions">
        <button
          type="button"
          className="crash-banner-primary"
          onClick={onEnable}
        >
          {t.crash.consentEnable}
        </button>
        <button type="button" onClick={onDecline}>
          {t.crash.consentNoThanks}
        </button>
      </div>
    </div>
  );
}
