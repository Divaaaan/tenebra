import { describe, expect, it } from "vitest";

import type { CheckStage, NodeCheckResult } from "../api";
import { dominantStage, isUsable, medianRtt, okCount } from "./nodecheck";

function node(targets: Array<[CheckStage, number]>): NodeCheckResult {
  return {
    node: "n",
    targets: targets.map(([stage, rttMs], i) => ({ target: `t${i}`, stage, rttMs })),
  };
}

describe("isUsable", () => {
  it("requires a strict majority of targets to survive", () => {
    expect(isUsable(node([["ok", 10], ["ok", 10], ["probe", 0]]))).toBe(true);
    // Exactly half is not a majority — a node splitting evenly is not something
    // to hand a session to.
    expect(isUsable(node([["ok", 10], ["probe", 0]]))).toBe(false);
    expect(isUsable(node([]))).toBe(false);
  });

  it("rejects the node that serves one incidental target", () => {
    // The 2026-08-18 failure: this exit answered one destination normally and
    // black-holed everything the user actually opened, while passing a TCP ping.
    const lucky = node([
      ["ok", 40],
      ["probe", 0],
      ["probe", 0],
      ["probe", 0],
      ["probe", 0],
    ]);
    expect(isUsable(lucky)).toBe(false);
    expect(okCount(lucky)).toBe(1);
  });
});

describe("medianRtt", () => {
  it("ignores an outlier the way a mean would not", () => {
    // Mean would be 340; the median reports what the link usually feels like.
    expect(medianRtt(node([["ok", 100], ["ok", 20], ["ok", 900]]))).toBe(100);
  });

  it("takes the lower middle sample on even counts, so it stays a real measurement", () => {
    expect(medianRtt(node([["ok", 10], ["ok", 20], ["ok", 30], ["ok", 40]]))).toBe(20);
  });

  it("counts only successful, non-zero samples", () => {
    expect(medianRtt(node([["ok", 50], ["dial", 0], ["handshake", 0]]))).toBe(50);
    expect(medianRtt(node([["dial", 0]]))).toBeNull();
    expect(medianRtt(node([["ok", 0]]))).toBeNull();
  });
});

describe("dominantStage", () => {
  it("reports ok for a usable node", () => {
    expect(dominantStage(node([["ok", 10], ["ok", 12]]))).toBe("ok");
  });

  it("names the failure that hit the most targets, not the first one", () => {
    const mixed = node([
      ["dial", 0],
      ["handshake", 0],
      ["handshake", 0],
    ]);
    expect(dominantStage(mixed)).toBe("handshake");
  });

  it("breaks ties toward the earliest stage, the most fundamental fault", () => {
    const tied = node([["dial", 0], ["probe", 0]]);
    expect(dominantStage(tied)).toBe("dial");
  });
});
