import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ProblemReportModal } from "./ProblemReportModal";
import { renderWithProviders } from "../test/renderWithProviders";

// The surface where the user decides whether to file anything at all. It has to
// show them the whole report before they can act on it, and it must not act on
// its own: the clipboard write and the browser both wait for a click.

const REPORT = {
  text: "Tenebra problem report\nstate: idle\n",
  path: String.raw`C:\Users\me\AppData\Local\Tenebra\bundle.txt`,
};

function setup(overrides: Partial<Parameters<typeof ProblemReportModal>[0]> = {}) {
  const props = {
    report: REPORT,
    building: false,
    onClose: vi.fn(),
    onOpenIssue: vi.fn(),
    ...overrides,
  };
  const utils = renderWithProviders(<ProblemReportModal {...props} />);
  return { ...utils, props };
}

describe("ProblemReportModal", () => {
  it("shows the report and says nothing has been sent", () => {
    setup();

    expect(screen.getByRole("dialog")).toHaveTextContent("state: idle");
    expect(screen.getByText(/Nothing has been sent/)).toBeInTheDocument();
  });

  it("names the file the untrimmed copy went to", () => {
    setup();

    expect(screen.getByText(REPORT.path)).toBeInTheDocument();
  });

  // Half a report is still worth filing, but it must not read as a whole one.
  it("says so when the core produced nothing", () => {
    setup({ report: { text: "shell only", path: null } });

    expect(screen.getByRole("status")).toHaveTextContent(
      /core didn't answer/i,
    );
  });

  it("waits for the collection before offering either action", () => {
    setup({ report: null, building: true });

    expect(screen.getByRole("button", { name: "Copy report" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Open GitHub issue" }),
    ).toBeDisabled();
    expect(screen.getByRole("dialog")).toHaveTextContent("Collecting…");
  });

  it("copies the report on request, and only then", async () => {
    setup();
    const user = userEvent.setup();

    // Installed after setup() so it, not userEvent's own clipboard, is what the
    // handler calls.
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    expect(writeText).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Copy report" }));

    expect(writeText).toHaveBeenCalledWith(REPORT.text);
    expect(
      await screen.findByRole("button", { name: "Copied" }),
    ).toBeInTheDocument();

    delete (navigator as { clipboard?: unknown }).clipboard;
  });

  it("claims no copy when the clipboard refuses", async () => {
    setup();
    const user = userEvent.setup();

    const writeText = vi.fn().mockRejectedValue(new Error("denied"));
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    await user.click(screen.getByRole("button", { name: "Copy report" }));

    expect(
      screen.getByRole("button", { name: "Copy report" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copied" })).toBeNull();

    delete (navigator as { clipboard?: unknown }).clipboard;
  });

  // Rendering a report is not publishing one. Only the button publishes.
  it("opens the issue form on its own button and nowhere else", () => {
    const { props } = setup();

    expect(props.onOpenIssue).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Open GitHub issue" }));
    expect(props.onOpenIssue).toHaveBeenCalledTimes(1);
  });

  it("closes on Escape and on a scrim click, like the app's other overlays", () => {
    const { props, container } = setup();

    fireEvent.keyDown(window, { key: "Escape" });
    expect(props.onClose).toHaveBeenCalledTimes(1);

    const scrim = container.querySelector(".prof-modal-scrim");
    fireEvent.mouseDown(scrim as Element);
    expect(props.onClose).toHaveBeenCalledTimes(2);
  });
});
