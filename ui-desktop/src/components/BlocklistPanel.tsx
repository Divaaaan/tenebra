import { useCallback, useRef, useState } from "react";

import { useI18n } from "../i18n/I18nContext";

export interface BlocklistSource {
  /** Stable id, used as the React key and for removal. */
  id: string;
  /** What the user sees: the archive's file name. */
  label: string;
  /** Rules parsed out of it; null while it is still being read. */
  rules: number | null;
}

interface BlocklistPanelProps {
  sources: BlocklistSource[];
  /** Import a blocklist from a dropped or picked archive. */
  onImportFile: (file: File) => Promise<void>;
  onRemove: (id: string) => void;
  /** Close the panel (it is opened as an overlay from the settings row). */
  onClose: () => void;
}

/** Archive and list formats the importer accepts. */
const ACCEPTED = [".zip", ".txt", ".lst", ".srs"];

function hasAcceptedExtension(name: string): boolean {
  const lower = name.toLowerCase();
  return ACCEPTED.some((ext) => lower.endsWith(ext));
}

/**
 * The blocklist import surface: drop an archive here, or click to pick one.
 *
 * Deliberately file-only. Server subscriptions arrive as a URL elsewhere in the
 * app; a blocklist is a snapshot the user brings themselves, and keeping the two
 * inputs in separate places is what stops a VPN link being pasted into the
 * blocklist field and silently doing nothing.
 *
 * Errors render inside the panel rather than as a toast: the user is looking at
 * the zone they just dropped onto, and a message that appears elsewhere reads as
 * "something broke" instead of "this file is wrong".
 */
export function BlocklistPanel({
  sources,
  onImportFile,
  onRemove,
  onClose,
}: BlocklistPanelProps) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [dragging, setDragging] = useState(false);
  const fileRef = useRef<HTMLInputElement | null>(null);

  // Nested drag events fire on every child element, so a plain boolean would
  // flicker the highlight as the pointer crosses the zone's own contents. A
  // depth counter tracks enter/leave pairs instead.
  const dragDepth = useRef(0);

  const submitFile = useCallback(
    async (file: File) => {
      if (busy) return;
      if (!hasAcceptedExtension(file.name)) {
        setError(t.blocklist.badFile);
        return;
      }
      setBusy(true);
      setError(null);
      try {
        await onImportFile(file);
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setBusy(false);
      }
    },
    [busy, onImportFile, t.blocklist.badFile],
  );

  return (
    <div className="bl-scrim" onClick={onClose} role="presentation">
      <section
        className="bl-panel"
        aria-label={t.blocklist.title}
        // The scrim closes on click; stop the panel's own clicks from bubbling
        // into it, or dropping a file would dismiss the panel underneath.
        onClick={(e) => e.stopPropagation()}
      >
        <header className="bl-panel__head">
          <h2 className="bl-panel__title">{t.blocklist.title}</h2>
          <button
            type="button"
            className="bl-panel__close"
            onClick={onClose}
            aria-label={t.blocklist.close}
          >
            ×
          </button>
        </header>
        <p className="bl-panel__hint">{t.blocklist.hint}</p>

        <div
          className={`bl-drop${dragging ? " is-dragging" : ""}${busy ? " is-busy" : ""}`}
          role="button"
          tabIndex={0}
          aria-label={t.blocklist.dropHint}
          onClick={() => fileRef.current?.click()}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              fileRef.current?.click();
            }
          }}
          onDragEnter={(e) => {
            e.preventDefault();
            dragDepth.current += 1;
            setDragging(true);
          }}
          onDragOver={(e) => e.preventDefault()}
          onDragLeave={(e) => {
            e.preventDefault();
            dragDepth.current -= 1;
            if (dragDepth.current <= 0) {
              dragDepth.current = 0;
              setDragging(false);
            }
          }}
          onDrop={(e) => {
            e.preventDefault();
            dragDepth.current = 0;
            setDragging(false);
            const file = e.dataTransfer?.files?.[0];
            if (file) void submitFile(file);
          }}
        >
          <span className="bl-drop__ring" aria-hidden="true" />
          <span className="bl-drop__label">
            {busy ? t.blocklist.loading : t.blocklist.dropHint}
          </span>
          <span className="bl-drop__formats">{ACCEPTED.join("   ")}</span>
          <input
            ref={fileRef}
            type="file"
            className="bl-file"
            accept={ACCEPTED.join(",")}
            onChange={(e) => {
              const file = e.target.files?.[0];
              // Clear the value so picking the same file twice fires onChange.
              e.target.value = "";
              if (file) void submitFile(file);
            }}
          />
        </div>

        {error && (
          <p className="bl-error" role="alert">
            {error}
          </p>
        )}

        {sources.length > 0 && (
          <ul className="bl-list">
            {sources.map((s, i) => (
              <li
                key={s.id}
                className="bl-item"
                // Stagger so an imported batch cascades in instead of landing as
                // one block; capped so a long list does not crawl.
                style={{ animationDelay: `${Math.min(i, 8) * 40}ms` }}
              >
                <span className="bl-item__label">{s.label}</span>
                <span className="bl-item__rules">
                  {s.rules === null ? t.blocklist.counting : `${s.rules} ${t.blocklist.rules}`}
                </span>
                <button
                  type="button"
                  className="bl-item__remove"
                  onClick={() => onRemove(s.id)}
                  aria-label={`${t.blocklist.remove} ${s.label}`}
                >
                  ×
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
