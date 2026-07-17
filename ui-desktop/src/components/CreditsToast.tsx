import { useEffect } from "react";

import { useI18n } from "../i18n/I18nContext";

/** How long the credits linger before self-dismissing, in ms. */
export const CREDITS_MS = 6000;

// A small crown, drawn in the mono face. Rendered in a <pre> so the columns hold.
const CROWN = ["  *   *   *", " / \\ / \\ / \\", "'-----------'"].join("\n");

interface CreditsToastProps {
  /** Dismiss the card — on the timeout, or a click / Escape. */
  onDismiss: () => void;
}

/**
 * A quiet thank-you, revealed by tapping the wordmark seven times. Not the action
 * toast (that channel is for confirmations) — a small, self-dismissing card with a
 * mono crown and a line of gratitude. Deliberately understated: a nod to whoever
 * went looking, not an announcement.
 */
export function CreditsToast({ onDismiss }: CreditsToastProps) {
  const { t } = useI18n();
  useEffect(() => {
    const id = setTimeout(onDismiss, CREDITS_MS);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onDismiss();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      clearTimeout(id);
      window.removeEventListener("keydown", onKey);
    };
  }, [onDismiss]);

  return (
    <div className="credits" role="status" aria-live="polite">
      <button
        type="button"
        className="credits-card"
        onClick={onDismiss}
        aria-label={t.toast.dismiss}
      >
        <span className="credits-crown" aria-hidden="true">
          {CROWN}
        </span>
        <span className="credits-line">made in the dark</span>
        <span className="credits-sub">thank you for looking closer</span>
      </button>
    </div>
  );
}
