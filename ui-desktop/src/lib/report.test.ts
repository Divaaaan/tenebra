import { describe, expect, it } from "vitest";

import { buildProblemReport, trimToBudget, REPORT_BUDGET } from "./report";
import type { LogLine } from "../state/useTenebra";

// The problem report is the thing a user pastes into an issue when the app is
// misbehaving but hasn't crashed. Two properties matter and are covered here:
// it survives a core that can't answer (which is most of the time when someone
// wants to complain), and it fits inside a GitHub issue body without silently
// losing the end of the log, which is the part that explains the failure.

const WHEN = new Date("2026-08-24T01:15:00.000Z");

function makeLogs(count: number, prefix = "line"): LogLine[] {
  return Array.from({ length: count }, (_, i) => ({
    id: i,
    at: WHEN,
    level: "info" as const,
    msg: `${prefix} ${i}`,
  }));
}

function baseInput() {
  return {
    appVersion: "0.5.5",
    daemonVersion: "0.5.5",
    platform: "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
    when: WHEN,
    logs: makeLogs(3),
    core: {
      path: String.raw`C:\Users\me\AppData\Local\Tenebra\tenebra-diagnostics-20260824-011500.txt`,
      text: "Tenebra core diagnostics\nstate: idle\n",
    },
    coreError: null,
  };
}

describe("buildProblemReport", () => {
  it("carries both sides: what the core said and what the shell saw", () => {
    const report = buildProblemReport(baseInput());

    expect(report).toContain("Tenebra core diagnostics");
    expect(report).toContain("state: idle");
    // The shell's own block — versions, platform, the log buffer.
    expect(report).toContain("0.5.5");
    expect(report).toContain("Windows NT 10.0");
    expect(report).toContain("line 2");
  });

  it("names the file holding the untrimmed copy", () => {
    const report = buildProblemReport(baseInput());

    expect(report).toContain("tenebra-diagnostics-20260824-011500.txt");
  });

  // The case people actually report in: the core is wedged, unreachable, or an
  // older build without the command. A report that gives up here would be
  // missing exactly when it is wanted.
  it("still reports when the core never answered", () => {
    const report = buildProblemReport({
      ...baseInput(),
      core: null,
      coreError: "core unreachable",
    });

    expect(report).toContain("core unreachable");
    // Everything the shell knows on its own is still there.
    expect(report).toContain("0.5.5");
    expect(report).toContain("Windows NT 10.0");
    expect(report).toContain("line 2");
  });

  it("masks a subscription token that reached the log buffer", () => {
    const report = buildProblemReport({
      ...baseInput(),
      logs: [
        {
          id: 1,
          at: WHEN,
          level: "error",
          msg: "fetch failed: https://vpsxd.pro/sub/9f8e7d6c5b4a3210",
        },
      ],
    });

    expect(report).not.toContain("9f8e7d6c5b4a3210");
    expect(report).toContain("vpsxd.pro");
  });

  it("trims an oversized bundle to the budget and says so", () => {
    const input = baseInput();
    const report = buildProblemReport(
      {
        ...input,
        core: { ...input.core, text: "x".repeat(REPORT_BUDGET * 2) },
      },
      REPORT_BUDGET,
    );

    expect(report.length).toBeLessThanOrEqual(REPORT_BUDGET);
    expect(report).toContain("cut from the middle");
    expect(report).toContain("tenebra-diagnostics-20260824-011500.txt");
  });
});

describe("trimToBudget", () => {
  const PATH = String.raw`C:\Users\me\AppData\Local\Tenebra\bundle.txt`;

  it("leaves a report that already fits completely alone", () => {
    const text = "short enough";
    expect(trimToBudget(text, 1000, PATH)).toBe(text);
  });

  it("keeps the head and the tail and cuts the middle", () => {
    const text = `HEAD-MARKER\n${"m".repeat(5000)}\nTAIL-MARKER`;
    const trimmed = trimToBudget(text, 600, PATH);

    expect(trimmed.length).toBeLessThanOrEqual(600);
    expect(trimmed).toContain("HEAD-MARKER");
    expect(trimmed).toContain("TAIL-MARKER");
    // The middle is what went; nothing pretends the report is whole.
    expect(trimmed).not.toContain("m".repeat(5000));
  });

  it("says how much went and where the whole thing lives", () => {
    const text = "H".repeat(200) + "T".repeat(200);
    const trimmed = trimToBudget(text, 300, PATH);

    expect(trimmed).toMatch(/\d+ characters cut from the middle/);
    expect(trimmed).toContain(PATH);
  });

  it("admits when there is no saved copy to point at", () => {
    const text = "H".repeat(200) + "T".repeat(200);
    const trimmed = trimToBudget(text, 300, null);

    expect(trimmed).toContain("cut from the middle");
    expect(trimmed).toMatch(/not saved/i);
  });

  it("never exceeds the budget, whatever the shape of the input", () => {
    for (const size of [400, 1000, 20000, 120000]) {
      const trimmed = trimToBudget("z".repeat(size), 500, PATH);
      expect(trimmed.length).toBeLessThanOrEqual(500);
    }
  });

  // The report is written to a UTF-8 file and put on the clipboard, and neither
  // can carry half a character. JavaScript indexes strings by UTF-16 code unit,
  // so a cut landing between the two units of an astral character leaves a lone
  // surrogate — which encodes as U+FFFD, putting a � in the middle of a report
  // the reporter did not corrupt and will be asked about. Cyrillic and the rest
  // of the BMP are never at risk here; emoji always are.
  it("never cuts a character in half", () => {
    // A high surrogate not followed by a low one, or a low one not preceded by a
    // high one: the two halves of a pair that survived without the other.
    const LONE_SURROGATE =
      /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/;

    // Two code units each, so shifting the run by one puts every possible split
    // offset under the cut across the pads.
    for (let pad = 0; pad < 8; pad++) {
      const text = "H".repeat(pad) + "🌍".repeat(4000) + "T".repeat(pad);
      const trimmed = trimToBudget(text, 600, PATH);

      expect(trimmed.length).toBeLessThanOrEqual(600);
      expect(trimmed).not.toMatch(LONE_SURROGATE);
      // The round trip a report actually makes: encoding to UTF-8 is what the
      // file write and the clipboard both come down to, and it is where a lone
      // surrogate turns into the replacement character.
      const roundTrip = new TextDecoder().decode(
        new TextEncoder().encode(trimmed),
      );
      expect(roundTrip).toBe(trimmed);
    }
  });
});
