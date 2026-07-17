import { useI18n } from "../i18n/I18nContext";

interface UpdateBannerProps {
  version: string;
  installing: boolean;
  /**
   * The install is armed but held back because a tunnel is up: it applies once
   * the tunnel drops. The strip says so, and its action reads "install now"
   * (dropping the tunnel) rather than a plain "update".
   */
  deferred: boolean;
  /** Integer download percent, or null while the size is unknown. */
  progress: number | null;
  onInstall: () => void;
  onDismiss: () => void;
}

// One-line strip under the top bar offering the release the launch check found.
// Deliberately quiet next to the kill-switch banner — an update is a heads-up,
// not an incident — and inline in the shell flow, never an overlay. While the
// install runs the strip shows download progress and drops the dismiss action:
// the restart is already on its way.
export function UpdateBanner({
  version,
  installing,
  deferred,
  progress,
  onInstall,
  onDismiss,
}: UpdateBannerProps) {
  const { t } = useI18n();

  const text = installing
    ? progress == null
      ? t.update.downloading
      : `${t.update.downloading} ${progress}%`
    : deferred
      ? t.update.deferred
      : t.update.available.replace("{version}", version);

  return (
    <div className="update-banner" role="status">
      <span className="update-banner-text">⇡ {text}</span>
      <div className="update-banner-actions">
        <button
          type="button"
          className="update-banner-install"
          onClick={onInstall}
          disabled={installing}
        >
          ▶ {deferred ? t.update.installNow : t.update.install}
        </button>
        {!installing && (
          <button type="button" onClick={onDismiss}>
            ✕ {t.update.later}
          </button>
        )}
      </div>
    </div>
  );
}
