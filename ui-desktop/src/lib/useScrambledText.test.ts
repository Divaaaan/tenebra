import { afterEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";

import { useScrambledText } from "./useScrambledText";
import { mockMatchMedia } from "../test/mediaQuery";

describe("useScrambledText", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("returns the initial value unchanged — no decrypt on first render", () => {
    const media = mockMatchMedia(false);
    const { result } = renderHook(({ text }) => useScrambledText(text), {
      initialProps: { text: "IDLE" },
    });
    expect(result.current).toBe("IDLE");
    media.restore();
  });

  it("shows a change instantly under reduced motion", () => {
    const media = mockMatchMedia(true);
    const { result, rerender } = renderHook(
      ({ text }) => useScrambledText(text),
      { initialProps: { text: "IDLE" } },
    );
    rerender({ text: "CONNECTED" });
    expect(result.current).toBe("CONNECTED");
    media.restore();
  });

  it("decrypts a changed word over its frames, settling exactly", () => {
    const media = mockMatchMedia(false);
    vi.useFakeTimers();
    const { result, rerender } = renderHook(
      ({ text }) => useScrambledText(text, { frames: 4, intervalMs: 10 }),
      { initialProps: { text: "IDLE" } },
    );

    act(() => {
      rerender({ text: "LIVE" });
    });
    // Mid-flight: scrambled, but always the target's length.
    expect(result.current).not.toBe("LIVE");
    expect(result.current).toHaveLength("LIVE".length);

    act(() => {
      vi.advanceTimersByTime(4 * 10);
    });
    expect(result.current).toBe("LIVE");
    media.restore();
  });
});
