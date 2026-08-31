// Assembles the "report a problem" bundle — the text a user pastes into a
// GitHub issue when the app misbehaves without crashing.
//
// It is deliberately built out of two independent halves. The core's own
// support bundle (state, build versions, routes, the last connect walk, the tail
// of the service log) is the richer half; the shell's own diagnostics (app
// build, platform, the log console buffer) are the half that still exists when
// the core is wedged, unreachable, or too old to answer — which is exactly the
// situation people want to complain about. Losing the report because the core
// went quiet would drop it precisely when it is needed.
//
// Nothing here sends anything. The result goes on the clipboard and into a local
// file; the browser only ever opens on a separate, explicit click.

import { buildDiagnostics, scrubSecrets } from "./diagnostics";
import type { LogLine } from "../state/useTenebra";

/**
 * GitHub's hard ceiling on an issue body. Past this the form refuses the
 * submission outright, which for a reporter looks like the site is broken.
 */
export const GITHUB_BODY_LIMIT = 65536;

/**
 * What a report is trimmed to. Comfortably under {@link GITHUB_BODY_LIMIT} so
 * the reporter's own description, the template's other fields and the fenced
 * code block's own markup all still fit around it.
 */
export const REPORT_BUDGET = 55000;

/** Share of the surviving text kept from the head; the rest is the tail. */
const HEAD_SHARE = 0.4;

/**
 * Move `end` back off the tail of a surrogate pair, so `slice(0, end)` cannot
 * cut a character in half.
 *
 * JavaScript indexes strings by UTF-16 code unit, and every character outside
 * the BMP — an emoji in a profile name, in a log line the user pasted, in a
 * Windows adapter name — is two of them. Cutting between the halves leaves a
 * lone surrogate, which is not encodable: written to the report file or handed
 * to the clipboard it becomes U+FFFD, and the reporter sees a � in a report they
 * did not corrupt. One character short is the correct answer.
 */
function backOffSurrogate(text: string, end: number): number {
  const code = text.charCodeAt(end - 1);
  // A high surrogate here means the pair's other half is at `end`, on the far
  // side of the cut.
  return code >= 0xd800 && code <= 0xdbff ? end - 1 : end;
}

/**
 * Move `start` forward off the head of a surrogate pair, the mirror of
 * {@link backOffSurrogate} for a slice that runs to the end of the string.
 */
function forwardOffSurrogate(text: string, start: number): number {
  const code = text.charCodeAt(start);
  // A low surrogate here means this index is the second half of a pair whose
  // first half was cut away.
  return code >= 0xdc00 && code <= 0xdfff ? start + 1 : start;
}

export interface ProblemReportInput {
  /** The app build, from the __APP_VERSION__ define. */
  appVersion: string;
  /** The daemon's reported build; null/undefined when it hasn't said. */
  daemonVersion: string | null | undefined;
  /** navigator.userAgent — the OS/engine line. */
  platform: string;
  /** When the report was taken. */
  when: Date;
  /** The log console buffer, oldest first. */
  logs: LogLine[];
  /** The core's support bundle and where it was saved, when it answered. */
  core: { path: string; text: string } | null;
  /** Why the core produced nothing, when it didn't. */
  coreError: string | null;
}

/**
 * Cut a report down to `budget` characters by removing its middle.
 *
 * The middle is what goes because both ends carry more than it does: the head
 * holds the versions and the connection state, and the tail holds the newest log
 * lines — the ones written while the thing being reported was going wrong. A
 * naive `slice(0, budget)` keeps the header and throws away the failure.
 *
 * The cut is always announced, with the path to the untrimmed copy on disk, so
 * nobody reads a trimmed report as a complete one.
 */
export function trimToBudget(
  text: string,
  budget: number,
  fullPath: string | null,
): string {
  if (text.length <= budget) {
    return text;
  }

  const where = fullPath
    ? `full, untrimmed report: ${fullPath}`
    : "the full report was not saved: the core did not answer";
  const marker = (cut: number) =>
    `\n\n[--- ${cut} characters cut from the middle of this report ---]\n[--- ${where} ---]\n\n`;

  // Reserve against the widest the marker can get: the cut is always smaller
  // than the whole text, so its digit count never exceeds this one's.
  const reserve = marker(text.length).length;
  const keep = budget - reserve;
  if (keep <= 0) {
    // A budget too small to hold even the notice. Say what happened rather than
    // hand back a silently truncated fragment.
    return marker(text.length).trim().slice(0, Math.max(budget, 0));
  }

  const head = Math.floor(keep * HEAD_SHARE);
  const tail = keep - head;
  // Both cuts only ever move inwards, so the result still fits the budget.
  const headEnd = backOffSurrogate(text, head);
  const tailStart = forwardOffSurrogate(text, text.length - tail);
  return (
    text.slice(0, headEnd) + marker(tailStart - headEnd) + text.slice(tailStart)
  );
}

/**
 * Assemble the problem report and trim it to `budget`.
 *
 * The format is fixed and English in every UI language, like the diagnostics
 * block it embeds: it is a report a maintainer reads, not interface chrome. The
 * whole thing goes through scrubSecrets on the way out — both halves already
 * mask their own, and doing it once more here means a future contributor cannot
 * widen the report without the masking following along.
 */
export function buildProblemReport(
  input: ProblemReportInput,
  budget = REPORT_BUDGET,
): string {
  const { appVersion, daemonVersion, platform, when, logs, core, coreError } =
    input;

  const header = [
    "Tenebra problem report",
    "======================",
    `Generated:    ${when.toISOString()}`,
    `Full report:  ${core ? core.path : "not saved — the core did not answer"}`,
    "",
    "Core diagnostics",
    "----------------",
    core
      ? core.text.trimEnd()
      : [
          `Unavailable: ${coreError ?? "the core did not answer"}`,
          "Everything below is what the app itself could see.",
        ].join("\n"),
    "",
  ].join("\n");

  const shell = buildDiagnostics({
    appVersion,
    daemonVersion,
    platform,
    when,
    // The leak check lives on the Logs screen and is not run for a report;
    // claiming otherwise would put a stale verdict in front of a maintainer.
    leak: null,
    logs,
  });

  return trimToBudget(
    scrubSecrets(`${header}\n${shell}\n`),
    budget,
    core?.path ?? null,
  );
}
