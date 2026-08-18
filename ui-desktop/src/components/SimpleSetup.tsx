import { useState } from "react";

import { useI18n } from "../i18n/I18nContext";

interface SimpleSetupProps {
  /** True once a subscription exists. */
  hasProfile: boolean;
  /** True once a bypass bundle is installed. */
  hasBypass: boolean;
  /** Import a subscription from a pasted link. */
  onSubscribe: (url: string) => Promise<void>;
  /** Import a bypass bundle from dropped paths (archive or unpacked folder). */
  onBypassPaths: (paths: string[]) => Promise<void>;
  /** Import a bypass bundle from dropped files. */
  onBypassFiles: (files: File[]) => Promise<void>;
}

/**
 * The two things a new user has to supply, on the same screen as the button.
 *
 * Everything here disappears once it is done. A setup step that stays visible
 * after it is satisfied is clutter, and clutter is what this screen exists to
 * avoid — the finished state is a status word and one control.
 *
 * The steps are numbered and shown together rather than as a wizard: both are
 * things the user already has in hand (a link they were sold, an archive they
 * downloaded), and hiding the second behind the first would make the app feel
 * longer than it is.
 */
export function SimpleSetup({
  hasProfile,
  hasBypass,
  onSubscribe,
  onBypassPaths,
  onBypassFiles,
}: SimpleSetupProps) {
  const { t } = useI18n();
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [dragging, setDragging] = useState(false);

  if (hasProfile && hasBypass) return null;

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
      {!hasProfile && (
        <div className="setup-step">
          <span className="setup-num" aria-hidden="true">
            1
          </span>
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
      )}

      {!hasBypass && (
        <div className="setup-step">
          <span className="setup-num" aria-hidden="true">
            {hasProfile ? "1" : "2"}
          </span>
          <div className="setup-body">
            <span className="setup-title">{t.simple.setupBypass}</span>
            <label
              className={`setup-drop${dragging ? " is-dragging" : ""}${busy ? " is-busy" : ""}`}
              onDragEnter={(e) => {
                e.preventDefault();
                setDragging(true);
              }}
              onDragOver={(e) => e.preventDefault()}
              onDragLeave={(e) => {
                e.preventDefault();
                setDragging(false);
              }}
              onDrop={(e) => {
                e.preventDefault();
                setDragging(false);
                const files = Array.from(e.dataTransfer?.files ?? []);
                if (files.length > 0) void run(() => onBypassFiles(files));
              }}
            >
              <span className="setup-drop-text">{t.simple.setupBypassHint}</span>
              <input
                type="file"
                className="setup-file"
                onChange={(e) => {
                  const files = Array.from(e.target.files ?? []);
                  e.target.value = "";
                  if (files.length > 0) void run(() => onBypassFiles(files));
                }}
              />
            </label>
            {/* Folders arrive only through Tauri's own drag-drop, which reports
                paths; the browser drop above cannot see them. Wiring both means
                "drop the folder" and "drop the zip" behave the same. */}
            <button
              type="button"
              className="setup-skip"
              disabled={busy}
              onClick={() => void run(() => onBypassPaths([]))}
              hidden
            />
          </div>
        </div>
      )}

      {error && (
        <p className="setup-error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
