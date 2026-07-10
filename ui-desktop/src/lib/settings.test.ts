import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  getUpdateChannel,
  migrateLegacyAutoconnect,
  setUpdateChannel,
} from "./settings";

const AUTOCONNECT_KEY = "tenebra.autoconnect";
const LAST_PROFILE_KEY = "tenebra.lastProfile";
const UPDATE_CHANNEL_KEY = "tenebra.updateChannel";

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
