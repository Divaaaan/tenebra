import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  checkForUpdate,
  inAppUpdatesSupported,
  installUpdate,
  notifyUpdateAvailable,
} from "./updates";
import {
  check,
  type DownloadEvent,
  type Update,
} from "@tauri-apps/plugin-updater";
import { relaunch } from "@tauri-apps/plugin-process";
import { invoke } from "@tauri-apps/api/core";

// installUpdate ends by restarting the app through the process plugin, and
// checkForUpdate asks the updater plugin (stable) or the backend command (beta)
// whether a signed release exists. Stub all three so the unit under test never
// touches a real Tauri host. Update is stubbed as a light class so the beta path
// can reconstitute a handle from backend metadata without the real plugin.
vi.mock("@tauri-apps/plugin-updater", () => ({
  check: vi.fn(),
  Update: class {
    constructor(metadata: Record<string, unknown>) {
      Object.assign(this, metadata);
    }
  },
}));
vi.mock("@tauri-apps/plugin-process", () => ({
  relaunch: vi.fn(),
}));
vi.mock("@tauri-apps/api/core", () => ({
  invoke: vi.fn(),
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

describe("checkForUpdate channel resolution", () => {
  beforeEach(() => {
    // The channel lives in localStorage; start each case on the default.
    localStorage.clear();
  });

  it("uses the plugin's own check for the stable channel by default", async () => {
    const update = { version: "1.0.0" } as unknown as Update;
    vi.mocked(check).mockResolvedValue(update);

    await expect(checkForUpdate()).resolves.toBe(update);
    // Stable reads the endpoint baked into tauri.conf.json, so it never calls
    // the channel command — the path installed clients have shipped since 0.3.0.
    expect(invoke).not.toHaveBeenCalled();
  });

  it("routes the beta channel through the backend command", async () => {
    localStorage.setItem("tenebra.updateChannel", "beta");
    vi.mocked(invoke).mockResolvedValue({
      rid: 7,
      currentVersion: "1.0.0",
      version: "1.1.0-beta.1",
      rawJson: {},
    });

    const result = await checkForUpdate();

    // Beta asks the backend to point the updater at beta.json for this check.
    expect(invoke).toHaveBeenCalledWith("check_update_for_channel", {
      channel: "beta",
    });
    // The plugin's fixed-endpoint check() is skipped on beta.
    expect(check).not.toHaveBeenCalled();
    // The metadata is rebuilt into an Update handle carrying the found version.
    expect(result).toMatchObject({ version: "1.1.0-beta.1", rid: 7 });
  });

  it("reports up to date on beta when the backend finds nothing newer", async () => {
    localStorage.setItem("tenebra.updateChannel", "beta");
    vi.mocked(invoke).mockResolvedValue(null);

    await expect(checkForUpdate()).resolves.toBeNull();
    expect(check).not.toHaveBeenCalled();
  });
});

// The updater can only replace the artifact the app was installed from, which on
// Linux means an AppImage and nothing else. The backend answers that question;
// the UI leans on it to decide whether to offer the flow at all, so a wrong
// answer here is either a dead button or a hidden updater.
describe("inAppUpdatesSupported", () => {
  it("passes the backend's answer through", async () => {
    vi.mocked(invoke).mockResolvedValue(true);
    await expect(inAppUpdatesSupported()).resolves.toBe(true);
    expect(invoke).toHaveBeenCalledWith("in_app_updates_supported");

    vi.mocked(invoke).mockResolvedValue(false);
    await expect(inAppUpdatesSupported()).resolves.toBe(false);
  });

  it("assumes a self-updating install when nothing answers", async () => {
    // No backend (an older shell, a test host): hiding a working updater would
    // be the worse of the two mistakes, so the answer defaults to the behaviour
    // every build had before the question existed.
    vi.mocked(invoke).mockRejectedValue(new Error("no such command"));
    await expect(inAppUpdatesSupported()).resolves.toBe(true);
  });
});

// The toast for a window minimised to the tray. The shell writes the wording
// (a system notification is drawn outside the webview, so it is translated
// where the tray labels are); this side only says which release.
describe("notifyUpdateAvailable", () => {
  it("hands the release to the shell", async () => {
    vi.mocked(invoke).mockResolvedValue(undefined);

    await notifyUpdateAvailable("0.5.6");

    expect(invoke).toHaveBeenCalledWith("notify_update_available", {
      version: "0.5.6",
    });
  });

  it("swallows a shell that has no such command", async () => {
    // An older shell, a browser preview, or an OS with notifications off. None
    // of them is a reason to fail the check that produced the release.
    vi.mocked(invoke).mockRejectedValue(new Error("no such command"));

    await expect(notifyUpdateAvailable("0.5.6")).resolves.toBeUndefined();
  });
});
