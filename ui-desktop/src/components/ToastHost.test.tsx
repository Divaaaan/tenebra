import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";

import { ToastHost, TOAST_LIFE_MS } from "./ToastHost";
import { pushToast } from "../lib/toast";

// The host owns the queue and the per-toast timer, so the lifetime tests run on
// fake timers; render is bare because ToastHost needs no context.
describe("ToastHost", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.runOnlyPendingTimers();
    vi.useRealTimers();
  });

  it("shows nothing until a toast is pushed", () => {
    render(<ToastHost />);
    expect(screen.queryByText("first")).not.toBeInTheDocument();
  });

  it("renders a pushed toast with the signal prefix", () => {
    render(<ToastHost />);

    act(() => {
      pushToast("tunnel up · ex-01 · hysteria2");
    });

    expect(screen.getByText("tunnel up · ex-01 · hysteria2")).toBeInTheDocument();
    expect(screen.getByText("▶")).toBeInTheDocument();
  });

  it("removes the toast after its lifetime", () => {
    render(<ToastHost />);

    act(() => {
      pushToast("gone soon");
    });
    expect(screen.getByText("gone soon")).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(TOAST_LIFE_MS);
    });
    expect(screen.queryByText("gone soon")).not.toBeInTheDocument();
  });

  it("queues toasts and shows them one at a time", () => {
    render(<ToastHost />);

    act(() => {
      pushToast("first");
      pushToast("second");
    });

    // Only the first occupies the slot; the second waits.
    expect(screen.getByText("first")).toBeInTheDocument();
    expect(screen.queryByText("second")).not.toBeInTheDocument();

    // After the first slot elapses, the second takes over.
    act(() => {
      vi.advanceTimersByTime(TOAST_LIFE_MS);
    });
    expect(screen.queryByText("first")).not.toBeInTheDocument();
    expect(screen.getByText("second")).toBeInTheDocument();

    // And then the slot clears.
    act(() => {
      vi.advanceTimersByTime(TOAST_LIFE_MS);
    });
    expect(screen.queryByText("second")).not.toBeInTheDocument();
  });
});
