import { describe, expect, it, vi, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";

import { ErrorBoundary } from "./ErrorBoundary";

function Boom(): never {
  throw new Error("render exploded");
}

describe("ErrorBoundary", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("renders its children while nothing throws", () => {
    render(
      <ErrorBoundary fallback={<div>fallback</div>}>
        <div>healthy child</div>
      </ErrorBoundary>,
    );
    expect(screen.getByText("healthy child")).toBeInTheDocument();
    expect(screen.queryByText("fallback")).not.toBeInTheDocument();
  });

  it("shows the fallback and reports the error when a child throws", () => {
    // React logs caught render errors to console.error; silence it for a clean
    // test run.
    vi.spyOn(console, "error").mockImplementation(() => {});
    const onError = vi.fn();

    render(
      <ErrorBoundary fallback={<div>fallback shown</div>} onError={onError}>
        <Boom />
      </ErrorBoundary>,
    );

    expect(screen.getByText("fallback shown")).toBeInTheDocument();
    // The boundary delegates the report (marker + core command) to onError, which
    // main.tsx wires to the debounced recorder — asserted in webCrash.test.ts.
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError.mock.calls[0][0]).toContain("render exploded");
  });
});
