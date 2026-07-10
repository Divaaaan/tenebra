import { describe, expect, it, vi } from "vitest";
import { screen, fireEvent, within } from "@testing-library/react";

import { DeepLinkConfirm } from "./DeepLinkConfirm";
import { renderWithProviders } from "../test/renderWithProviders";

// The presentational half of the connect gate: it names the request and reports
// the user's choice. It must never act on its own — only call the callbacks.
describe("DeepLinkConfirm", () => {
  it("names the profile the link asked to connect", () => {
    renderWithProviders(
      <DeepLinkConfirm
        profile="Demo subscription"
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );

    const dialog = within(screen.getByRole("alertdialog"));
    expect(dialog.getByText(/Demo subscription/)).toBeInTheDocument();
    // The provenance line makes clear it came from outside the app.
    expect(dialog.getByText(/external link/i)).toBeInTheDocument();
  });

  it("confirms only when the primary action is pressed", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    renderWithProviders(
      <DeepLinkConfirm
        profile="p1"
        onConfirm={onConfirm}
        onCancel={onCancel}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("declines via the cancel button", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    renderWithProviders(
      <DeepLinkConfirm
        profile="p1"
        onConfirm={onConfirm}
        onCancel={onCancel}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("declines on Escape — the safe default", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    renderWithProviders(
      <DeepLinkConfirm
        profile="p1"
        onConfirm={onConfirm}
        onCancel={onCancel}
      />,
    );

    fireEvent.keyDown(window, { key: "Escape" });
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("declines when the scrim behind the card is clicked", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    const { container } = renderWithProviders(
      <DeepLinkConfirm
        profile="p1"
        onConfirm={onConfirm}
        onCancel={onCancel}
      />,
    );

    const scrim = container.querySelector(".prof-modal-scrim");
    expect(scrim).not.toBeNull();
    fireEvent.mouseDown(scrim as Element);
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });
});
