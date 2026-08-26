import { describe, expect, it } from "vitest";

import {
  UPDATE_CHECK_INTERVAL_MS,
  UPDATE_FAILURE_LIMIT,
  UPDATE_PULSE_MS,
  isCheckDue,
  isCheckStalled,
} from "./updateSchedule";

// A fixed reading rather than Date.now(): every case here is arithmetic on two
// numbers, and the whole point of the module is that it never asks a clock.
const NOW = Date.parse("2026-08-24T12:00:00Z");
const HOUR = 60 * 60 * 1000;

describe("isCheckDue", () => {
  it("is due when nothing has ever been checked", () => {
    expect(isCheckDue(NOW, null)).toBe(true);
  });

  it("is not due right after a check", () => {
    expect(isCheckDue(NOW, NOW)).toBe(false);
  });

  it("is not due part way through the interval", () => {
    expect(isCheckDue(NOW, NOW - UPDATE_CHECK_INTERVAL_MS + 1)).toBe(false);
  });

  it("is due exactly on the interval", () => {
    expect(isCheckDue(NOW, NOW - UPDATE_CHECK_INTERVAL_MS)).toBe(true);
  });

  it("is due once the interval is past", () => {
    expect(isCheckDue(NOW, NOW - 8 * HOUR)).toBe(true);
  });

  it("takes the interval it is given", () => {
    expect(isCheckDue(NOW, NOW - 2 * HOUR, HOUR)).toBe(true);
    expect(isCheckDue(NOW, NOW - 2 * HOUR, 6 * HOUR)).toBe(false);
  });

  // The one that decides whether a client can wedge itself. A stamp in the
  // future means the clock moved backwards — a corrected NTP sync, a machine
  // that came up with a dead RTC, a restored backup — or the entry is junk.
  // Waiting for `now` to climb back to it could take years, and a VPN client
  // stuck on an old build is exactly what this schedule exists to prevent.
  it("is due when the stamp is in the future", () => {
    expect(isCheckDue(NOW, NOW + 1)).toBe(true);
    expect(isCheckDue(NOW, NOW + 365 * 24 * HOUR)).toBe(true);
  });

  it("is due for a stamp that is not a real reading", () => {
    expect(isCheckDue(NOW, Number.NaN)).toBe(true);
    expect(isCheckDue(NOW, Number.POSITIVE_INFINITY)).toBe(true);
  });
});

describe("isCheckStalled", () => {
  it("says nothing until the limit is reached", () => {
    expect(isCheckStalled(0)).toBe(false);
    expect(isCheckStalled(1)).toBe(false);
    expect(isCheckStalled(UPDATE_FAILURE_LIMIT - 1)).toBe(false);
  });

  it("holds from the limit on", () => {
    expect(isCheckStalled(UPDATE_FAILURE_LIMIT)).toBe(true);
    expect(isCheckStalled(UPDATE_FAILURE_LIMIT + 10)).toBe(true);
  });

  it("takes the limit it is given", () => {
    expect(isCheckStalled(2, 2)).toBe(true);
    expect(isCheckStalled(2, 5)).toBe(false);
  });
});

describe("the constants they are used with", () => {
  it("beats often enough to be a heartbeat, not a second schedule", () => {
    // A beat that approached the interval would put the wake-from-sleep case
    // back where it started: hours of latency before an overdue check runs.
    expect(UPDATE_PULSE_MS).toBeLessThan(UPDATE_CHECK_INTERVAL_MS / 4);
  });

  it("waits the best part of a day before calling checks broken", () => {
    // The notice has to outlast a closed lid and a train tunnel; three misses
    // at six hours is around eighteen.
    expect(UPDATE_FAILURE_LIMIT * UPDATE_CHECK_INTERVAL_MS).toBeGreaterThan(
      12 * HOUR,
    );
  });
});
