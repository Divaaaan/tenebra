// The update check. Asks the updater whether a newer signed release exists,
// then either surfaces it as a dismissible banner or — when the auto-install
// preference is on — installs it right away (installUpdate relaunches the app
// on success). A failed check stays silent the first time round: an offline
// launch must look exactly like an up-to-date one.
//
// It runs on a schedule rather than once at mount. A client that lives in the
// tray mounts this hook exactly once and then keeps the same webview for days,
// so a launch-only check was, for the people who never open the window, a
// check that ran when they installed the app and never again — on a product
// that shipped five patches in a day, none of them arrived. The heartbeat is
// short and the decision is made against the wall clock, because neither a
// suspended machine nor a hidden WebView2 window keeps a long timer honest;
// see lib/updateSchedule for that reasoning and lib/settings for where the
// timestamp lives.
//
// The one hard rule layered on top: never tear down a live tunnel to update.
// installUpdate relaunches the process (and on Windows the updater stops the
// background service), so applying it mid-session drops the VPN. So an install
// is gated on the tunnel state — auto-install only while the tunnel is down, and
// a manual install while it is up asks first. See tunnelBusy.
//
// The check is skipped entirely on an install that cannot replace itself (a
// Linux package manager owns those files, see inAppUpdatesSupported): a banner
// whose only action would fail is worse than no banner, and Settings still says
// where updates come from.

import { useCallback, useEffect, useRef, useState } from "react";
import type { Update } from "@tauri-apps/plugin-updater";

import type { ConnectionState } from "../api";
import {
  checkForUpdate,
  inAppUpdatesSupported,
  installUpdate,
  notifyUpdateAvailable,
} from "./updates";
import {
  getAutoInstallUpdates,
  getUpdateFailures,
  getUpdateLastCheck,
  setUpdateFailures,
  setUpdateLastCheck,
} from "./settings";
import {
  UPDATE_CHECK_INTERVAL_MS,
  UPDATE_PULSE_MS,
  isCheckDue,
  isCheckStalled,
} from "./updateSchedule";
import { tunnelBusy } from "./tunnel";

export interface UpdatePrompt {
  /** Version to offer in the banner, or null when there is nothing to show. */
  available: string | null;
  /** True while the banner's install action is downloading and applying. */
  installing: boolean;
  /** Integer download percent, or null while the size is unknown. */
  progress: number | null;
  /**
   * True when an auto-install is armed but held back because the tunnel is up:
   * it applies on its own once the tunnel goes down. Drives the banner's
   * "installs after you disconnect" copy.
   */
  deferred: boolean;
  /**
   * True when a manual install is waiting on the "this drops your VPN" confirm —
   * a tunnel is up and the user asked to install now. Drives the confirm dialog.
   */
  confirming: boolean;
  /**
   * True when enough checks have failed back to back to stop calling it
   * weather: this copy has had no answer from the release host in the best part
   * of a day and may be sitting on a version that has been superseded twice.
   * Drives the quiet "couldn't check" strip.
   */
  stalled: boolean;
  /** Primary install action: installs, or asks first when the tunnel is up. */
  install: () => void;
  /** Approve the pending confirm and install now, dropping the live tunnel. */
  confirmInstall: () => void;
  /** Dismiss the confirm without installing. */
  cancelInstall: () => void;
  /** Hide the banner for this run only; the next launch will offer it again. */
  dismiss: () => void;
  /** Check right now, whatever the schedule says. The stalled strip's action. */
  checkNow: () => void;
  /** Hide the stalled strip for this run. The count keeps rising underneath. */
  dismissStalled: () => void;
}

export function useUpdateCheck(phase: ConnectionState): UpdatePrompt {
  const [update, setUpdate] = useState<Update | null>(null);
  const [dismissed, setDismissed] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);
  const [deferred, setDeferred] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [checking, setChecking] = useState(false);
  // Seeded from storage: the run of failures is the property of the install,
  // not of this process — see getUpdateFailures.
  const [failures, setFailures] = useState(getUpdateFailures);
  const [stalledDismissed, setStalledDismissed] = useState(false);

  // The current phase, kept on a ref so the scheduled check and the install
  // callbacks read it fresh without re-subscribing on every transition.
  const phaseRef = useRef(phase);
  phaseRef.current = phase;

  // Kick off the download → install → relaunch. Shared by every route into an
  // install: the silent auto path, the deferred auto-fire, and both manual
  // paths. It clears the two "waiting" flags so the banner drops straight into
  // its downloading state.
  const installingRef = useRef(false);
  const runInstall = useCallback((target: Update) => {
    setConfirming(false);
    setDeferred(false);
    setInstalling(true);
    installingRef.current = true;
    setProgress(null);
    void (async () => {
      try {
        // Relaunches on success, so this only returns on failure.
        await installUpdate(target, setProgress);
      } catch {
        // Keep the banner and unlock the action so it can be retried.
        setInstalling(false);
        installingRef.current = false;
        setProgress(null);
      }
    })();
  }, []);

  // Whether this install can replace itself, asked once and kept: the answer is
  // a property of how the app was installed, so re-asking the backend on every
  // beat would be 96 round trips a day to hear the same thing.
  const selfUpdating = useRef<boolean | null>(null);
  // Held for the length of an attempt. Set before the first await, so it also
  // absorbs StrictMode's replayed mount effect, which would otherwise start a
  // second check alongside the first.
  const inFlight = useRef(false);
  // The release this run has already acted on. It keeps a later check from
  // re-offering something the user put off, from replacing the handle an
  // install is running against — and it is what makes the desktop notification
  // fire once per release rather than once per beat.
  const offered = useRef<string | null>(null);
  // Whether the armed deferred install has already gone off. Per release, not
  // per run: it exists to survive StrictMode's replay of the effect below, and
  // latching it for the whole session would leave a second release sitting
  // behind an "installs after you disconnect" that never came.
  const autoFired = useRef(false);

  const runCheck = useCallback(async (force: boolean) => {
    if (inFlight.current) {
      return;
    }
    if (
      !force &&
      !isCheckDue(Date.now(), getUpdateLastCheck(), UPDATE_CHECK_INTERVAL_MS)
    ) {
      return;
    }
    inFlight.current = true;
    setChecking(true);
    try {
      if (selfUpdating.current === null) {
        selfUpdating.current = await inAppUpdatesSupported();
      }
      if (!selfUpdating.current) {
        // Nothing was checked, so nothing is stamped: the timestamp answers
        // "when did we last ask the release host", and here we never do.
        return;
      }
      // Stamp the attempt as it starts rather than when it answers. A check
      // that never comes back — a hung socket on a captive-portal network —
      // would otherwise leave the client permanently due and fire a fresh one
      // on every beat.
      setUpdateLastCheck(Date.now());
      let found: Update | null;
      try {
        found = await checkForUpdate();
      } catch {
        // Offline, or the release host is unreachable. One of these is nothing;
        // a run of them is worth saying out loud, which is what the counter is
        // for. Read through storage rather than the state so a check that
        // started before the last one's setState landed still counts.
        const run = getUpdateFailures() + 1;
        setUpdateFailures(run);
        setFailures(run);
        return;
      }
      setUpdateFailures(0);
      setFailures(0);
      if (!found) {
        return;
      }
      if (installingRef.current || found.version === offered.current) {
        return;
      }
      offered.current = found.version;
      // A newer release than the one that was put off: re-open the banner, and
      // re-arm the deferred install for it.
      setDismissed(false);
      autoFired.current = false;
      if (getAutoInstallUpdates()) {
        if (!tunnelBusy(phaseRef.current)) {
          try {
            // Nothing is riding the tunnel — apply it silently and relaunch.
            await installUpdate(found);
            return;
          } catch {
            // The silent install failed; fall back to the banner so the update
            // stays discoverable (and retryable) by hand.
          }
        } else {
          // A tunnel is up: don't relaunch out from under it. Surface the banner
          // in its deferred state and let the phase effect install once the
          // tunnel drops.
          setUpdate(found);
          setDeferred(true);
          return;
        }
      }
      setUpdate(found);
      // The banner is now the only way this release gets applied, and on a
      // window minimised to the tray it is drawn where nobody will see it. Say
      // it where the user is instead. A window that is open needs no toast —
      // the strip is right there.
      if (document.visibilityState === "hidden") {
        void notifyUpdateAvailable(found.version);
      }
    } finally {
      inFlight.current = false;
      setChecking(false);
    }
  }, []);

  // The heartbeat. Deliberately not one timer set to the check interval: a
  // machine that suspends loses every timer it holds for the length of the
  // sleep, and a hidden WebView2 window has its background timers coalesced
  // into minute buckets. Each beat only compares the clock against the stored
  // timestamp, so a client that wakes up overdue checks on the next beat.
  useEffect(() => {
    void runCheck(false);
    const beat = setInterval(() => void runCheck(false), UPDATE_PULSE_MS);
    return () => clearInterval(beat);
  }, [runCheck]);

  // The deferred auto-install: once armed, apply it the moment the tunnel is no
  // longer carrying traffic. The ref guard fires it exactly once per release —
  // including across StrictMode's replay — and a manual install (which clears
  // `deferred`) stands it down.
  useEffect(() => {
    if (!deferred || !update || autoFired.current) {
      return;
    }
    if (tunnelBusy(phase)) {
      return;
    }
    autoFired.current = true;
    runInstall(update);
  }, [deferred, update, phase, runInstall]);

  const install = useCallback(() => {
    if (!update || installing) {
      return;
    }
    // A live tunnel: installing relaunches the app and drops the VPN, so get an
    // explicit go-ahead first rather than cutting the connection silently.
    if (tunnelBusy(phaseRef.current)) {
      setConfirming(true);
      return;
    }
    runInstall(update);
  }, [update, installing, runInstall]);

  const confirmInstall = useCallback(() => {
    if (!update || installing) {
      return;
    }
    runInstall(update);
  }, [update, installing, runInstall]);

  const cancelInstall = useCallback(() => setConfirming(false), []);

  // "Later": hide the banner for this run. It also stands down a pending
  // deferred auto-install — an explicit dismiss shouldn't come back as a
  // surprise relaunch when the tunnel later drops; the next launch re-offers it.
  const dismiss = useCallback(() => {
    setDismissed(true);
    setDeferred(false);
  }, []);

  const checkNow = useCallback(() => void runCheck(true), [runCheck]);

  const dismissStalled = useCallback(() => setStalledDismissed(true), []);

  return {
    available: update && !dismissed ? update.version : null,
    installing,
    progress,
    deferred,
    confirming,
    // Held back while a check is actually running: a client coming back from a
    // bad night would otherwise show "couldn't check" for the second it takes
    // the check it is already running to answer.
    stalled: isCheckStalled(failures) && !checking && !stalledDismissed,
    install,
    confirmInstall,
    cancelInstall,
    dismiss,
    checkNow,
    dismissStalled,
  };
}
