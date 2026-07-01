// Auto-update flow, kept out of the Settings screen so the component stays a
// thin view over these two calls. The updater checks a signed `latest.json`
// published to GitHub Releases (see tauri.conf.json → plugins.updater); the
// process plugin performs the relaunch after an install.

import { check, type Update } from "@tauri-apps/plugin-updater";
import { relaunch } from "@tauri-apps/plugin-process";

/** What the Settings updater row is currently showing. */
export type UpdateStatus =
  | { kind: "idle" }
  | { kind: "checking" }
  | { kind: "uptodate" }
  | { kind: "available"; version: string }
  | { kind: "downloading"; percent: number | null }
  | { kind: "installing" }
  | { kind: "error"; message: string };

/** Ask GitHub whether a newer signed release exists. Returns the `Update`
 *  handle when one is available (the caller then downloads + installs it), or
 *  `null` when we're already on the latest version. */
export async function checkForUpdate(): Promise<Update | null> {
  return check();
}

/** Download the pending update — reporting integer percent when the server
 *  sends a content length, otherwise `null` — install it, and relaunch. The
 *  process exits during `relaunch`, so on success this never returns. */
export async function installUpdate(
  update: Update,
  onProgress?: (percent: number | null) => void,
): Promise<void> {
  let total = 0;
  let received = 0;
  await update.downloadAndInstall((event) => {
    switch (event.event) {
      case "Started":
        total = event.data.contentLength ?? 0;
        onProgress?.(total > 0 ? 0 : null);
        break;
      case "Progress":
        received += event.data.chunkLength;
        onProgress?.(total > 0 ? Math.round((received / total) * 100) : null);
        break;
      case "Finished":
        onProgress?.(100);
        break;
    }
  });
  await relaunch();
}
