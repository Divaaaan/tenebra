import type { Profile } from "../api";
import { useI18n } from "../i18n/I18nContext";
import { formatExpiry, formatTrafficUsage } from "../lib/format";

// Injected at build time from package.json (vite/vitest `define`), which
// scripts/set-version.mjs keeps in lockstep with the rest of the release.
const VERSION = `v${__APP_VERSION__}`;

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
