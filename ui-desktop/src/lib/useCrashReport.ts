import { useCallback, useEffect, useRef, useState } from "react";

import { api, type CrashReport } from "../api";

/** localStorage key holding the signature of the last dismissed crash report. */
const ACK_KEY = "tenebra.crashAcknowledged";

export interface CrashReportPrompt {
  /** The report to offer, or null when there is nothing to show. */
  report: CrashReport | null;
  /** Mark the current report seen so it isn't offered again on the next launch. */
  dismiss: () => void;
}

/**
 * On the first ready render, ask the core whether a crash file from a previous
 * run exists. A report is surfaced only when all hold: the user has opted in
 * (`consent`), a file is present, and its signature differs from the last one the
 * user dismissed — so the same crash isn't re-offered, but a newer one is.
 * Reading the file is local core I/O; nothing is sent. The check runs regardless
 * of `consent` (it's a local read), but the result is gated on it, so flipping
 * consent on later reveals a pending report without another launch.
 */
export function useCrashReport(
  consent: boolean,
  ready: boolean,
): CrashReportPrompt {
  const [report, setReport] = useState<CrashReport | null>(null);
  const checked = useRef(false);

  useEffect(() => {
    if (checked.current || !ready) return;
    checked.current = true;
    void (async () => {
      let found: CrashReport | null;
      try {
        found = await api.checkCrashReport();
      } catch {
        return; // No report, or the command is unavailable; stay silent.
      }
      if (!found) return;
      if (localStorage.getItem(ACK_KEY) === found.signature) return;
      setReport(found);
    })();
  }, [ready]);

  const dismiss = useCallback(() => {
    setReport((current) => {
      if (current) {
        try {
          localStorage.setItem(ACK_KEY, current.signature);
        } catch {
          // Non-fatal: at worst the same report is offered again next launch.
        }
      }
      return null;
    });
  }, []);

  return { report: consent ? report : null, dismiss };
}
