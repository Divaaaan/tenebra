import { describe, expect, it, vi } from "vitest";

import { checkForUpdate, installUpdate } from "./updates";
import {
  check,
  type DownloadEvent,
  type Update,
} from "@tauri-apps/plugin-updater";
import { relaunch } from "@tauri-apps/plugin-process";

// installUpdate ends by restarting the app through the process plugin, and
// checkForUpdate asks the updater plugin whether a signed release exists. Stub
// both so the unit under test never touches a real Tauri host.
vi.mock("@tauri-apps/plugin-updater", () => ({
  check: vi.fn(),
}));
vi.mock("@tauri-apps/plugin-process", () => ({
  relaunch: vi.fn(),
}));

// A stand-in for the plugin's Update handle. downloadAndInstall replays a
// scripted list of download events through the caller's callback, exactly as
// the real plugin streams them in from the Rust side, then resolves.
function fakeUpdate(events: DownloadEvent[]): Update {
  return {
    version: "1.2.3",
    downloadAndInstall: vi.fn(
      async (onEvent?: (event: DownloadEvent) => void): Promise<void> => {
        for (const event of events) {
          onEvent?.(event);
        }
      },
    ),
  } as unknown as Update;
}

describe("installUpdate", () => {
  it("reports rounded integer percent as chunks arrive when the length is known", async () => {
    const percents: (number | null)[] = [];
    const update = fakeUpdate([
      { event: "Started", data: { contentLength: 200 } },
      { event: "Progress", data: { chunkLength: 50 } },
      { event: "Progress", data: { chunkLength: 50 } },
      { event: "Progress", data: { chunkLength: 33 } },
      { event: "Finished" },
    ]);

    await installUpdate(update, (p) => percents.push(p));

    // Started with a known length seeds 0; each Progress reports the rounded
    // cumulative fraction; Finished pins 100. The third tick is the telling
    // one: 133/200 = 66.5, which Math.round lifts to 67 (a floor would give 66).
    expect(percents).toEqual([0, 25, 50, 67, 100]);
  });

  it("reports null throughout when the server sends a zero content length", async () => {
    const percents: (number | null)[] = [];
    const update = fakeUpdate([
      { event: "Started", data: { contentLength: 0 } },
      { event: "Progress", data: { chunkLength: 40 } },
      { event: "Progress", data: { chunkLength: 60 } },
      { event: "Finished" },
    ]);

    await installUpdate(update, (p) => percents.push(p));

    // With nothing to divide by, every in-flight tick is null rather than a
    // NaN or Infinity from x/0; only Finished forces the terminal 100.
    expect(percents).toEqual([null, null, null, 100]);
  });

  it("treats a missing content length the same as zero", async () => {
    const percents: (number | null)[] = [];
    const update = fakeUpdate([
      // The plugin omits contentLength entirely when the server sends none.
      { event: "Started", data: {} },
      { event: "Progress", data: { chunkLength: 10 } },
      { event: "Finished" },
    ]);

    await installUpdate(update, (p) => percents.push(p));

    // `contentLength ?? 0` makes an absent length behave like a zero one:
    // indeterminate progress, still no division.
    expect(percents).toEqual([null, null, 100]);
  });

  it("relaunches the app once the install completes", async () => {
    const update = fakeUpdate([
      { event: "Started", data: { contentLength: 100 } },
      { event: "Progress", data: { chunkLength: 100 } },
      { event: "Finished" },
    ]);

    await installUpdate(update);

    expect(relaunch).toHaveBeenCalledTimes(1);
  });

  it("works without a progress callback", async () => {
    const update = fakeUpdate([
      { event: "Started", data: { contentLength: 100 } },
      { event: "Progress", data: { chunkLength: 100 } },
      { event: "Finished" },
    ]);

    // The onProgress argument is optional; the optional-call guards must not
    // throw when it is absent.
    await expect(installUpdate(update)).resolves.toBeUndefined();
    expect(relaunch).toHaveBeenCalledTimes(1);
  });

  it("propagates a download failure without relaunching", async () => {
    const update = {
      version: "1.2.3",
      downloadAndInstall: vi.fn().mockRejectedValue(new Error("network down")),
    } as unknown as Update;

    await expect(installUpdate(update)).rejects.toThrow("network down");
    // The failure must surface to the caller and short-circuit the restart.
    expect(relaunch).not.toHaveBeenCalled();
  });
});

describe("checkForUpdate", () => {
  it("returns the update handle when a newer release is available", async () => {
    const update = { version: "9.9.9" } as unknown as Update;
    vi.mocked(check).mockResolvedValue(update);

    await expect(checkForUpdate()).resolves.toBe(update);
  });

  it("returns null when already on the latest version", async () => {
    vi.mocked(check).mockResolvedValue(null);

    await expect(checkForUpdate()).resolves.toBeNull();
  });
});
