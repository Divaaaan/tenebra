import { describe, expect, it, vi } from "vitest";
import { fireEvent, renderHook } from "@testing-library/react";

import { useKonami } from "./useKonami";

const CODE = [
  "ArrowUp",
  "ArrowUp",
  "ArrowDown",
  "ArrowDown",
  "ArrowLeft",
  "ArrowRight",
  "ArrowLeft",
  "ArrowRight",
  "b",
  "a",
];

function type(keys: string[], target: Element = document.body) {
  for (const key of keys) {
    fireEvent.keyDown(target, { key });
  }
}

describe("useKonami", () => {
  it("fires the callback on the full sequence", () => {
    const onUnlock = vi.fn();
    renderHook(() => useKonami(onUnlock));

    type(CODE);

    expect(onUnlock).toHaveBeenCalledTimes(1);
  });

  it("matches the two letters case-insensitively", () => {
    const onUnlock = vi.fn();
    renderHook(() => useKonami(onUnlock));

    type([...CODE.slice(0, 8), "B", "A"]);

    expect(onUnlock).toHaveBeenCalledTimes(1);
  });

  it("resets on a wrong key", () => {
    const onUnlock = vi.fn();
    renderHook(() => useKonami(onUnlock));

    type(["ArrowUp", "ArrowUp", "x"]);
    type(CODE);

    // The broken run contributed nothing; only the clean run counts.
    expect(onUnlock).toHaveBeenCalledTimes(1);
  });

  it("recovers when the miss is itself the opening key", () => {
    const onUnlock = vi.fn();
    renderHook(() => useKonami(onUnlock));

    // A stray ArrowUp mid-run should re-seed the sequence at one, not drop to zero.
    type(["ArrowDown", "ArrowUp"]);
    type(["ArrowUp", "ArrowDown", "ArrowDown", "ArrowLeft", "ArrowRight", "ArrowLeft", "ArrowRight", "b", "a"]);

    expect(onUnlock).toHaveBeenCalledTimes(1);
  });

  it("stands down while a text field owns the keyboard", () => {
    const onUnlock = vi.fn();
    renderHook(() => useKonami(onUnlock));

    const input = document.createElement("input");
    document.body.appendChild(input);
    type(CODE, input);
    input.remove();

    expect(onUnlock).not.toHaveBeenCalled();
  });

  it("detaches its listener on unmount", () => {
    const onUnlock = vi.fn();
    const { unmount } = renderHook(() => useKonami(onUnlock));

    unmount();
    type(CODE);

    expect(onUnlock).not.toHaveBeenCalled();
  });
});
