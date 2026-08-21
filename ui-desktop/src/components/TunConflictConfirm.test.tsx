import { describe, expect, it, vi } from "vitest";
import { screen, fireEvent, within } from "@testing-library/react";

import { TunConflictConfirm } from "./TunConflictConfirm";
import { renderWithProviders } from "../test/renderWithProviders";

// The override gate for the tun-conflict guard. Overriding can take the machine
// offline, so every ambiguous gesture — Escape, the scrim, the focused button —
// has to mean "no".
describe("TunConflictConfirm", () => {
  it("states what the override costs", () => {
    renderWithProviders(
      <TunConflictConfirm onConfirm={vi.fn()} onCancel={vi.fn()} />,
    );

    const dialog = within(screen.getByRole("alertdialog"));
    expect(dialog.getByText(/do not overlap/i)).toBeInTheDocument();
  });

  it("overrides only when the primary action is pressed", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    renderWithProviders(
      <TunConflictConfirm onConfirm={onConfirm} onCancel={onCancel} />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Connect anyway" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("declines via the cancel button", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    renderWithProviders(
      <TunConflictConfirm onConfirm={onConfirm} onCancel={onCancel} />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("declines on Escape", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    renderWithProviders(
      <TunConflictConfirm onConfirm={onConfirm} onCancel={onCancel} />,
    );

    fireEvent.keyDown(window, { key: "Escape" });
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("declines when the scrim behind the card is clicked", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    const { container } = renderWithProviders(
      <TunConflictConfirm onConfirm={onConfirm} onCancel={onCancel} />,
    );

    const scrim = container.querySelector(".prof-modal-scrim");
    expect(scrim).not.toBeNull();
    fireEvent.mouseDown(scrim as Element);
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("puts focus on declining, so a stray Enter cannot override the guard", () => {
    renderWithProviders(
      <TunConflictConfirm onConfirm={vi.fn()} onCancel={vi.fn()} />,
    );

    expect(screen.getByRole("button", { name: "Cancel" })).toHaveFocus();
  });
});
