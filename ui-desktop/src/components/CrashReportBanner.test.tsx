import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { CrashReportBanner } from "./CrashReportBanner";
import { renderWithProviders } from "../test/renderWithProviders";

describe("CrashReportBanner", () => {
  it("announces the crash and offers the three actions", () => {
    renderWithProviders(
      <CrashReportBanner
        onView={vi.fn()}
        onCreateIssue={vi.fn()}
        onDismiss={vi.fn()}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      "Tenebra crashed last time",
    );
    for (const name of ["View report", "Create GitHub issue", "Dismiss"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
  });

  it("routes each button to its callback", async () => {
    const onView = vi.fn();
    const onCreateIssue = vi.fn();
    const onDismiss = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(
      <CrashReportBanner
        onView={onView}
        onCreateIssue={onCreateIssue}
        onDismiss={onDismiss}
      />,
    );

    await user.click(screen.getByRole("button", { name: "View report" }));
    await user.click(screen.getByRole("button", { name: "Create GitHub issue" }));
    await user.click(screen.getByRole("button", { name: "Dismiss" }));

    expect(onView).toHaveBeenCalledTimes(1);
    expect(onCreateIssue).toHaveBeenCalledTimes(1);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });
});
