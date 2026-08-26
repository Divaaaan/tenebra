// Auto-update flow, kept out of the Settings screen so the component stays a
// thin view over these two calls. The updater checks a signed manifest published
// to GitHub Releases (see tauri.conf.json → plugins.updater); the process plugin
// performs the relaunch after an install. Which manifest is checked depends on
// the user's release channel — see checkForUpdate.

import { check, Update } from "@tauri-apps/plugin-updater";
import { relaunch } from "@tauri-apps/plugin-process";
import { invoke } from "@tauri-apps/api/core";

import { getUpdateChannel, type UpdateChannel } from "./settings";

/** What the Settings updater row is currently showing. */
export type UpdateStatus =
  | { kind: "idle" }
  | { kind: "checking" }
  | { kind: "uptodate" }
  | { kind: "available"; version: string }
  | { kind: "downloading"; percent: number | null }
  | { kind: "installing" }
  | { kind: "error"; message: string };

/** The subset of the updater plugin's `Update` metadata our backend command
 *  returns. Its shape matches what the plugin's own `check()` hands the `Update`
 *  constructor, so a channel result reconstitutes into a real `Update` handle —
 *  same `rid`, same `downloadAndInstall`, same minisign verification. */
interface UpdateMetadata {
  rid: number;
  currentVersion: string;
  version: string;
  date?: string;
  body?: string;
  rawJson: Record<string, unknown>;
}

/** Whether this install can update itself in place, asked of the backend (only
 *  Rust can see how the app was installed).
 *
 *  Everywhere but Linux the answer is always yes. On Linux the updater can only
 *  replace an AppImage — a copy from a package manager is owned by that manager,
 *  and offering to overwrite it would be a button that fails at best. The UI
 *  therefore drops the whole flow and says where updates come from instead; see
 *  `in_app_updates_supported` in `src-tauri/src/update_channel.rs`.
 *
 *  A failed call means no backend answered (an older shell, a test host), which
 *  is not a reason to hide a working updater — it resolves to `true`, the
 *  behaviour every build had before the question existed. */
export async function inAppUpdatesSupported(): Promise<boolean> {
  try {
    return await invoke<boolean>("in_app_updates_supported");
  } catch {
    return true;
  }
}

/** Ask the release host whether a newer signed release exists on the user's
 *  chosen channel. Returns the `Update` handle when one is available (the caller
 *  then downloads + installs it), or `null` when we're already current.
 *
 *  Stable stays on the plugin's own `check()`, which reads the endpoint baked
 *  into tauri.conf.json (latest.json) — the exact path shipped since 0.3.0, so
 *  installed stable clients are untouched. Beta can't be served that way: the
 *  updater endpoint is fixed at build time and the plugin's JS `check()` exposes
 *  no runtime override, so a small backend command (`check_update_for_channel`)
 *  points the updater at the beta manifest for this one check and hands back
 *  metadata we rebuild into a normal `Update`. The beta manifest carries the
 *  newest of the beta and stable releases, so a beta user always sees a shipped
 *  stable that is ahead of their build. Download, install and signature
 *  verification are then identical on both channels. */
export async function checkForUpdate(): Promise<Update | null> {
  if (getUpdateChannel() === "beta") {
    return checkChannelUpdate("beta");
  }
  return check();
}

/** Run a channel-scoped check through the backend and rebuild the plugin's
 *  `Update` handle from the returned metadata (or `null` when up to date). */
async function checkChannelUpdate(
  channel: UpdateChannel,
): Promise<Update | null> {
  const metadata = await invoke<UpdateMetadata | null>(
    "check_update_for_channel",
    { channel },
  );
  return metadata ? new Update(metadata) : null;
}

/** Ask the shell to raise a desktop notification for a release the periodic
 *  check found. The caller decides *whether* to: this is for the window nobody
 *  is looking at, where the banner is drawn into a hidden webview and reaches
 *  no one.
 *
 *  The wording is built in Rust rather than passed from here, the same way the
 *  connection-state toasts are (see `transition_notice` in src-tauri/src/lib.rs):
 *  a system notification is drawn outside the webview, so the shell — which
 *  already tracks the active language for the tray — owns its strings.
 *
 *  Fire and forget. A failed call means no backend answered (an older shell, a
 *  browser preview, a test host) or the OS refused the toast; neither is worth
 *  disturbing an update check over. */
export async function notifyUpdateAvailable(version: string): Promise<void> {
  try {
    await invoke("notify_update_available", { version });
  } catch {
    // No shell, or notifications are off at the OS level.
  }
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
