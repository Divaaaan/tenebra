// Launch-time update check. Runs once when the app mounts: quietly asks the
// updater whether a newer signed release exists, then either surfaces it as a
// dismissible banner or — when the auto-install preference is on — installs it
// right away (installUpdate relaunches the app on success). A failed check
// stays silent: an offline launch must look exactly like an up-to-date one.

import { useCallback, useEffect, useRef, useState } from "react";
import type { Update } from "@tauri-apps/plugin-updater";

import { checkForUpdate, installUpdate } from "./updates";
import { getAutoInstallUpdates } from "./settings";

export interface UpdatePrompt {
  /** Version to offer in the banner, or null when there is nothing to show. */
  available: string | null;
  /** True while the banner's install action is downloading and applying. */
  installing: boolean;
  /** Integer download percent, or null while the size is unknown. */
  progress: number | null;
  install: () => void;
  /** Hide the banner for this run only; the next launch will offer it again. */
  dismiss: () => void;
}

export function useUpdateCheck(): UpdatePrompt {
  const [update, setUpdate] = useState<Update | null>(null);
  const [dismissed, setDismissed] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);

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
        try {
          // Relaunches on success, so this only falls through on failure.
          await installUpdate(found);
          return;
        } catch {
          // The silent install failed; fall back to the banner so the update
          // stays discoverable (and retryable) by hand.
        }
      }
      setUpdate(found);
    })();
  }, []);

  const install = useCallback(() => {
    if (!update || installing) {
      return;
    }
    setInstalling(true);
    setProgress(null);
    void (async () => {
      try {
        // Relaunches on success, so there is nothing to reset afterwards.
        await installUpdate(update, setProgress);
      } catch {
        // Keep the banner and unlock the action so it can be retried.
        setInstalling(false);
        setProgress(null);
      }
    })();
  }, [update, installing]);

  const dismiss = useCallback(() => setDismissed(true), []);

  return {
    available: update && !dismissed ? update.version : null,
    installing,
    progress,
    install,
    dismiss,
  };
}
