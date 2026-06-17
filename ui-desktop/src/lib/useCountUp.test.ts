import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";

import { useCountUp } from "./useCountUp";

// useCountUp drives its tween off the rAF *timestamp* (now - start), not a tick
// count, so a manual frame harness with a controllable clock is the reliable way
// to step it: each flush passes the timestamp the hook reads. Vitest's faked rAF
// keeps performance.now frozen, which would pin progress at t=0.
let queue: Map<number, FrameRequestCallback>;
let nextHandle: number;
let clock: number;

function installRaf() {
  queue = new Map();
  nextHandle = 1;
  // Start above zero: the hook uses start===0 as its "first frame" sentinel, and
  // a real rAF timestamp (performance.now) is never 0. A zero clock would never
  // capture the start and the tween would stall — exactly what the hook avoids.
  clock = 1000;
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    const handle = nextHandle++;
    queue.set(handle, cb);
    return handle;
  });
  vi.stubGlobal("cancelAnimationFrame", (handle: number) => {
    queue.delete(handle);
  });
}

/** Advance the clock by `ms` and run every frame currently queued, once. */
function flush(ms: number) {
  clock += ms;
  const pending = [...queue.entries()];
  queue.clear();
  for (const [, cb] of pending) {
    cb(clock);
  }
}

describe("useCountUp", () => {
  beforeEach(() => installRaf());

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("snaps to the target immediately when disabled", () => {
    const { result } = renderHook(
      ({ target }) => useCountUp(target, { enabled: false }),
      { initialProps: { target: 1000 } },
    );
    expect(result.current).toBe(1000);
  });

  it("snaps to a new target while disabled instead of tweening", () => {
    const { result, rerender } = renderHook(
      ({ target }) => useCountUp(target, { enabled: false }),
      { initialProps: { target: 0 } },
    );
    expect(result.current).toBe(0);

    rerender({ target: 5000 });
    expect(result.current).toBe(5000);
  });

  it("sets the value without animating when the delta is below the threshold", () => {
    const { result, rerender } = renderHook(
      ({ target }) => useCountUp(target),
      { initialProps: { target: 0 } },
    );
    // 0.3 < MIN_DELTA (0.5): the effect short-circuits to setDisplay(target).
    act(() => {
      rerender({ target: 0.3 });
    });
    expect(result.current).toBe(0.3);
    // No frame should have been queued for a sub-threshold change.
    expect(queue.size).toBe(0);
  });

  it("glides from the on-screen value to a changed target", () => {
    const { result, rerender } = renderHook(
      ({ target }) => useCountUp(target, { duration: 600 }),
      { initialProps: { target: 0 } },
    );
    expect(result.current).toBe(0);

    // Kick off a tween 0 → 1000. Step the rerender and each frame in their own
    // act() so the effect commits (and queues its first frame) before we flush.
    act(() => {
      rerender({ target: 1000 });
    });
    act(() => flush(0)); // first frame: captures start (t=0)
    act(() => flush(150)); // second frame: t = 150/600
    expect(result.current).toBeGreaterThan(0);
    expect(result.current).toBeLessThan(1000);

    // Past the duration: easeOutCubic at t=1 yields exactly the target.
    act(() => flush(600));
    expect(result.current).toBe(1000);
  });

  it("restarts from the current value when the target changes mid-tween", () => {
    const { result, rerender } = renderHook(
      ({ target }) => useCountUp(target, { duration: 600 }),
      { initialProps: { target: 0 } },
    );

    act(() => {
      rerender({ target: 1000 });
    });
    act(() => flush(0));
    act(() => flush(150));
    const midway = result.current;
    expect(midway).toBeGreaterThan(0);
    expect(midway).toBeLessThan(1000);

    // Retarget downward; it should converge on the new target, not the old one.
    act(() => {
      rerender({ target: 200 });
    });
    act(() => flush(0));
    act(() => flush(600));
    expect(result.current).toBe(200);
  });

  it("coerces a non-finite target to zero", () => {
    const { result } = renderHook(
      ({ target }) => useCountUp(target, { enabled: false }),
      { initialProps: { target: Number.NaN } },
    );
    expect(result.current).toBe(0);
  });
});
