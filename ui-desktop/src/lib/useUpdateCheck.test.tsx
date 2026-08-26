import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { StrictMode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";

import { useUpdateCheck } from "./useUpdateCheck";
import {
  checkForUpdate,
  inAppUpdatesSupported,
  installUpdate,
  notifyUpdateAvailable,
} from "./updates";
import { getUpdateChannel, setUpdateChannel } from "./settings";
import { UPDATE_CHECK_INTERVAL_MS, UPDATE_PULSE_MS } from "./updateSchedule";
import type { ConnectionState } from "../api";
import type { Update } from "@tauri-apps/plugin-updater";

// The hook drives the whole launch flow through these calls; stub the module so
// no test touches the updater plugin or performs a real download. tunnelBusy is
// left real (from ./tunnel) — the gate logic is what we exercise. So is
// ./settings: the schedule's timestamp and failure counter are ordinary
// localStorage, and a stub would hide the very thing that has to survive a
// restart.
vi.mock("./updates", () => ({
  checkForUpdate: vi.fn(),
  inAppUpdatesSupported: vi.fn(),
  installUpdate: vi.fn(),
  notifyUpdateAvailable: vi.fn(),
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
    // Every case but the packaged one runs on an install that can update
    // itself; restoreMocks wipes the factory impl between tests, so re-arm it.
    vi.mocked(inAppUpdatesSupported).mockResolvedValue(true);
  });

  afterEach(() => {
    // Restore real timers here rather than at the end of the test bodies: an
    // assertion that fails half way through must not leak them into the next
    // test.
    vi.useRealTimers();
  });

  it("does not even check on an install that cannot replace itself", async () => {
    // A Linux copy owned by a package manager: the banner's only action would
    // fail, so the check is skipped outright and nothing is offered.
    vi.mocked(inAppUpdatesSupported).mockResolvedValue(false);
    vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());

    const { result } = renderHook(() => useUpdateCheck("idle"));

    await waitFor(() => expect(inAppUpdatesSupported).toHaveBeenCalled());
    expect(checkForUpdate).not.toHaveBeenCalled();
    expect(result.current.available).toBeNull();
    expect(installUpdate).not.toHaveBeenCalled();
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
    // "Later" means this run only: the schedule's own bookkeeping is the whole
    // of what gets written, so the next launch offers the same release again.
    expect(Object.keys(localStorage).sort()).toEqual([
      "tenebra.updateFailures",
      "tenebra.updateLastCheck",
    ]);
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

  // The schedule. A client parked in the tray never remounts this hook, so a
  // check that only ran at mount ran once per install — days on a machine that
  // is only ever woken and slept, across which every patch shipped in between
  // went unseen. The heartbeat decides on the wall clock, so these move the
  // clock rather than counting ticks.
  describe("schedule", () => {
    beforeEach(() => {
      vi.useFakeTimers();
      vi.setSystemTime(new Date("2026-08-24T12:00:00Z"));
    });

    afterEach(() => {
      vi.useRealTimers();
    });

    /** Advance both clocks and let every promise the beat started settle. */
    async function beat(ms = 0) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
      });
    }

    it("checks again once the interval has passed", async () => {
      vi.mocked(checkForUpdate).mockResolvedValue(null);

      renderHook(() => useUpdateCheck("idle"));

      await beat();
      expect(checkForUpdate).toHaveBeenCalledTimes(1);

      await beat(UPDATE_CHECK_INTERVAL_MS);
      expect(checkForUpdate).toHaveBeenCalledTimes(2);
    });

    it("leaves the release host alone in between", async () => {
      vi.mocked(checkForUpdate).mockResolvedValue(null);

      renderHook(() => useUpdateCheck("idle"));
      await beat();

      // Every beat in the interval asks the clock and finds nothing to do; the
      // beat is a comparison, not a request.
      await beat(UPDATE_CHECK_INTERVAL_MS - UPDATE_PULSE_MS);
      expect(checkForUpdate).toHaveBeenCalledTimes(1);
    });

    it("checks on the first beat after a long sleep", async () => {
      vi.mocked(checkForUpdate).mockResolvedValue(null);

      renderHook(() => useUpdateCheck("idle"));
      await beat();
      expect(checkForUpdate).toHaveBeenCalledTimes(1);

      // Suspend: the machine is out for eight hours, so no timer fires and the
      // slept time is gone from every one of them — moving the system clock
      // without ticking is exactly that. A plain interval would now wait out
      // its full period; the wall-clock comparison sees the overdue check on
      // the very next beat.
      vi.setSystemTime(new Date("2026-08-24T20:00:00Z"));
      await beat(UPDATE_PULSE_MS);
      expect(checkForUpdate).toHaveBeenCalledTimes(2);
    });

    it("picks a channel switch up on the next check", async () => {
      // checkForUpdate reads the channel when it is called, so a switch needs
      // no plumbing of its own — but only as long as a later check happens.
      const asked: string[] = [];
      vi.mocked(checkForUpdate).mockImplementation(() => {
        asked.push(getUpdateChannel());
        return Promise.resolve(null);
      });

      renderHook(() => useUpdateCheck("idle"));
      await beat();
      expect(asked).toEqual(["stable"]);

      setUpdateChannel("beta");
      await beat(UPDATE_CHECK_INTERVAL_MS);
      expect(asked).toEqual(["stable", "beta"]);
    });

    it("never checks on an install that cannot replace itself", async () => {
      // A Linux copy owned by a package manager, left running for a day: the
      // schedule must not turn a skipped check into 96 skipped checks, and the
      // one question it does ask is asked once.
      vi.mocked(inAppUpdatesSupported).mockResolvedValue(false);
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate());

      const { result } = renderHook(() => useUpdateCheck("idle"));
      await beat();
      await beat(24 * 60 * 60 * 1000);

      expect(checkForUpdate).not.toHaveBeenCalled();
      expect(inAppUpdatesSupported).toHaveBeenCalledTimes(1);
      expect(result.current.available).toBeNull();
      expect(result.current.stalled).toBe(false);
    });

    it("offers a release found by a later check, not just the first", async () => {
      vi.mocked(checkForUpdate).mockResolvedValueOnce(null);
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate("0.5.6"));

      const { result } = renderHook(() => useUpdateCheck("idle"));
      await beat();
      expect(result.current.available).toBeNull();

      await beat(UPDATE_CHECK_INTERVAL_MS);
      expect(result.current.available).toBe("0.5.6");
    });

    it("re-arms the deferred install for a second release", async () => {
      // The first install fails, so the app is still running when the next
      // release turns up. The deferred fire is latched per release rather than
      // per run: latching it for the session would leave the second one behind
      // an "installs after you disconnect" that never came.
      localStorage.setItem("tenebra.autoInstallUpdates", "1");
      vi.mocked(checkForUpdate).mockResolvedValueOnce(fakeUpdate("0.5.6"));
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate("0.5.7"));
      vi.mocked(installUpdate).mockRejectedValueOnce(new Error("disk full"));
      vi.mocked(installUpdate).mockResolvedValue();

      const { result, rerender } = renderHook(
        ({ phase }: { phase: ConnectionState }) => useUpdateCheck(phase),
        { initialProps: { phase: "connected" as ConnectionState } },
      );

      await beat();
      expect(result.current.deferred).toBe(true);

      // Tunnel down: it fires, fails, and falls back to the banner.
      rerender({ phase: "idle" });
      await beat();
      expect(installUpdate).toHaveBeenCalledTimes(1);
      expect(result.current.available).toBe("0.5.6");

      // Connected again, and the next check finds a newer release.
      rerender({ phase: "connected" });
      await beat(UPDATE_CHECK_INTERVAL_MS);
      expect(result.current.available).toBe("0.5.7");
      expect(result.current.deferred).toBe(true);

      rerender({ phase: "idle" });
      await beat();
      expect(installUpdate).toHaveBeenCalledTimes(2);
    });

    it("does not re-offer a release the user already put off", async () => {
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate("0.5.6"));

      const { result } = renderHook(() => useUpdateCheck("idle"));
      await beat();
      act(() => result.current.dismiss());
      expect(result.current.available).toBeNull();

      // "Later" has to mean later than six hours: the same release coming back
      // round on the next check would undo the dismissal on a timer.
      await beat(UPDATE_CHECK_INTERVAL_MS);
      expect(result.current.available).toBeNull();
    });

    // Three failures in a row is about eighteen hours without a single answer.
    // One is a closed lid.
    describe("checks that keep failing", () => {
      it("says nothing about the first failure", async () => {
        vi.mocked(checkForUpdate).mockRejectedValue(new Error("offline"));

        const { result } = renderHook(() => useUpdateCheck("idle"));
        await beat();

        // An offline launch still looks exactly like an up-to-date one.
        expect(result.current.stalled).toBe(false);
        expect(result.current.available).toBeNull();
      });

      it("surfaces the third failure in a row", async () => {
        vi.mocked(checkForUpdate).mockRejectedValue(new Error("offline"));

        const { result } = renderHook(() => useUpdateCheck("idle"));
        await beat();
        await beat(UPDATE_CHECK_INTERVAL_MS);
        expect(result.current.stalled).toBe(false);

        await beat(UPDATE_CHECK_INTERVAL_MS);
        expect(result.current.stalled).toBe(true);
      });

      it("counts failures across restarts", async () => {
        // The usual shape of this: a client that cannot reach the release host
        // was already offline before it was last started, so a counter that
        // reset on mount would reset precisely when it mattered.
        localStorage.setItem("tenebra.updateFailures", "2");
        vi.mocked(checkForUpdate).mockRejectedValue(new Error("offline"));

        const { result } = renderHook(() => useUpdateCheck("idle"));
        await beat();

        expect(result.current.stalled).toBe(true);
      });

      it("goes quiet again as soon as a check answers", async () => {
        localStorage.setItem("tenebra.updateFailures", "4");
        vi.mocked(checkForUpdate).mockResolvedValue(null);

        const { result } = renderHook(() => useUpdateCheck("idle"));
        await beat();

        expect(result.current.stalled).toBe(false);
        expect(localStorage.getItem("tenebra.updateFailures")).toBe("0");
      });

      it("checks on demand whatever the schedule says", async () => {
        localStorage.setItem("tenebra.updateFailures", "3");
        vi.mocked(checkForUpdate).mockRejectedValueOnce(new Error("offline"));
        vi.mocked(checkForUpdate).mockResolvedValue(null);

        const { result } = renderHook(() => useUpdateCheck("idle"));
        await beat();
        expect(result.current.stalled).toBe(true);
        expect(checkForUpdate).toHaveBeenCalledTimes(1);

        // The notice's own action, seconds after the check it is reporting on:
        // the interval has nowhere near elapsed, and it still checks.
        await act(async () => {
          result.current.checkNow();
          await vi.advanceTimersByTimeAsync(0);
        });

        expect(checkForUpdate).toHaveBeenCalledTimes(2);
        expect(result.current.stalled).toBe(false);
      });

      it("hides the notice for the run on dismiss", async () => {
        localStorage.setItem("tenebra.updateFailures", "3");
        vi.mocked(checkForUpdate).mockRejectedValue(new Error("offline"));

        const { result } = renderHook(() => useUpdateCheck("idle"));
        await beat();
        expect(result.current.stalled).toBe(true);

        act(() => result.current.dismissStalled());
        expect(result.current.stalled).toBe(false);

        // Still hidden after another failure: dismissing is for this run, and
        // the count keeps rising underneath it.
        await beat(UPDATE_CHECK_INTERVAL_MS);
        expect(result.current.stalled).toBe(false);
        expect(localStorage.getItem("tenebra.updateFailures")).toBe("5");
      });
    });
  });

  // The whole point of the schedule is the window nobody is looking at, and a
  // banner drawn into a hidden webview reaches no one. Whether anybody is
  // looking is the shell's call, not this hook's: the renderer would have to
  // answer from `document.visibilityState`, whose value once the tray handler
  // has hidden the window is a property of the embedded browser that nobody
  // here has verified. So the hook offers the toast and the shell drops it when
  // the window is in front of someone.
  describe("announcing a release the user has to act on", () => {
    it("offers the toast for a release that is waiting on the user", async () => {
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate("0.5.6"));

      renderHook(() => useUpdateCheck("idle"));

      await waitFor(() =>
        expect(notifyUpdateAvailable).toHaveBeenCalledWith("0.5.6"),
      );
      expect(notifyUpdateAvailable).toHaveBeenCalledTimes(1);
    });

    it("offers it regardless of what the renderer thinks it can see", async () => {
      // An open window is not a reason to stay silent here: this hook cannot
      // tell an open window from a hidden one, and pretending otherwise is how
      // the toast would go missing on whichever platform reports it differently.
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate("0.5.6"));

      const { result } = renderHook(() => useUpdateCheck("idle"));

      await waitFor(() => expect(result.current.available).toBe("0.5.6"));
      expect(notifyUpdateAvailable).toHaveBeenCalledWith("0.5.6");
    });

    it("notifies once per release, not once per check", async () => {
      vi.useFakeTimers();
      vi.setSystemTime(new Date("2026-08-24T12:00:00Z"));
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate("0.5.6"));

      renderHook(() => useUpdateCheck("idle"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(24 * 60 * 60 * 1000);
      });

      expect(notifyUpdateAvailable).toHaveBeenCalledTimes(1);
    });

    it("says nothing when the update installs itself", async () => {
      // Auto-install with the tunnel down: the release applies and the app
      // relaunches into it. A toast announcing what already happened is noise.
      localStorage.setItem("tenebra.autoInstallUpdates", "1");
      vi.mocked(checkForUpdate).mockResolvedValue(fakeUpdate("0.5.6"));
      vi.mocked(installUpdate).mockResolvedValue();

      renderHook(() => useUpdateCheck("idle"));

      await waitFor(() => expect(installUpdate).toHaveBeenCalled());
      expect(notifyUpdateAvailable).not.toHaveBeenCalled();
    });
  });
});
