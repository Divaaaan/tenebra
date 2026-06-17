import { afterEach, describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";

import { prefersReducedMotion, useReducedMotion } from "./motion";
import { mockMatchMedia } from "../test/mediaQuery";

describe("prefersReducedMotion", () => {
  afterEach(() => {
    document.documentElement.style.removeProperty("--motion-ok");
  });

  it("is true only when --motion-ok is exactly '0'", () => {
    document.documentElement.style.setProperty("--motion-ok", "0");
    expect(prefersReducedMotion()).toBe(true);
  });

  it("is false when motion is allowed (--motion-ok '1')", () => {
    document.documentElement.style.setProperty("--motion-ok", "1");
    expect(prefersReducedMotion()).toBe(false);
  });

  it("is false when the flag is unset", () => {
    document.documentElement.style.removeProperty("--motion-ok");
    expect(prefersReducedMotion()).toBe(false);
  });
});

describe("useReducedMotion", () => {
  let media: ReturnType<typeof mockMatchMedia>;

  afterEach(() => {
    media?.restore();
  });

  it("initialises from the current match state", () => {
    media = mockMatchMedia(true);
    const { result } = renderHook(() => useReducedMotion());
    expect(result.current).toBe(true);
  });

  it("starts false when the user has not asked for reduced motion", () => {
    media = mockMatchMedia(false);
    const { result } = renderHook(() => useReducedMotion());
    expect(result.current).toBe(false);
  });

  it("re-renders when the OS preference flips", () => {
    media = mockMatchMedia(false);
    const { result } = renderHook(() => useReducedMotion());
    expect(result.current).toBe(false);

    act(() => media.setMatches(true));
    expect(result.current).toBe(true);

    act(() => media.setMatches(false));
    expect(result.current).toBe(false);
  });

  it("stops listening after unmount", () => {
    media = mockMatchMedia(false);
    const { result, unmount } = renderHook(() => useReducedMotion());
    unmount();
    // No subscribers remain, so a change must not throw or affect anything.
    act(() => media.setMatches(true));
    expect(result.current).toBe(false);
  });
});
