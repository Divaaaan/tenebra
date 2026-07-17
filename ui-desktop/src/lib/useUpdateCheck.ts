// Launch-time update check. Runs once when the app mounts: quietly asks the
// updater whether a newer signed release exists, then either surfaces it as a
// dismissible banner or — when the auto-install preference is on — installs it
// right away (installUpdate relaunches the app on success). A failed check
// stays silent: an offline launch must look exactly like an up-to-date one.
//
// The one hard rule layered on top: never tear down a live tunnel to update.
// installUpdate relaunches the process (and on Windows the updater stops the
// background service), so applying it mid-session drops the VPN. So an install
// is gated on the tunnel state — auto-install only while the tunnel is down, and
// a manual install while it is up asks first. See tunnelBusy.

import { useCallback, useEffect, useRef, useState } from "react";
import type { Update } from "@tauri-apps/plugin-updater";

import type { ConnectionState } from "../api";
import { checkForUpdate, installUpdate } from "./updates";
import { getAutoInstallUpdates } from "./settings";
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
  /** Primary install action: installs, or asks first when the tunnel is up. */
  install: () => void;
  /** Approve the pending confirm and install now, dropping the live tunnel. */
  confirmInstall: () => void;
  /** Dismiss the confirm without installing. */
  cancelInstall: () => void;
  /** Hide the banner for this run only; the next launch will offer it again. */
  dismiss: () => void;
}

export function useUpdateCheck(phase: ConnectionState): UpdatePrompt {
  const [update, setUpdate] = useState<Update | null>(null);
  const [dismissed, setDismissed] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);
  const [deferred, setDeferred] = useState(false);
  const [confirming, setConfirming] = useState(false);

  // The current phase, kept on a ref so the once-only launch effect and the
  // install callbacks read it fresh without re-subscribing on every transition.
  const phaseRef = useRef(phase);
  phaseRef.current = phase;

  // Kick off the download → install → relaunch. Shared by every route into an
  // install: the silent auto path, the deferred auto-fire, and both manual
  // paths. It clears the two "waiting" flags so the banner drops straight into
  // its downloading state.
  const runInstall = useCallback((target: Update) => {
    setConfirming(false);
    setDeferred(false);
    setInstalling(true);
    setProgress(null);
    void (async () => {
      try {
        // Relaunches on success, so this only returns on failure.
        await installUpdate(target, setProgress);
      } catch {
        // Keep the banner and unlock the action so it can be retried.
        setInstalling(false);
        setProgress(null);
      }
    })();
  }, []);

  // Fires at most once per run: the ref survives StrictMode's double effect
  // invocation, so a dev build cannot kick off two checks (or two installs).
  const checked = useRef(false);
  useEffect(() => {
    if (checked.current) {
      return;
    }
    checked.current = true;
    void (async () => {
      let found: Update | null;
      try {
        found = await checkForUpdate();
      } catch {
        // Offline, or the release host is unreachable. The launch check is
        // opportunistic and must never surface an error.
        return;
      }
      if (!found) {
        return;
      }
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
    })();
  }, []);

  // The deferred auto-install: once armed, apply it the moment the tunnel is no
  // longer carrying traffic. The ref guard fires it exactly once — including
  // across StrictMode's replay — and a manual install (which clears `deferred`)
  // stands it down.
  const autoFired = useRef(false);
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

  return {
    available: update && !dismissed ? update.version : null,
    installing,
    progress,
    deferred,
    confirming,
    install,
    confirmInstall,
    cancelInstall,
    dismiss,
  };
}
