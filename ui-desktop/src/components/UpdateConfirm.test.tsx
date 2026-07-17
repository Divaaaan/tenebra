import { describe, expect, it, vi } from "vitest";
import { screen, fireEvent, within } from "@testing-library/react";

import { UpdateConfirm } from "./UpdateConfirm";
import { renderWithProviders } from "../test/renderWithProviders";

// The presentational half of the manual-install gate: it states the consequence
// (this drops the VPN) and reports the user's choice. It must never act on its
// own — only call the callbacks.
describe("UpdateConfirm", () => {
  it("spells out that installing drops the connection", () => {
    renderWithProviders(
      <UpdateConfirm onConfirm={vi.fn()} onCancel={vi.fn()} />,
    );

    const dialog = within(screen.getByRole("alertdialog"));
    expect(dialog.getByText(/disconnects your VPN/i)).toBeInTheDocument();
  });

  it("confirms only when the primary action is pressed", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    renderWithProviders(
      <UpdateConfirm onConfirm={onConfirm} onCancel={onCancel} />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Install now" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("declines via the cancel button", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    renderWithProviders(
      <UpdateConfirm onConfirm={onConfirm} onCancel={onCancel} />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("declines on Escape — the safe default keeps the tunnel", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    renderWithProviders(
      <UpdateConfirm onConfirm={onConfirm} onCancel={onCancel} />,
    );

    fireEvent.keyDown(window, { key: "Escape" });
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("declines when the scrim behind the card is clicked", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    const { container } = renderWithProviders(
      <UpdateConfirm onConfirm={onConfirm} onCancel={onCancel} />,
    );

    const scrim = container.querySelector(".prof-modal-scrim");
    expect(scrim).not.toBeNull();
    fireEvent.mouseDown(scrim as Element);
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });
});
