import { useEffect, useRef, useState } from "react";

import { useI18n } from "../i18n/I18nContext";
import type { ProblemReport } from "../lib/useProblemReport";

interface ProblemReportModalProps {
  /** The assembled report; null while it is still being collected. */
  report: ProblemReport | null;
  /** True while the bundle is being collected. */
  building: boolean;
  onClose: () => void;
  /** Open the pre-filled issue form in the browser. */
  onOpenIssue: () => void;
  /**
   * True while the surface is playing its exit. The owner keeps it mounted for
   * that beat (see lib/usePresence); this only paints it.
   */
  leaving?: boolean;
}

/**
 * The report a user files by hand: the whole text on screen, a Copy button, and
 * a second button that opens the issue form.
 *
 * Two clicks rather than one, on purpose. The report is shown in full before
 * anything can be done with it — the user reads exactly what they would be
 * publishing, including that the masking worked — and it only moves when they
 * move it. Nothing is transmitted by this app at any point; opening the browser
 * is the last step and it carries no report with it, just a version and an OS.
 */
export function ProblemReportModal({
  report,
  building,
  onClose,
  onOpenIssue,
  leaving = false,
}: ProblemReportModalProps) {
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

  // A fresh report deserves a fresh button: leaving "Copied" over text that has
  // since been rebuilt would claim a copy that never happened.
  useEffect(() => {
    setCopied(false);
  }, [report]);

  async function copy() {
    if (!report) return;
    try {
      await navigator.clipboard.writeText(report.text);
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
        className="prof-modal problem-report-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="problem-report-title"
      >
        <h2 id="problem-report-title" className="prof-modal-title">
          {t.report.title}
        </h2>

        <p className="problem-report-lead">{t.report.lead}</p>
        <ol className="problem-report-steps">
          <li>{t.report.step1}</li>
          <li>{t.report.step2}</li>
          <li>{t.report.step3}</li>
        </ol>

        <pre className="crash-report-pre selectable" aria-busy={building}>
          {report ? report.text : t.report.building}
        </pre>

        {/* Where the untrimmed copy lives — or, when the core never answered,
            that this report is the shell's half only. Either way the user is
            told what they are holding. */}
        {report &&
          (report.path ? (
            <p className="problem-report-note">
              {t.report.savedTo}{" "}
              <span className="selectable">{report.path}</span>
            </p>
          ) : (
            <p className="problem-report-note is-warn" role="status">
              {t.report.corePartial}
            </p>
          ))}

        <div className="prof-modal-actions">
          <button
            type="button"
            className="prof-btn-primary"
            onClick={() => void copy()}
            disabled={!report}
          >
            {copied ? t.report.copied : t.report.copy}
          </button>
          <button
            type="button"
            className="prof-ghost"
            onClick={onOpenIssue}
            disabled={!report}
          >
            {t.report.openIssue}
          </button>
          <button
            ref={closeRef}
            type="button"
            className="prof-ghost"
            onClick={onClose}
          >
            {t.report.close}
          </button>
        </div>
      </div>
    </div>
  );
}
