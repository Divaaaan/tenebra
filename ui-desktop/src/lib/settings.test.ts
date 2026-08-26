import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  getUpdateChannel,
  getUpdateFailures,
  getUpdateLastCheck,
  migrateLegacyAutoconnect,
  setUpdateChannel,
  setUpdateFailures,
  setUpdateLastCheck,
} from "./settings";

const AUTOCONNECT_KEY = "tenebra.autoconnect";
const LAST_PROFILE_KEY = "tenebra.lastProfile";
const UPDATE_CHANNEL_KEY = "tenebra.updateChannel";
const UPDATE_LAST_CHECK_KEY = "tenebra.updateLastCheck";
const UPDATE_FAILURES_KEY = "tenebra.updateFailures";

describe("migrateLegacyAutoconnect", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("arms the core once when the legacy flag is on and the core is off", async () => {
    localStorage.setItem(AUTOCONNECT_KEY, "1");
    localStorage.setItem(LAST_PROFILE_KEY, "p1");
    const arm = vi.fn().mockResolvedValue(undefined);

    await migrateLegacyAutoconnect(false, arm);

    expect(arm).toHaveBeenCalledTimes(1);
    // Both stale keys are gone; the core owns the preference and records its
    // own last connect.
    expect(localStorage.getItem(AUTOCONNECT_KEY)).toBeNull();
    expect(localStorage.getItem(LAST_PROFILE_KEY)).toBeNull();
  });

  it("does not arm when the core already reports on, but still drops the keys", async () => {
    localStorage.setItem(AUTOCONNECT_KEY, "1");
    localStorage.setItem(LAST_PROFILE_KEY, "p1");
    const arm = vi.fn().mockResolvedValue(undefined);

    await migrateLegacyAutoconnect(true, arm);

    expect(arm).not.toHaveBeenCalled();
    expect(localStorage.getItem(AUTOCONNECT_KEY)).toBeNull();
    expect(localStorage.getItem(LAST_PROFILE_KEY)).toBeNull();
  });

  it("does not arm from a legacy off flag", async () => {
    localStorage.setItem(AUTOCONNECT_KEY, "0");
    const arm = vi.fn().mockResolvedValue(undefined);

    await migrateLegacyAutoconnect(false, arm);

    expect(arm).not.toHaveBeenCalled();
    expect(localStorage.getItem(AUTOCONNECT_KEY)).toBeNull();
  });

  it("is a no-op with no legacy keys present", async () => {
    const arm = vi.fn().mockResolvedValue(undefined);

    await migrateLegacyAutoconnect(false, arm);

    expect(arm).not.toHaveBeenCalled();
  });

  it("keeps the keys when arming fails, so the next launch retries", async () => {
    localStorage.setItem(AUTOCONNECT_KEY, "1");
    localStorage.setItem(LAST_PROFILE_KEY, "p1");
    const arm = vi.fn().mockRejectedValue(new Error("core unreachable"));

    await expect(migrateLegacyAutoconnect(false, arm)).rejects.toThrow(
      "core unreachable",
    );

    expect(localStorage.getItem(AUTOCONNECT_KEY)).toBe("1");
    expect(localStorage.getItem(LAST_PROFILE_KEY)).toBe("p1");
  });
});

describe("update channel", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("defaults to stable when nothing is stored", () => {
    expect(getUpdateChannel()).toBe("stable");
  });

  it("returns beta once the beta channel is chosen", () => {
    setUpdateChannel("beta");

    expect(localStorage.getItem(UPDATE_CHANNEL_KEY)).toBe("beta");
    expect(getUpdateChannel()).toBe("beta");
  });

  it("round-trips back to stable", () => {
    setUpdateChannel("beta");
    setUpdateChannel("stable");

    expect(localStorage.getItem(UPDATE_CHANNEL_KEY)).toBe("stable");
    expect(getUpdateChannel()).toBe("stable");
  });

  it("falls back to stable for any value that isn't exactly beta", () => {
    // A stale or corrupted entry must never silently opt someone into
    // prereleases: only the exact string "beta" selects the beta channel.
    for (const value of ["Beta", "BETA", "nightly", "1", ""]) {
      localStorage.setItem(UPDATE_CHANNEL_KEY, value);
      expect(getUpdateChannel()).toBe("stable");
    }
  });
});

// The schedule's own two values. They are read on every beat and after every
// restart, so what a corrupt entry resolves to is the whole of the story: a
// junk timestamp must not wedge the client on its installed version, and a
// junk counter must not pin a notice on screen that nothing can clear.
describe("update check bookkeeping", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("reports no last check before the first one", () => {
    expect(getUpdateLastCheck()).toBeNull();
  });

  it("round-trips a reading of the clock", () => {
    const at = Date.parse("2026-08-24T12:00:00Z");
    setUpdateLastCheck(at);

    expect(getUpdateLastCheck()).toBe(at);
  });

  it("reads a garbage timestamp as never checked", () => {
    // Never-checked is the safe resolution: it makes a check due now, which
    // rewrites the entry with a real reading.
    for (const value of ["", "soon", "NaN", "0", "-1", "{}"]) {
      localStorage.setItem(UPDATE_LAST_CHECK_KEY, value);
      expect(getUpdateLastCheck()).toBeNull();
    }
  });

  it("starts the failure run at zero", () => {
    expect(getUpdateFailures()).toBe(0);
  });

  it("round-trips a run of failures", () => {
    setUpdateFailures(3);

    expect(localStorage.getItem(UPDATE_FAILURES_KEY)).toBe("3");
    expect(getUpdateFailures()).toBe(3);
  });

  it("reads a garbage counter as no failures at all", () => {
    for (const value of ["", "lots", "-2", "2.5", "true"]) {
      localStorage.setItem(UPDATE_FAILURES_KEY, value);
      expect(getUpdateFailures()).toBe(0);
    }
  });
});
