import { useEffect, useRef } from "react";

import { useI18n } from "../i18n/I18nContext";

// Confirmation gate for a manual update while a tunnel is up. Installing an
// update relaunches the app (and on Windows stops the background service to swap
// it), which drops a live VPN connection — so when the user asks to install now
// mid-session, we make that consequence explicit and get an explicit go-ahead
// rather than cutting the tunnel silently. When the tunnel is already down the
// install runs without this prompt.
//
// Presentational and self-contained so the gate is unit-testable without
// mounting the whole app: it states the consequence and reports the user's
// choice through onConfirm / onCancel. It never touches the updater itself.

interface UpdateConfirmProps {
  /** Approve: proceed with the install (this drops the tunnel). */
  onConfirm: () => void;
  /** Decline: keep the connection and leave the update pending. */
  onCancel: () => void;
  /**
   * True while the surface is playing its exit. The owner keeps it mounted for
   * that beat (see lib/usePresence); this only paints it.
   */
  leaving?: boolean;
}

export function UpdateConfirm({
  onConfirm,
  onCancel,
  leaving = false,
}: UpdateConfirmProps) {
  const { t } = useI18n();
  // Focus the cancel action on mount: the safe default is to keep the tunnel, so
  // a stray Enter/Space shouldn't tear it down. Escape cancels too.
  const cancelRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    cancelRef.current?.focus();
  }, []);
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onCancel();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel]);

  return (
    <div
      className={`prof-modal-scrim${leaving ? " is-leaving" : ""}`}
      onMouseDown={(e) => {
        // A click on the scrim (not the card) cancels — the safe default.
        if (e.target === e.currentTarget) onCancel();
      }}
    >
      <div
        className="prof-modal deeplink-confirm"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="update-confirm-title"
        aria-describedby="update-confirm-body"
      >
        <h2 id="update-confirm-title" className="prof-modal-title">
          {t.update.confirmTitle}
        </h2>
        <div id="update-confirm-body" className="deeplink-confirm-body">
          <p className="deeplink-confirm-line">{t.update.confirmBody}</p>
        </div>
        <div className="prof-modal-actions">
          <button
            ref={cancelRef}
            type="button"
            className="prof-ghost"
            onClick={onCancel}
          >
            {t.home.cancel}
          </button>
          <button
            type="button"
            className="prof-btn-primary"
            onClick={onConfirm}
          >
            {t.update.installNow}
          </button>
        </div>
      </div>
    </div>
  );
}
