import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { useTunOverrideAsk } from "./useTunOverrideAsk";

// Covers the contract App leans on: every question is answered exactly once, and
// no question can outlive the tree that asked it. The second rule is the one with
// teeth — an unresolved promise leaves the connect's `finally` unreached and the
// button disabled with no way back short of a restart.

describe("useTunOverrideAsk", () => {
  it("resolves with the answer the user gave", async () => {
    const { result } = renderHook(() => useTunOverrideAsk());

    let answered: boolean | null = null;
    act(() => {
      void result.current.ask().then((ok) => {
        answered = ok;
      });
    });
    expect(result.current.pending).toBe(true);

    act(() => result.current.answer(true));

    await waitFor(() => expect(answered).toBe(true));
    expect(result.current.pending).toBe(false);
  });

  it("resolves false when the user declines", async () => {
    const { result } = renderHook(() => useTunOverrideAsk());

    let answered: boolean | null = null;
    act(() => {
      void result.current.ask().then((ok) => {
        answered = ok;
      });
    });
    act(() => result.current.answer(false));

    await waitFor(() => expect(answered).toBe(false));
    expect(result.current.pending).toBe(false);
  });

  it("declines a question left open when the tree unmounts", async () => {
    const { result, unmount } = renderHook(() => useTunOverrideAsk());

    let answered: boolean | null = null;
    act(() => {
      void result.current.ask().then((ok) => {
        answered = ok;
      });
    });
    expect(answered).toBeNull();

    unmount();

    // The asker gets its answer back even though there is no longer anyone to
    // click: unmounting is a decline, not a black hole.
    await waitFor(() => expect(answered).toBe(false));
  });

  it("answers an earlier question when a new one supersedes it", async () => {
    const { result } = renderHook(() => useTunOverrideAsk());

    let first: boolean | null = null;
    let second: boolean | null = null;
    act(() => {
      void result.current.ask().then((ok) => {
        first = ok;
      });
    });
    act(() => {
      void result.current.ask().then((ok) => {
        second = ok;
      });
    });

    await waitFor(() => expect(first).toBe(false));
    expect(second).toBeNull();

    act(() => result.current.answer(true));
    await waitFor(() => expect(second).toBe(true));
  });

  it("ignores an answer when nothing was asked", () => {
    const { result } = renderHook(() => useTunOverrideAsk());
    expect(() => act(() => result.current.answer(true))).not.toThrow();
    expect(result.current.pending).toBe(false);
  });
});
