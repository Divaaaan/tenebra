import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";

import { ReportNudge } from "./ReportNudge";
import { renderWithProviders } from "../test/renderWithProviders";

describe("ReportNudge", () => {
  it("names what is broken rather than asking in the abstract", () => {
    renderWithProviders(<ReportNudge onReport={vi.fn()} onDismiss={vi.fn()} />);

    expect(screen.getByRole("status")).toHaveTextContent(/video/i);
  });

  it("opens the report flow", () => {
    const onReport = vi.fn();
    renderWithProviders(<ReportNudge onReport={onReport} onDismiss={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
    expect(onReport).toHaveBeenCalledTimes(1);
  });

  it("can be waved away", () => {
    const onDismiss = vi.fn();
    renderWithProviders(
      <ReportNudge onReport={vi.fn()} onDismiss={onDismiss} />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });
});
