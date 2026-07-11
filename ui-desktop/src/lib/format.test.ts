import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  formatBytes,
  formatDate,
  formatExpiry,
  formatTime,
  formatTrafficUsage,
  trafficUsedFraction,
} from "./format";

describe("formatBytes", () => {
  it("returns 0 B for zero, negatives, and non-finite input", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(-1)).toBe("0 B");
    expect(formatBytes(NaN)).toBe("0 B");
    expect(formatBytes(Infinity)).toBe("0 B");
  });

  it("keeps bytes whole and unsuffixed below 1 KB", () => {
    expect(formatBytes(1)).toBe("1 B");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1023)).toBe("1023 B");
  });

  it("crosses to KB at 1024 with one decimal", () => {
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(1536)).toBe("1.5 KB");
  });

  it("drops the decimal once the value reaches 100 in a unit", () => {
    expect(formatBytes(100 * 1024)).toBe("100 KB");
    expect(formatBytes(99.4 * 1024)).toBe("99.4 KB");
  });

  it("scales through MB, GB and TB", () => {
    expect(formatBytes(1024 ** 2)).toBe("1.0 MB");
    expect(formatBytes(1024 ** 3)).toBe("1.0 GB");
    expect(formatBytes(1024 ** 4)).toBe("1.0 TB");
  });

  it("clamps past the largest unit instead of inventing one", () => {
    // 1 PB has no unit; it stays expressed in TB (the last entry).
    expect(formatBytes(1024 ** 5)).toBe("1024 TB");
  });
});

describe("formatDate", () => {
  it("returns an em dash for missing or unparseable timestamps", () => {
    expect(formatDate(undefined, "en")).toBe("—");
    expect(formatDate("not-a-date", "en")).toBe("—");
  });

  it("formats a valid timestamp per the requested language", () => {
    const en = formatDate("2026-03-14T00:00:00Z", "en");
    expect(en).toMatch(/Mar/);
    expect(en).toMatch(/2026/);

    const ru = formatDate("2026-03-14T00:00:00Z", "ru");
    // ru-RU short month for March; assert the year travels through.
    expect(ru).toMatch(/2026/);
    expect(ru).not.toBe(en);
  });
});

describe("formatTime", () => {
  it("renders a 24-hour wall-clock time", () => {
    const date = new Date("2026-03-14T14:03:21Z");
    const out = formatTime(date, "en");
    // Local tz shifts the hour, so assert the format, not the exact value.
    expect(out).toMatch(/^\d{1,2}:\d{2}:\d{2}$/);
  });
});

describe("formatExpiry", () => {
  const labels = {
    in: "in {days} days",
    today: "today",
    tomorrow: "tomorrow",
    expired: "expired",
  };

  beforeEach(() => {
    vi.useFakeTimers();
    // Fix "now" to a stable wall clock so day math is deterministic.
    vi.setSystemTime(new Date("2026-06-15T12:00:00Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("returns null for missing or invalid input so the field can hide", () => {
    expect(formatExpiry(undefined, "en", labels)).toBeNull();
    expect(formatExpiry("nonsense", "en", labels)).toBeNull();
  });

  it("reports an elapsed timestamp as expired", () => {
    expect(formatExpiry("2026-06-14T12:00:00Z", "en", labels)).toBe("expired");
  });

  it("calls the same calendar day today, regardless of the hour", () => {
    expect(formatExpiry("2026-06-15T23:59:00Z", "en", labels)).toBe("today");
    expect(formatExpiry("2026-06-15T00:01:00Z", "en", labels)).toBe("today");
  });

  it("calls the next calendar day tomorrow", () => {
    expect(formatExpiry("2026-06-16T06:00:00Z", "en", labels)).toBe("tomorrow");
  });

  it("interpolates the day count in the near term", () => {
    expect(formatExpiry("2026-06-20T12:00:00Z", "en", labels)).toBe(
      "in 5 days",
    );
  });

  it("includes the boundary day at 31 out", () => {
    expect(formatExpiry("2026-07-16T12:00:00Z", "en", labels)).toBe(
      "in 31 days",
    );
  });

  it("switches to an absolute date once past a month", () => {
    const out = formatExpiry("2026-08-01T12:00:00Z", "en", labels);
    expect(out).not.toMatch(/in \d+ days/);
    expect(out).toMatch(/Aug/);
    expect(out).toMatch(/2026/);
  });
});

describe("formatTrafficUsage", () => {
  it("returns null when neither used nor total is known", () => {
    expect(formatTrafficUsage(undefined, undefined)).toBeNull();
  });

  it("shows just the used figure when the plan is unmetered (total 0 or absent)", () => {
    expect(formatTrafficUsage(1.5 * 1024 ** 3, 0)).toBe("1.5 GB");
    expect(formatTrafficUsage(1.5 * 1024 ** 3, undefined)).toBe("1.5 GB");
  });

  it("treats a missing used value as zero against a known total", () => {
    expect(formatTrafficUsage(undefined, 50 * 1024 ** 3)).toBe("0 B / 50.0 GB");
  });

  it("formats a used/total pair", () => {
    expect(formatTrafficUsage(1.2 * 1024 ** 3, 50 * 1024 ** 3)).toBe(
      "1.2 GB / 50.0 GB",
    );
  });

  it("drops the decimal on a total of 100 or more", () => {
    expect(formatTrafficUsage(1.2 * 1024 ** 3, 200 * 1024 ** 3)).toBe(
      "1.2 GB / 200 GB",
    );
  });
});

describe("trafficUsedFraction", () => {
  it("returns null without a metered total to measure against", () => {
    expect(trafficUsedFraction(5, 0)).toBeNull();
    expect(trafficUsedFraction(5, undefined)).toBeNull();
    expect(trafficUsedFraction(undefined, 100)).toBeNull();
  });

  it("returns the consumed fraction of a metered plan", () => {
    expect(trafficUsedFraction(25, 100)).toBe(0.25);
    expect(trafficUsedFraction(50, 200)).toBe(0.25);
  });

  it("clamps at a full bar when usage meets or exceeds the cap", () => {
    expect(trafficUsedFraction(100, 100)).toBe(1);
    expect(trafficUsedFraction(150, 100)).toBe(1);
  });

  it("floors a negative or non-finite used value at empty", () => {
    expect(trafficUsedFraction(-10, 100)).toBe(0);
    expect(trafficUsedFraction(NaN, 100)).toBe(0);
  });
});
