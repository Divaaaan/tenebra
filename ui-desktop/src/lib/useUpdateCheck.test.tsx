import { beforeEach, describe, expect, it, vi } from "vitest";
import { StrictMode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";

import { useUpdateCheck } from "./useUpdateCheck";
import { checkForUpdate, installUpdate } from "./updates";
import type { ConnectionState } from "../api";
import type { Update } from "@tauri-apps/plugin-updater";

// The hook drives the whole launch flow through these two calls; stub the
// module so no test touches the updater plugin or performs a real download.
// tunnelBusy is left real (from ./tunnel) — the gate logic is what we exercise.
vi.mock("./updates", () => ({
  checkForUpdate: vi.fn(),
  installUpdate: vi.fn(),
}));

// Only the version is read off the handle before it is passed back to
// installUpdate, so a bare object stands in for the plugin's Update.
function fakeUpdate(version = "9.9.9"): Update {
  return { version } as unknown as Update;
}

describe("useUpdateCheck", () => {
  beforeEach(() => {
    // The auto-install preference lives in localStorage; start each test clean.
    localStorage.clear();
  });

  it("surfaces the found version for the banner", async () => {
    vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());

    const { result } = renderHook(() => useUpdateCheck("idle"));

    await waitFor(() => expect(result.current.available).toBe("9.9.9"));
    // Auto-install is off by default, so nothing may install on its own.
    expect(installUpdate).not.toHaveBeenCalled();
  });

  it("shows nothing when already on the latest version", async () => {
    vi.mocked(checkForUpdate).mockResolvedValue(null);

    const { result } = renderHook(() => useUpdateCheck("idle"));

    await waitFor(() => expect(checkForUpdate).toHaveBeenCalled());
    expect(result.current.available).toBeNull();
  });

  it("swallows a failed check so an offline launch stays silent", async () => {
    vi.mocked(checkForUpdate).mockRejectedValue(new Error("offline"));

    const { result } = renderHook(() => useUpdateCheck("idle"));

    await waitFor(() => expect(checkForUpdate).toHaveBeenCalled());
    expect(result.current.available).toBeNull();
    expect(installUpdate).not.toHaveBeenCalled();
  });

  it("checks once per run, surviving StrictMode's double effect", async () => {
    vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());

    // StrictMode replays the mount effect (setup → cleanup → setup); the hook
    // must not fire a second check — with auto-install on that would race two
    // installs of the same release.
    const { result, rerender } = renderHook(() => useUpdateCheck("idle"), {
      wrapper: StrictMode,
    });

    await waitFor(() => expect(result.current.available).toBe("9.9.9"));
    rerender();
    expect(checkForUpdate).toHaveBeenCalledTimes(1);
  });

  it("hides the banner on dismiss without persisting anything", async () => {
    vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());

    const { result } = renderHook(() => useUpdateCheck("idle"));
    await waitFor(() => expect(result.current.available).toBe("9.9.9"));

    act(() => result.current.dismiss());

    expect(result.current.available).toBeNull();
    // "Later" means this run only: nothing is written, so the next launch
    // offers the same release again.
    expect(localStorage.length).toBe(0);
  });

  it("installs on the banner action and reports download progress", async () => {
    const update = fakeUpdate();
    vi.mocked(checkForUpdate).mockResolvedValue(update);
    let sendProgress!: (percent: number | null) => void;
    vi.mocked(installUpdate).mockImplementation((_update, onProgress) => {
      sendProgress = (percent) => onProgress?.(percent);
      // Never settles: a successful install ends in a relaunch, so from the
      // hook's point of view the promise simply stays pending.
      return new Promise<void>(() => {});
    });

    const { result } = renderHook(() => useUpdateCheck("idle"));
    await waitFor(() => expect(result.current.available).toBe("9.9.9"));

    act(() => result.current.install());

    expect(result.current.installing).toBe(true);
    expect(installUpdate).toHaveBeenCalledWith(update, expect.any(Function));

    act(() => sendProgress(42));
    expect(result.current.progress).toBe(42);
  });

  it("keeps the banner and unlocks install when the download fails", async () => {
    vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());
    vi.mocked(installUpdate).mockRejectedValue(new Error("disk full"));

    const { result } = renderHook(() => useUpdateCheck("idle"));
    await waitFor(() => expect(result.current.available).toBe("9.9.9"));

    act(() => result.current.install());

    await waitFor(() => expect(result.current.installing).toBe(false));
    // The release stays offered so the action can be retried.
    expect(result.current.available).toBe("9.9.9");
  });

  it("installs immediately without a banner when auto-install is on", async () => {
    localStorage.setItem("tenebra.autoInstallUpdates", "1");
    const update = fakeUpdate();
    vi.mocked(checkForUpdate).mockResolvedValue(update);
    vi.mocked(installUpdate).mockResolvedValue();

    const { result } = renderHook(() => useUpdateCheck("idle"));

    // The silent path passes no progress callback — there is no banner to feed.
    await waitFor(() => expect(installUpdate).toHaveBeenCalledWith(update));
    expect(result.current.available).toBeNull();
  });

  it("falls back to the banner when the silent install fails", async () => {
    localStorage.setItem("tenebra.autoInstallUpdates", "1");
    vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());
    vi.mocked(installUpdate).mockRejectedValue(new Error("network down"));

    const { result } = renderHook(() => useUpdateCheck("idle"));

    // A failed silent install must leave the update discoverable by hand.
    await waitFor(() => expect(result.current.available).toBe("9.9.9"));
  });

  // The tunnel gate: an install relaunches the app (and on Windows stops the
  // service), which would drop a live VPN. So it never fires while the tunnel is
  // up — auto-install waits for it to drop, and a manual install asks first.
  describe("tunnel gate", () => {
    it("holds the auto-install while a tunnel is up and shows the deferred banner", async () => {
      localStorage.setItem("tenebra.autoInstallUpdates", "1");
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());
      vi.mocked(installUpdate).mockResolvedValue();

      const { result } = renderHook(() => useUpdateCheck("connected"));

      // The banner surfaces in its deferred state instead of installing.
      await waitFor(() => expect(result.current.deferred).toBe(true));
      expect(result.current.available).toBe("9.9.9");
      expect(installUpdate).not.toHaveBeenCalled();
    });

    it("runs the deferred auto-install once the tunnel goes down", async () => {
      localStorage.setItem("tenebra.autoInstallUpdates", "1");
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());
      vi.mocked(installUpdate).mockResolvedValue();

      const { result, rerender } = renderHook(
        ({ phase }: { phase: ConnectionState }) => useUpdateCheck(phase),
        { initialProps: { phase: "connected" as ConnectionState } },
      );

      await waitFor(() => expect(result.current.deferred).toBe(true));
      expect(installUpdate).not.toHaveBeenCalled();

      // The user disconnects: the held install applies on its own.
      rerender({ phase: "idle" });
      await waitFor(() => expect(installUpdate).toHaveBeenCalledTimes(1));
    });

    it("does not fire the deferred install on a mid-connect transition", async () => {
      localStorage.setItem("tenebra.autoInstallUpdates", "1");
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());
      vi.mocked(installUpdate).mockResolvedValue();

      const { result, rerender } = renderHook(
        ({ phase }: { phase: ConnectionState }) => useUpdateCheck(phase),
        { initialProps: { phase: "connected" as ConnectionState } },
      );

      await waitFor(() => expect(result.current.deferred).toBe(true));

      // connecting and health_reconnecting are still "busy" — the tunnel is not
      // safely down, so the install must keep waiting.
      rerender({ phase: "connecting" });
      rerender({ phase: "health_reconnecting" });
      expect(installUpdate).not.toHaveBeenCalled();
    });

    it("asks before a manual install while a tunnel is up", async () => {
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());
      vi.mocked(installUpdate).mockResolvedValue();

      const { result } = renderHook(() => useUpdateCheck("connected"));
      await waitFor(() => expect(result.current.available).toBe("9.9.9"));

      // The banner action opens the confirm rather than cutting the tunnel.
      act(() => result.current.install());
      expect(result.current.confirming).toBe(true);
      expect(installUpdate).not.toHaveBeenCalled();

      // Approving it goes ahead and installs.
      act(() => result.current.confirmInstall());
      expect(result.current.confirming).toBe(false);
      await waitFor(() => expect(installUpdate).toHaveBeenCalledTimes(1));
    });

    it("drops the confirm without installing on cancel", async () => {
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());
      vi.mocked(installUpdate).mockResolvedValue();

      const { result } = renderHook(() => useUpdateCheck("connected"));
      await waitFor(() => expect(result.current.available).toBe("9.9.9"));

      act(() => result.current.install());
      expect(result.current.confirming).toBe(true);

      act(() => result.current.cancelInstall());
      expect(result.current.confirming).toBe(false);
      expect(installUpdate).not.toHaveBeenCalled();
      // The release stays on offer for later.
      expect(result.current.available).toBe("9.9.9");
    });

    it("installs a manual update straight away when the tunnel is down", async () => {
      const update = fakeUpdate();
      vi.mocked(checkForUpdate).mockResolvedValue(update);
      vi.mocked(installUpdate).mockResolvedValue();

      const { result } = renderHook(() => useUpdateCheck("idle"));
      await waitFor(() => expect(result.current.available).toBe("9.9.9"));

      act(() => result.current.install());
      // No tunnel to protect, so no confirm — it installs directly.
      expect(result.current.confirming).toBe(false);
      await waitFor(() =>
        expect(installUpdate).toHaveBeenCalledWith(update, expect.any(Function)),
      );
    });
  });
});
