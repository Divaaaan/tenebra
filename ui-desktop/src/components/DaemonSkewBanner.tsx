import { useState } from "react";

import { useI18n } from "../i18n/I18nContext";

/**
 * The reinstall one-liner for the hand-installed macOS LaunchDaemon, run from a
 * checkout of the repo (which is how the daemon was installed in the first
 * place — see README's macOS note). --allow-unsigned matches today's unsigned
 * dev bundles; drop it from the command once release bundles carry a
 * Developer ID signature.
 */
export const MACOS_DAEMON_UPDATE_COMMAND =
  "sudo bash scripts/macos/install-daemon.sh --from-app /Applications/Tenebra.app --allow-unsigned";

interface DaemonSkewBannerProps {
  /** The daemon's reported version; null = predates the field (≤0.4.3). */
  daemonVersion: string | null;
  appVersion: string;
  onDismiss: () => void;
}

// One-line strip under the top bar, sharing the update banner's quiet look: a
// skewed daemon is a degradation warning, not an incident. It names the two
// builds and offers the platform's way out — the reinstall command on macOS
// (where the in-app updater never touches the root LaunchDaemon), the package
// route on Linux (the daemon is a system service the package owns, so the app
// can neither update nor restart it), and a reinstall hint on Windows (the
// installer refreshes the service itself, so a skew there means the app was
// laid down without running it).
export function DaemonSkewBanner({
  daemonVersion,
  appVersion,
  onDismiss,
}: DaemonSkewBannerProps) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);

  // No os plugin in the app; the UA is enough to pick a hint string. Linux
  // browsers report "X11" or "Linux" in it, and never "Mac", so the two tests
  // cannot both match.
  const isMac = navigator.userAgent.includes("Mac");
  const isLinux = !isMac && /Linux|X11/.test(navigator.userAgent);

  const text = daemonVersion
    ? t.daemon.stale
        .replace("{daemon}", daemonVersion)
        .replace("{app}", appVersion)
    : t.daemon.staleUnknown.replace("{app}", appVersion);

  async function copyCommand() {
    try {
      await navigator.clipboard.writeText(MACOS_DAEMON_UPDATE_COMMAND);
      setCopied(true);
    } catch {
      // Clipboard access denied: leave the button label as-is; the command
      // also lives in the README for anyone who needs to retype it.
    }
  }

  return (
    <div className="update-banner daemon-skew-banner" role="status">
      <span className="update-banner-text">⚠ {text}</span>
      <div className="update-banner-actions">
        {isMac ? (
          <button type="button" onClick={() => void copyCommand()}>
            ⧉ {copied ? t.daemon.copied : t.daemon.copyCommand}
          </button>
        ) : (
          <span className="daemon-skew-hint">
            {isLinux ? t.daemon.restartServiceHint : t.daemon.reinstallHint}
          </span>
        )}
        <button type="button" onClick={onDismiss}>
          ✕ {t.daemon.dismiss}
        </button>
      </div>
    </div>
  );
}
