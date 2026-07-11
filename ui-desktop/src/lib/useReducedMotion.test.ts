import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";

import { useReducedMotion } from "./useReducedMotion";
import { mockMatchMedia } from "../test/mediaQuery";

describe("useReducedMotion", () => {
  it("reports false when motion is allowed", () => {
    const media = mockMatchMedia(false);
    const { result } = renderHook(() => useReducedMotion());
    expect(result.current).toBe(false);
    media.restore();
  });

  it("reports true when the reduce preference is set", () => {
    const media = mockMatchMedia(true);
    const { result } = renderHook(() => useReducedMotion());
    expect(result.current).toBe(true);
    media.restore();
  });

  it("re-renders when the preference flips at runtime", () => {
    const media = mockMatchMedia(false);
    const { result } = renderHook(() => useReducedMotion());
    expect(result.current).toBe(false);

    act(() => media.setMatches(true));
    expect(result.current).toBe(true);

    act(() => media.setMatches(false));
    expect(result.current).toBe(false);

    media.restore();
  });
});
