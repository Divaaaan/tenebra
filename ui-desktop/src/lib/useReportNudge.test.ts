import { describe, expect, it, beforeEach, afterEach, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";

import { useReportNudge, NUDGE_KEY } from "./useReportNudge";
import type { ConnectionState, ServiceCheck } from "../api";

// A prompt that fires when it shouldn't teaches the user to dismiss it without
// reading, and then it is worth nothing on the day it is right. Two things keep
// it honest: it waits for a second failing check in a row, and it speaks at most
// once a day.

const DAY_MS = 24 * 60 * 60 * 1000;

function video(ok: boolean): ServiceCheck[] {
  return [
    { service: "video", ok, rttMs: ok ? 40 : 0, detail: "youtube" },
    { service: "voice", ok: true, rttMs: 30, detail: "discord" },
  ];
}

function setup(
  checks: ServiceCheck[] = [],
  runs = 0,
  phase: ConnectionState = "connected",
) {
  return renderHook(
    (props: { phase: ConnectionState; checks: ServiceCheck[]; runs: number }) =>
      useReportNudge(props.phase, props.checks, props.runs),
    { initialProps: { phase, checks, runs } },
  );
}

describe("useReportNudge", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("says nothing while video works", () => {
    const { result, rerender } = setup();
    rerender({ phase: "connected", checks: video(true), runs: 1 });
    rerender({ phase: "connected", checks: video(true), runs: 2 });

    expect(result.current.visible).toBe(false);
  });

  // One failure is a flake often enough that asking about it is noise.
  it("holds its tongue after a single failing check", () => {
    const { result, rerender } = setup();
    rerender({ phase: "connected", checks: video(false), runs: 1 });

    expect(result.current.visible).toBe(false);
  });

  it("offers to report after two failing checks in a row", () => {
    const { result, rerender } = setup();
    rerender({ phase: "connected", checks: video(false), runs: 1 });
    rerender({ phase: "connected", checks: video(false), runs: 2 });

    expect(result.current.visible).toBe(true);
  });

  it("starts counting again when video comes back", () => {
    const { result, rerender } = setup();
    rerender({ phase: "connected", checks: video(false), runs: 1 });
    rerender({ phase: "connected", checks: video(true), runs: 2 });
    rerender({ phase: "connected", checks: video(false), runs: 3 });

    expect(result.current.visible).toBe(false);
  });

  // A check that reported nothing about video says nothing either way; it must
  // not count as a failure.
  it("ignores a run that produced no video verdict", () => {
    const { result, rerender } = setup();
    rerender({ phase: "connected", checks: video(false), runs: 1 });
    rerender({ phase: "connected", checks: [], runs: 2 });

    expect(result.current.visible).toBe(false);
  });

  it("stays quiet when the tunnel isn't up", () => {
    const { result, rerender } = setup([], 0, "idle");
    rerender({ phase: "idle", checks: video(false), runs: 1 });
    rerender({ phase: "idle", checks: video(false), runs: 2 });

    expect(result.current.visible).toBe(false);
  });

  it("drops the prompt when the connection it was about ends", () => {
    const { result, rerender } = setup();
    rerender({ phase: "connected", checks: video(false), runs: 1 });
    rerender({ phase: "connected", checks: video(false), runs: 2 });
    expect(result.current.visible).toBe(true);

    rerender({ phase: "idle", checks: [], runs: 2 });
    expect(result.current.visible).toBe(false);
  });

  // Shown once, then silent for the day — whatever the user did with it. Waiting
  // for a dismissal to start the clock means an ignored prompt returns on every
  // reconnect, which is how a nudge trains itself to be ignored.
  it("speaks at most once a day, even when ignored", () => {
    const first = setup();
    first.rerender({ phase: "connected", checks: video(false), runs: 1 });
    first.rerender({ phase: "connected", checks: video(false), runs: 2 });
    expect(first.result.current.visible).toBe(true);
    expect(localStorage.getItem(NUDGE_KEY)).not.toBeNull();
    first.unmount();

    const second = setup();
    second.rerender({ phase: "connected", checks: video(false), runs: 1 });
    second.rerender({ phase: "connected", checks: video(false), runs: 2 });
    expect(second.result.current.visible).toBe(false);
  });

  it("may speak again once the day has passed", () => {
    localStorage.setItem(NUDGE_KEY, String(Date.now() - DAY_MS - 1000));

    const { result, rerender } = setup();
    rerender({ phase: "connected", checks: video(false), runs: 1 });
    rerender({ phase: "connected", checks: video(false), runs: 2 });

    expect(result.current.visible).toBe(true);
  });

  it("hides on dismissal without asking again today", () => {
    const { result, rerender } = setup();
    rerender({ phase: "connected", checks: video(false), runs: 1 });
    rerender({ phase: "connected", checks: video(false), runs: 2 });
    expect(result.current.visible).toBe(true);

    act(() => result.current.dismiss());
    expect(result.current.visible).toBe(false);

    rerender({ phase: "connected", checks: video(false), runs: 3 });
    rerender({ phase: "connected", checks: video(false), runs: 4 });
    expect(result.current.visible).toBe(false);
  });

  it("survives a localStorage that refuses to answer", () => {
    const getItem = vi
      .spyOn(Storage.prototype, "getItem")
      .mockImplementation(() => {
        throw new Error("denied");
      });
    const setItem = vi
      .spyOn(Storage.prototype, "setItem")
      .mockImplementation(() => {
        throw new Error("denied");
      });

    const { result, rerender } = setup();
    rerender({ phase: "connected", checks: video(false), runs: 1 });
    rerender({ phase: "connected", checks: video(false), runs: 2 });

    // No stored history to consult; the prompt still works this session.
    expect(result.current.visible).toBe(true);

    getItem.mockRestore();
    setItem.mockRestore();
  });
});
