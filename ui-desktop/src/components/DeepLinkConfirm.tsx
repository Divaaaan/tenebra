import { useEffect, useRef } from "react";

import { useI18n } from "../i18n/I18nContext";

// Confirmation gate for a state-changing `tenebra://` deep link. A deep link can
// be handed to the app by any web page the user visits, so a connect must not
// fire on arrival the way it once did — the user approves it here first. Import
// already required a click (it only opens the pre-filled import dialog); this
// makes connect parallel. The Rust side still owns parsing the link into a
// tagged action; this only gates the acting on it.
//
// Presentational and self-contained so the gate is unit-testable without
// mounting the whole app: it renders the request, and reports the user's choice
// through onConfirm / onCancel. It never touches the backend itself.

interface DeepLinkConfirmProps {
  /** The subscription id/name the link asked to connect. */
  profile: string;
  /** Approve: proceed with the connect. */
  onConfirm: () => void;
  /** Decline: drop the request without connecting. */
  onCancel: () => void;
}

export function DeepLinkConfirm({
  profile,
  onConfirm,
  onCancel,
}: DeepLinkConfirmProps) {
  const { t } = useI18n();
  // Focus the primary action on mount so the prompt is keyboard-operable and
  // screen readers announce it; Escape declines (the safe default).
  const confirmRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    confirmRef.current?.focus();
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
      className="prof-modal-scrim"
      onMouseDown={(e) => {
        // A click on the scrim (not the card) declines — the safe default.
        if (e.target === e.currentTarget) onCancel();
      }}
    >
      <div
        className="prof-modal deeplink-confirm"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="deeplink-confirm-title"
        aria-describedby="deeplink-confirm-body"
      >
        <h2 id="deeplink-confirm-title" className="prof-modal-title">
          {t.deeplink.connectTitle}
        </h2>
        <div id="deeplink-confirm-body" className="deeplink-confirm-body">
          <p className="deeplink-confirm-line">
            {t.deeplink.connectBody.replace("{profile}", profile)}
          </p>
          <p className="deeplink-confirm-source">{t.deeplink.connectSource}</p>
        </div>
        <div className="prof-modal-actions">
          <button type="button" className="prof-ghost" onClick={onCancel}>
            {t.deeplink.cancel}
          </button>
          <button
            ref={confirmRef}
            type="button"
            className="prof-btn-primary"
            onClick={onConfirm}
          >
            {t.deeplink.confirm}
          </button>
        </div>
      </div>
    </div>
  );
}
