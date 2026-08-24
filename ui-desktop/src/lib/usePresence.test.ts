import { afterEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";

import { mockMatchMedia } from "../test/mediaQuery";
import { EXIT_MS, usePresence } from "./usePresence";

describe("usePresence", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows nothing when it was never opened", () => {
    const { result } = renderHook(() => usePresence<string>(null));
    expect(result.current.value).toBeNull();
    expect(result.current.leaving).toBe(false);
  });

  it("renders the value immediately when it opens", () => {
    const { result, rerender } = renderHook(
      ({ v }: { v: string | null }) => usePresence(v),
      { initialProps: { v: null as string | null } },
    );
    rerender({ v: "settings" });
    expect(result.current.value).toBe("settings");
    expect(result.current.leaving).toBe(false);
  });

  it("holds the last value on screen for the exit, then drops it", () => {
    vi.useFakeTimers();
    const { result, rerender } = renderHook(
      ({ v }: { v: string | null }) => usePresence(v),
      { initialProps: { v: "settings" as string | null } },
    );

    rerender({ v: null });
    // Still mounted, and still showing what it showed — this is the whole point:
    // a modal whose content is cleared on dismiss must not flash an empty card
    // on its way out.
    expect(result.current.value).toBe("settings");
    expect(result.current.leaving).toBe(true);

    act(() => {
      vi.advanceTimersByTime(EXIT_MS - 1);
    });
    expect(result.current.value).toBe("settings");

    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(result.current.value).toBeNull();
    expect(result.current.leaving).toBe(false);
  });

  it("cancels a running exit when the surface is re-opened", () => {
    vi.useFakeTimers();
    const { result, rerender } = renderHook(
      ({ v }: { v: string | null }) => usePresence(v),
      { initialProps: { v: "settings" as string | null } },
    );

    rerender({ v: null });
    act(() => {
      vi.advanceTimersByTime(EXIT_MS / 2);
    });
    rerender({ v: "logs" });

    expect(result.current.value).toBe("logs");
    expect(result.current.leaving).toBe(false);

    // The cancelled timer must not fire later and tear down the surface that
    // replaced it.
    act(() => {
      vi.advanceTimersByTime(EXIT_MS * 2);
    });
    expect(result.current.value).toBe("logs");
  });

  it("drops the surface at once under prefers-reduced-motion", () => {
    const media = mockMatchMedia(true);
    try {
      const { result, rerender } = renderHook(
        ({ v }: { v: string | null }) => usePresence(v),
        { initialProps: { v: "settings" as string | null } },
      );
      rerender({ v: null });
      expect(result.current.value).toBeNull();
      expect(result.current.leaving).toBe(false);
    } finally {
      media.restore();
    }
  });
});
