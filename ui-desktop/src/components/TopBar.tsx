import type { Profile } from "../api";
import { useI18n } from "../i18n/I18nContext";
import { formatExpiry, formatTrafficUsage } from "../lib/format";

// Bumped per release; mirrors tauri.conf.json / package.json. A short build mark
// could be injected at build time later — kept literal for now.
const VERSION = "v0.1.1";

interface TopBarProps {
  /** The connected (or otherwise selected) subscription, for the meta line. */
  activeProfile: Profile | null;
}

/**
 * Top bar: the Tenebra wordmark on the left, and the active subscription's own
 * data on the right — traffic left and expiry, pulled from the subscription
 * user-info (Tenebra has no account/plan/device model, so the meta reflects the
 * subscription itself). Falls back to a quiet "no subscription".
 */
export function TopBar({ activeProfile }: TopBarProps) {
  const { t, lang } = useI18n();

  const usage = activeProfile
    ? formatTrafficUsage(activeProfile.trafficUsed, activeProfile.trafficTotal)
    : null;
  const expiry = activeProfile
    ? formatExpiry(activeProfile.expiresAt, lang, {
        in: t.profiles.expiresIn,
        today: t.profiles.expiresToday,
        tomorrow: t.profiles.expiresTomorrow,
        expired: t.profiles.expired,
      })
    : null;

  return (
    <div className="app-top">
      <div className="brand">
        <span className="bracket">[</span>
        <span className="mark">Tenebra</span>
        <span className="bracket">]</span>
        <span className="ver">{VERSION}</span>
      </div>
      <div className="app-acct">
        {activeProfile ? (
          <>
            <span className="b">{activeProfile.name}</span>
            {usage && (
              <>
                <span className="sep" aria-hidden="true">
                  ·
                </span>
                <span className="tnum">{usage}</span>
              </>
            )}
            {expiry && (
              <>
                <span className="sep" aria-hidden="true">
                  ·
                </span>
                <span>{expiry}</span>
              </>
            )}
          </>
        ) : (
          <span>{t.topbar.noSubscription}</span>
        )}
      </div>
    </div>
  );
}
