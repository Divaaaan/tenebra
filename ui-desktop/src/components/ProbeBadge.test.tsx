import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";

import type { CheckStage, NodeCheckResult } from "../api";
import { renderWithProviders } from "../test/renderWithProviders";
import { ProbeBadge } from "./ProbeBadge";

function node(targets: Array<[CheckStage, number]>): NodeCheckResult {
  return {
    node: "n",
    targets: targets.map(([stage, rttMs], i) => ({ target: `t${i}`, stage, rttMs })),
  };
}

describe("ProbeBadge", () => {
  it("shows a placeholder for a node that has not been checked", () => {
    const { container } = renderWithProviders(<ProbeBadge />);
    expect(container.querySelector(".probe-badge--empty")).not.toBeNull();
  });

  it("sweeps while the probe is in flight", () => {
    const { container } = renderWithProviders(<ProbeBadge probing />);
    expect(container.querySelector(".probe-badge--probing")).not.toBeNull();
    // The moving band is decorative, so it must not be read out.
    const scan = container.querySelector(".probe-badge__scan");
    expect(scan).not.toBeNull();
    expect(scan?.getAttribute("aria-hidden")).toBe("true");
  });

  it("shows latency and coverage for a working node", () => {
    renderWithProviders(<ProbeBadge result={node([["ok", 40], ["ok", 60], ["probe", 0]])} />);
    // Median of 40 and 60 is the lower middle sample.
    expect(screen.getByText(/40/)).toBeTruthy();
    expect(screen.getByText("2/3")).toBeTruthy();
  });

  it("names the failed stage instead of showing a bare error mark", () => {
    const { container } = renderWithProviders(
      <ProbeBadge result={node([["handshake", 0], ["handshake", 0]])} />,
    );
    // A node that accepts TCP and never handshakes is exactly what a ping badge
    // gets wrong, so it is called out by name and tinted apart from other faults.
    expect(screen.getByText("handshake failed")).toBeTruthy();
    expect(container.querySelector(".probe-badge--handshake")).not.toBeNull();
  });

  it("distinguishes an unreachable address from a dead tunnel", () => {
    const { unmount } = renderWithProviders(
      <ProbeBadge result={node([["dial", 0], ["dial", 0]])} />,
    );
    expect(screen.getByText("no answer")).toBeTruthy();
    unmount();

    renderWithProviders(<ProbeBadge result={node([["probe", 0], ["probe", 0]])} />);
    expect(screen.getByText("no traffic")).toBeTruthy();
  });

  it("marks the auto-selected node once", () => {
    const { container } = renderWithProviders(
      <ProbeBadge result={node([["ok", 30], ["ok", 30]])} best />,
    );
    expect(container.querySelector(".probe-badge--best")).not.toBeNull();
    expect(screen.getByText("best")).toBeTruthy();
  });

  it("does not mark a node best unless told to", () => {
    const { container } = renderWithProviders(
      <ProbeBadge result={node([["ok", 30], ["ok", 30]])} />,
    );
    expect(container.querySelector(".probe-badge--best")).toBeNull();
  });

  it("treats a node with one lucky target as failed, not as fastest", () => {
    // Regression guard for the field failure: this node answered a single
    // destination and would have shown a green 40ms badge under a naive check.
    const { container } = renderWithProviders(
      <ProbeBadge
        result={node([
          ["ok", 40],
          ["probe", 0],
          ["probe", 0],
        ])}
      />,
    );
    expect(container.querySelector(".probe-badge--ok")).toBeNull();
    expect(container.querySelector(".probe-badge--fail")).not.toBeNull();
  });
});
