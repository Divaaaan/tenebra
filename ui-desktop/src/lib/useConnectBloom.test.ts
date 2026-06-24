import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";

import { useConnectBloom } from "./useConnectBloom";
import type { ConnectionState } from "../api";

// The bloom paints a fixed full-window overlay (zIndex 60) onto document.body.
// Count those to assert whether a bloom fired, without depending on its styling.
function bloomCount(): number {
  return document.querySelectorAll('div[aria-hidden="true"][style*="z-index: 60"]')
    .length;
}

describe("useConnectBloom", () => {
  // A fake Animation that resolves synchronously enough for assertions: capture
  // the keyframes/options and expose finish/cancel listeners the hook attaches.
  let animateSpy: ReturnType<typeof vi.fn>;
  let lastAnim: { cancel: () => void; listeners: Record<string, () => void> } | null;

  beforeEach(() => {
    lastAnim = null;
    animateSpy = vi.fn(function (this: Element) {
      const listeners: Record<string, () => void> = {};
      const anim = {
        listeners,
        addEventListener: (type: string, cb: () => void) => {
          listeners[type] = cb;
        },
        cancel: () => listeners.cancel?.(),
        finish: () => listeners.finish?.(),
      };
      lastAnim = anim;
      return anim as unknown as Animation;
    });
    // jsdom has no WAAPI; install our spy as Element.prototype.animate.
    (Element.prototype as unknown as { animate: unknown }).animate = animateSpy;
    // Motion allowed by default for these tests.
    document.documentElement.style.setProperty("--motion-ok", "1");
  });

  afterEach(() => {
    document.documentElement.style.removeProperty("--motion-ok");
    delete (Element.prototype as Partial<{ animate: unknown }>).animate;
    // Sweep any overlay a test left behind.
    document
      .querySelectorAll('div[aria-hidden="true"][style*="z-index: 60"]')
      .forEach((n) => n.remove());
    vi.restoreAllMocks();
  });

  it("does not bloom on the initial phase, even if already connected", () => {
    renderHook(() => useConnectBloom("connected"));
    expect(animateSpy).not.toHaveBeenCalled();
    expect(bloomCount()).toBe(0);
  });

  it("blooms on the transition into connected", () => {
    const { rerender } = renderHook(
      ({ phase }: { phase: ConnectionState }) => useConnectBloom(phase),
      { initialProps: { phase: "connecting" as ConnectionState } },
    );
    expect(animateSpy).not.toHaveBeenCalled();

    rerender({ phase: "connected" });
    expect(animateSpy).toHaveBeenCalledTimes(1);
    expect(bloomCount()).toBe(1);

    // When the animation finishes, the overlay removes itself.
    lastAnim?.listeners.finish?.();
    expect(bloomCount()).toBe(0);
  });

  it("does not bloom again while staying connected", () => {
    const { rerender } = renderHook(
      ({ phase }: { phase: ConnectionState }) => useConnectBloom(phase),
      { initialProps: { phase: "idle" as ConnectionState } },
    );
    rerender({ phase: "connected" });
    lastAnim?.listeners.finish?.();
    rerender({ phase: "connected" }); // a re-render with the same phase
    expect(animateSpy).toHaveBeenCalledTimes(1);
  });

  it("stands down entirely under reduced motion", () => {
    document.documentElement.style.setProperty("--motion-ok", "0");
    const { rerender } = renderHook(
      ({ phase }: { phase: ConnectionState }) => useConnectBloom(phase),
      { initialProps: { phase: "idle" as ConnectionState } },
    );
    rerender({ phase: "connected" });
    expect(animateSpy).not.toHaveBeenCalled();
    expect(bloomCount()).toBe(0);
  });

  it("cleans up the overlay if unmounted mid-bloom", () => {
    const { rerender, unmount } = renderHook(
      ({ phase }: { phase: ConnectionState }) => useConnectBloom(phase),
      { initialProps: { phase: "connecting" as ConnectionState } },
    );
    rerender({ phase: "connected" });
    expect(bloomCount()).toBe(1);
    unmount(); // effect cleanup cancels the animation → cancel listener removes it
    expect(bloomCount()).toBe(0);
  });
});
