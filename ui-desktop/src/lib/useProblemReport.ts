import { useCallback, useEffect, useRef, useState } from "react";

import { api } from "../api";
import type { SavedDiagnostics } from "../api";
import { buildProblemReport } from "./report";
import type { LogLine } from "../state/useTenebra";

/**
 * Same-document event any screen can raise to ask the shell for the report
 * flow. Screens nested inside an overlay (the diagnostics panel in Settings)
 * have no path to the shell's state and would otherwise have to grow one
 * through every layer between — for a surface that has to outlive the overlay
 * anyway. The string is the contract; keep it verbatim on both sides.
 */
export const REPORT_PROBLEM_EVENT = "tenebra:report-problem";

/** What the report surface shows once a report has been assembled. */
export interface ProblemReport {
  /** The report, masked and trimmed to what an issue body will take. */
  text: string;
  /** Where the full, untrimmed copy was saved; null when the core said nothing. */
  path: string | null;
}

export interface ProblemReportState {
  /** True while the report surface should be on screen. */
  active: boolean;
  /** The assembled report; null while it is still being collected. */
  report: ProblemReport | null;
  /** True while the bundle is being collected. */
  building: boolean;
  /** Open the surface and assemble a report. Local work only — nothing is sent. */
  open: () => void;
  /** Close the surface. */
  close: () => void;
  /** Open the pre-filled issue form. The only thing here that leaves the machine. */
  openIssue: () => void;
}

/** A rejection's text, for the record — the report is read by a maintainer. */
function reason(e: unknown): string {
  if (e instanceof Error) return e.message;
  if (typeof e === "string") return e;
  return "unknown error";
}

/**
 * The "report a problem" flow: collect, show, and — only if the user then asks —
 * open the issue form.
 *
 * The two halves are deliberately separate calls with a click between them. This
 * app has no telemetry and never will: assembling a report is local work (one
 * core command, one file written on this machine, one clipboard-sized block of
 * text), and the browser opens only from {@link ProblemReportState.openIssue}. A
 * refactor that folds the two together turns a diagnostic into a transmission,
 * so App.report.test.tsx asserts the separation directly.
 *
 * A core that refuses or never answers does not sink the report: the shell's own
 * diagnostics still describe the app, the platform and the log buffer, and the
 * failure is written into the report rather than swallowed. That case is not an
 * edge — a wedged core is one of the likelier reasons somebody is reporting
 * anything at all.
 */
export function useProblemReport(
  daemonVersion: string | null | undefined,
  logs: LogLine[],
): ProblemReportState {
  const [active, setActive] = useState(false);
  const [report, setReport] = useState<ProblemReport | null>(null);
  const [building, setBuilding] = useState(false);
  const inFlight = useRef(false);

  // Read at click time, not captured at mount: the log buffer grows constantly
  // and `open` is a stable callback, so a closure over it would report whatever
  // the console held when the component first rendered.
  const source = useRef({ daemonVersion, logs });
  source.current = { daemonVersion, logs };

  const open = useCallback(() => {
    setActive(true);
    if (inFlight.current) {
      return;
    }
    inFlight.current = true;
    setReport(null);
    setBuilding(true);
    void (async () => {
      let core: SavedDiagnostics | null = null;
      let coreError: string | null = null;
      try {
        core = await api.collectDiagnostics();
      } catch (e) {
        coreError = reason(e);
      }
      setReport({
        text: buildProblemReport({
          appVersion: __APP_VERSION__,
          daemonVersion: source.current.daemonVersion,
          platform: navigator.userAgent,
          when: new Date(),
          logs: source.current.logs,
          core,
          coreError,
        }),
        path: core?.path ?? null,
      });
      inFlight.current = false;
      setBuilding(false);
    })();
  }, []);

  // Screens too deep to reach this state ask for the flow by event; the shell
  // holds the only listener, so the surface is a singleton however it was asked
  // for.
  useEffect(() => {
    window.addEventListener(REPORT_PROBLEM_EVENT, open);
    return () => window.removeEventListener(REPORT_PROBLEM_EVENT, open);
  }, [open]);

  // The report itself is left in place: the surface plays an exit animation, and
  // emptying it here would blank the card mid-leave. The next open replaces it.
  const close = useCallback(() => setActive(false), []);

  const openIssue = useCallback(() => {
    // Rust builds the whole URL and hands it to the OS browser; a failure to
    // launch one is not worth a dialog over an already-copied report.
    void api.openProblemUrl().catch(() => {});
  }, []);

  return { active, report, building, open, close, openIssue };
}
