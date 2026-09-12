import { importErrorMessage } from "../lib/importError";
import { useId, useRef, useState } from "react";

import { useI18n } from "../i18n/I18nContext";

interface SimpleSetupProps {
  /** True once a subscription exists. */
  hasProfile: boolean;
  /** Import a subscription from a pasted link. */
  onSubscribe: (url: string) => Promise<void>;
}

/**
 * The one thing a new user has to supply, on the same screen as the button.
 *
 * It used to be two: the subscription link and the bypass archive. The archive
 * is not asked for at all any more — the core fetches and installs a bundle on
 * the first connect when there is none, so making the user find a release page,
 * pick the right asset and drag it in was asking them to do work the program
 * already does. Keeping it as a folded-away "optional" step was no better: it
 * still put a decision in front of someone who has none to make.
 *
 * Import stays local to this form; an error never displays a private URL.
 */
export function SimpleSetup({ hasProfile, onSubscribe }: SimpleSetupProps) {
  const { t } = useI18n();
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const pending = useRef(false);
  const fieldId = useId();

  // The link is the whole of the setup. A missing bundle is not a missing step:
  // the first connect installs one.
  if (hasProfile) return null;

  const run = async (fn: () => Promise<void>) => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setError(null);
    try {
      await fn();
      setUrl("");
    } catch (e) {
      setError(importErrorMessage(e, t));
    } finally {
      setBusy(false);
      pending.current = false;
    }
  };

  return (
    <section className="setup" aria-labelledby={`${fieldId}-heading`}>
      <div className="setup-intro">
        <h1 id={`${fieldId}-heading`}>{t.simple.welcome}</h1>
        <p>{t.simple.welcomeHint}</p>
      </div>
      <form className="setup-body" onSubmit={(event) => {
        event.preventDefault();
        if (url.trim()) void run(() => onSubscribe(url.trim()));
      }} aria-busy={busy}>
          <label className="setup-title" htmlFor={fieldId}>{t.simple.setupLink}</label>
          <div className="setup-row">
            <input
              id={fieldId}
              className="setup-input"
              type="url"
              inputMode="url"
              placeholder={t.simple.setupLinkPlaceholder}
              aria-describedby={`${fieldId}-help${error ? ` ${fieldId}-error` : ""}`}
              aria-invalid={!!error}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              value={url}
              disabled={busy}
              onChange={(e) => setUrl(e.target.value)}
            />
            <button
              type="submit"
              className="setup-go"
              disabled={busy || url.trim() === ""}
            >
              {busy ? t.simple.importing : t.profiles.import.title}
            </button>
          </div>
          <p className="setup-help" id={`${fieldId}-help`}>{t.simple.linkHelp}</p>
      </form>

      {error && (
        <p className="setup-error" id={`${fieldId}-error`} role="alert">
          {error}
        </p>
      )}
    </section>
  );
}
