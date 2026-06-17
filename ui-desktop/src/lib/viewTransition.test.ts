import { afterEach, describe, expect, it, vi } from "vitest";

import { withViewTransition } from "./viewTransition";

type StartViewTransition = (cb: () => void) => unknown;

afterEach(() => {
  delete (document as { startViewTransition?: StartViewTransition })
    .startViewTransition;
  document.documentElement.style.removeProperty("--motion-ok");
});

describe("withViewTransition", () => {
  it("applies the update synchronously when the API is absent", () => {
    // jsdom has no startViewTransition; the update must still run.
    const update = vi.fn();
    withViewTransition(update);
    expect(update).toHaveBeenCalledTimes(1);
  });

  it("routes the update through startViewTransition when available", () => {
    const start = vi.fn((cb: () => void) => {
      cb();
      return {};
    });
    (
      document as { startViewTransition?: StartViewTransition }
    ).startViewTransition = start;

    const update = vi.fn();
    withViewTransition(update);

    expect(start).toHaveBeenCalledTimes(1);
    expect(start).toHaveBeenCalledWith(update);
    expect(update).toHaveBeenCalledTimes(1);
  });

  it("bypasses the API under reduced motion but still applies the update", () => {
    const start = vi.fn((cb: () => void) => {
      cb();
      return {};
    });
    (
      document as { startViewTransition?: StartViewTransition }
    ).startViewTransition = start;
    // --motion-ok '0' makes prefersReducedMotion() true.
    document.documentElement.style.setProperty("--motion-ok", "0");

    const update = vi.fn();
    withViewTransition(update);

    expect(start).not.toHaveBeenCalled();
    expect(update).toHaveBeenCalledTimes(1);
  });
});
