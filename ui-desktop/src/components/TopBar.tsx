import { useEffect, useRef, useState } from "react";

import type { Profile } from "../api";
import { useI18n } from "../i18n/I18nContext";
import {
  formatExpiry,
  formatTrafficUsage,
  trafficUsedFraction,
} from "../lib/format";
import { CreditsToast } from "./CreditsToast";

// Injected at build time from package.json (vite/vitest `define`), which
// scripts/set-version.mjs keeps in lockstep with the rest of the release.
const VERSION = `v${__APP_VERSION__}`;

// Two quiet rewards, each a short run of taps within the window below. Three taps
// on the wordmark play the eclipse; three on the version badge reveal the credits.
// Distinct targets, so neither run ever spends the other. A plain click is
// discoverable by accident where a key chord never was.
const ECLIPSE_TAPS = 3;
const CREDITS_TAPS = 3;
const TAP_WINDOW_MS = 1200;

interface TopBarProps {
  /** The connected (or otherwise selected) subscription, for the meta line. */
  activeProfile: Profile | null;
  /** Fired when the wordmark is tapped {@link ECLIPSE_TAPS} times — plays the eclipse. */
  onEclipse: () => void;
}

/**
 * Top bar: the Tenebra wordmark on the left, and the active subscription's own
 * data on the right — traffic left and expiry, pulled from the subscription
 * user-info (Tenebra has no account/plan/device model, so the meta reflects the
 * subscription itself). Falls back to a quiet "no subscription".
 */
export function TopBar({ activeProfile, onEclipse }: TopBarProps) {
  const { t, lang } = useI18n();

  // Two hidden tap counters, one per egg. Count and reset timer ride refs (no
  // re-render per tap); only the credits reveal flips state.
  const eclipseTaps = useRef(0);
  const eclipseTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const creditTaps = useRef(0);
  const creditTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [credits, setCredits] = useState(false);

  useEffect(
    () => () => {
      if (eclipseTimer.current) {
        clearTimeout(eclipseTimer.current);
      }
      if (creditTimer.current) {
        clearTimeout(creditTimer.current);
      }
    },
    [],
  );

  // Three taps on the wordmark, within the window, play the eclipse; a stalled
  // run resets.
  const handleWordmarkClick = () => {
    if (eclipseTimer.current) {
      clearTimeout(eclipseTimer.current);
      eclipseTimer.current = null;
    }
    eclipseTaps.current += 1;
    if (eclipseTaps.current >= ECLIPSE_TAPS) {
      eclipseTaps.current = 0;
      onEclipse();
      return;
    }
    eclipseTimer.current = setTimeout(() => {
      eclipseTaps.current = 0;
    }, TAP_WINDOW_MS);
  };

  // Three taps on the version badge reveal the credits card.
  const handleVersionClick = () => {
    if (creditTimer.current) {
      clearTimeout(creditTimer.current);
      creditTimer.current = null;
    }
    creditTaps.current += 1;
    if (creditTaps.current >= CREDITS_TAPS) {
      creditTaps.current = 0;
      setCredits(true);
      return;
    }
    creditTimer.current = setTimeout(() => {
      creditTaps.current = 0;
    }, TAP_WINDOW_MS);
  };

  const usage = activeProfile
    ? formatTrafficUsage(activeProfile.trafficUsed, activeProfile.trafficTotal)
    : null;
  // A metered plan gets a slim consumption meter beside the usage text; unmetered
  // (or usage the header omits) yields null and no bar.
  const usedFraction = activeProfile
    ? trafficUsedFraction(activeProfile.trafficUsed, activeProfile.trafficTotal)
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
    <>
      <div className="app-top">
        <div className="brand">
          <span className="bracket">[</span>
          <span className="mark" onClick={handleWordmarkClick}>
            Tenebra
          </span>
          <span className="bracket">]</span>
          <span className="ver" onClick={handleVersionClick}>
            {VERSION}
          </span>
        </div>
        <div className="app-acct">
          {activeProfile ? (
            <>
              <span className="b">{activeProfile.name}</span>
              {activeProfile.tier === "premium" && (
                <span className="acct-badge acct-badge--premium">
                  {t.topbar.premium}
                </span>
              )}
              {usage && (
                <>
                  <span className="sep" aria-hidden="true">
                    ·
                  </span>
                  <span className="tnum">{usage}</span>
                  {usedFraction !== null && (
                    <svg
                      className="usage-bar"
                      width="64"
                      height="3"
                      viewBox="0 0 64 3"
                      aria-hidden="true"
                    >
                      <rect width="64" height="3" fill="var(--line)" />
                      <rect
                        width={(usedFraction * 64).toFixed(1)}
                        height="3"
                        fill="var(--text-dim)"
                      />
                    </svg>
                  )}
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
      {credits && <CreditsToast onDismiss={() => setCredits(false)} />}
    </>
  );
}
