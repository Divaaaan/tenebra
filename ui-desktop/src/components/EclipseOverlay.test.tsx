import { describe, expect, it, vi, afterEach } from "vitest";
import { act, render } from "@testing-library/react";

import { EclipseOverlay, ECLIPSE_MS } from "./EclipseOverlay";

describe("EclipseOverlay", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders nothing while inactive", () => {
    const { container } = render(
      <EclipseOverlay active={false} onDone={vi.fn()} />,
    );
    expect(container.querySelector(".eclipse")).not.toBeInTheDocument();
  });

  it("renders the inert corona while active", () => {
    const { container } = render(
      <EclipseOverlay active onDone={vi.fn()} />,
    );
    const el = container.querySelector(".eclipse");
    expect(el).toBeInTheDocument();
    expect(el).toHaveAttribute("aria-hidden", "true");
    expect(container.querySelector(".eclipse-corona")).toBeInTheDocument();
  });

  it("calls onDone once the hold elapses", () => {
    vi.useFakeTimers();
    const onDone = vi.fn();
    render(<EclipseOverlay active onDone={onDone} />);

    expect(onDone).not.toHaveBeenCalled();
    act(() => {
      vi.advanceTimersByTime(ECLIPSE_MS);
    });
    expect(onDone).toHaveBeenCalledTimes(1);
  });
});
