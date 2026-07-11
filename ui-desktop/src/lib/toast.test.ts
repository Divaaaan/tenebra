import { describe, expect, it, vi } from "vitest";

import { pushToast, subscribeToast } from "./toast";

describe("toast bus", () => {
  it("delivers a pushed message to a subscriber", () => {
    const seen: string[] = [];
    const unsubscribe = subscribeToast((m) => seen.push(m));

    pushToast("tunnel up");

    expect(seen).toEqual(["tunnel up"]);
    unsubscribe();
  });

  it("delivers to every current subscriber", () => {
    const a = vi.fn();
    const b = vi.fn();
    const ua = subscribeToast(a);
    const ub = subscribeToast(b);

    pushToast("route · smart");

    expect(a).toHaveBeenCalledWith("route · smart");
    expect(b).toHaveBeenCalledWith("route · smart");
    ua();
    ub();
  });

  it("stops delivering after unsubscribe", () => {
    const fn = vi.fn();
    const unsubscribe = subscribeToast(fn);

    unsubscribe();
    pushToast("kill-switch · on");

    expect(fn).not.toHaveBeenCalled();
  });

  it("drops a message when nobody is listening", () => {
    expect(() => pushToast("into the void")).not.toThrow();
  });
});
