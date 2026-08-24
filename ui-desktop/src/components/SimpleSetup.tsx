import { useState } from "react";

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
 * Everything here disappears once it is done. A setup step that stays visible
 * after it is satisfied is clutter, and clutter is what this screen exists to
 * avoid — the finished state is a status word and one control.
 */
export function SimpleSetup({ hasProfile, onSubscribe }: SimpleSetupProps) {
  const { t } = useI18n();
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The link is the whole of the setup. A missing bundle is not a missing step:
  // the first connect installs one.
  if (hasProfile) return null;

  const run = async (fn: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="setup">
      <div className="setup-step">
        <div className="setup-body">
          <span className="setup-title">{t.simple.setupLink}</span>
          <div className="setup-row">
            <input
              className="setup-input"
              type="url"
              inputMode="url"
              placeholder={t.simple.setupLinkPlaceholder}
              aria-label={t.simple.setupLink}
              value={url}
              disabled={busy}
              onChange={(e) => setUrl(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && url.trim()) {
                  e.preventDefault();
                  void run(() => onSubscribe(url.trim()));
                }
              }}
            />
            <button
              type="button"
              className="setup-go"
              disabled={busy || url.trim() === ""}
              onClick={() => void run(() => onSubscribe(url.trim()))}
            >
              →
            </button>
          </div>
        </div>
      </div>

      {error && (
        <p className="setup-error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
