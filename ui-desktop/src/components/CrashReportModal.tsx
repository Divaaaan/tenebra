import { useEffect, useRef, useState } from "react";

import { useI18n } from "../i18n/I18nContext";

interface CrashReportModalProps {
  report: string;
  onClose: () => void;
  /**
   * True while the surface is playing its exit. The owner keeps it mounted for
   * that beat (see lib/usePresence); this only paints it.
   */
  leaving?: boolean;
}

/**
 * Overlay showing the raw crash-report text with a Copy button, so the user can
 * read exactly what would go into a report and paste it into a GitHub issue. Esc
 * or a scrim click closes it, matching the app's other overlays. The report is
 * read from the local file; opening this sends nothing.
 */
export function CrashReportModal({
  report,
  onClose,
  leaving = false,
}: CrashReportModalProps) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const closeRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    closeRef.current?.focus();
  }, []);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  async function copy() {
    try {
      await navigator.clipboard.writeText(report);
      setCopied(true);
    } catch {
      // Clipboard blocked; the text stays selectable for a manual copy.
    }
  }

  return (
    <div
      className={`prof-modal-scrim${leaving ? " is-leaving" : ""}`}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        className="prof-modal crash-report-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="crash-report-title"
      >
        <h2 id="crash-report-title" className="prof-modal-title">
          {t.crash.reportTitle}
        </h2>
        <pre className="crash-report-pre selectable">{report}</pre>
        <div className="prof-modal-actions">
          <button type="button" className="prof-btn-primary" onClick={copy}>
            {copied ? t.crash.reportCopied : t.crash.reportCopy}
          </button>
          <button
            ref={closeRef}
            type="button"
            className="prof-ghost"
            onClick={onClose}
          >
            {t.crash.reportClose}
          </button>
        </div>
      </div>
    </div>
  );
}
