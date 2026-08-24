import { useEffect, useRef } from "react";

import { useI18n } from "../i18n/I18nContext";

// Override gate for the tun-conflict guard. The core refuses to raise our tunnel
// while another VPN owns the default route; the user is the only one who knows
// whether the two overlap, so the refusal has to be askable — but not with
// `window.confirm`. Under Tauri v2 that call is brokered through the dialog
// plugin, which this app deliberately does not grant beyond the file picker: the
// call threw "plugin:dialog|confirm not allowed by ACL" and took the connect
// down with it. Worse, the brokered `confirm` is asynchronous, so the old
// `if (!window.confirm(...))` would have read a Promise as truthy and overridden
// the guard *without asking* had the permission been granted.
//
// Presentational and self-contained like DeepLinkConfirm: it renders the
// question and reports the answer through onConfirm / onCancel. Cancel is the
// safe default — Escape and a scrim click both decline.

interface TunConflictConfirmProps {
  /** Approve: connect anyway, overriding the guard for this attempt only. */
  onConfirm: () => void;
  /** Decline: leave the guard's refusal standing. */
  onCancel: () => void;
  /**
   * True while the surface is playing its exit. The owner keeps it mounted for
   * that beat (see lib/usePresence); this only paints it.
   */
  leaving?: boolean;
}

export function TunConflictConfirm({
  onConfirm,
  onCancel,
  leaving = false,
}: TunConflictConfirmProps) {
  const { t } = useI18n();
  // Focus the *declining* button on mount: overriding the guard can take the
  // machine offline, so a stray Enter must not be the one that does it.
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
        if (e.target === e.currentTarget) onCancel();
      }}
    >
      <div
        className="prof-modal tunconflict-confirm"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="tunconflict-confirm-title"
        aria-describedby="tunconflict-confirm-body"
      >
        <h2 id="tunconflict-confirm-title" className="prof-modal-title">
          {t.daemon.tunConflictOverrideTitle}
        </h2>
        <div id="tunconflict-confirm-body" className="deeplink-confirm-body">
          <p className="deeplink-confirm-line">{t.daemon.tunConflictOverride}</p>
        </div>
        <div className="prof-modal-actions">
          <button
            ref={cancelRef}
            type="button"
            className="prof-ghost"
            onClick={onCancel}
          >
            {t.daemon.tunConflictOverrideCancel}
          </button>
          <button type="button" className="prof-btn-primary" onClick={onConfirm}>
            {t.daemon.tunConflictOverrideConfirm}
          </button>
        </div>
      </div>
    </div>
  );
}
