import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { BottomBar } from "./BottomBar";
import { renderWithProviders } from "../test/renderWithProviders";

function baseProps(overrides: Partial<Parameters<typeof BottomBar>[0]> = {}) {
  return {
    routing: "smart" as const,
    onSetRouting: vi.fn(),
    killSwitch: false,
    onToggleKillSwitch: vi.fn(),
    onLeakCheck: vi.fn(),
    onSettings: vi.fn(),
    ...overrides,
  };
}

describe("BottomBar", () => {
  describe("routing segment", () => {
    it("marks the current mode as pressed", () => {
      renderWithProviders(<BottomBar {...baseProps({ routing: "smart" })} />);

      expect(screen.getByRole("button", { name: "Smart" })).toHaveAttribute(
        "aria-pressed",
        "true",
      );
      expect(screen.getByRole("button", { name: "Global" })).toHaveAttribute(
        "aria-pressed",
        "false",
      );
    });

    it("sets the other mode on click", async () => {
      const onSetRouting = vi.fn();
      const user = userEvent.setup();
      renderWithProviders(
        <BottomBar {...baseProps({ routing: "smart", onSetRouting })} />,
      );

      await user.click(screen.getByRole("button", { name: "Global" }));
      expect(onSetRouting).toHaveBeenCalledWith("global");
    });
  });

  describe("kill-switch", () => {
    it("reflects the armed state", () => {
      renderWithProviders(<BottomBar {...baseProps({ killSwitch: true })} />);

      const button = screen.getByRole("button", { name: /kill-switch/ });
      expect(button).toBeEnabled();
      expect(button).toHaveAttribute("aria-pressed", "true");
    });

    it("requests a toggle on click", async () => {
      const onToggleKillSwitch = vi.fn();
      const user = userEvent.setup();
      renderWithProviders(
        <BottomBar {...baseProps({ killSwitch: false, onToggleKillSwitch })} />,
      );

      const button = screen.getByRole("button", { name: /kill-switch/ });
      expect(button).toHaveAttribute("aria-pressed", "false");
      await user.click(button);
      expect(onToggleKillSwitch).toHaveBeenCalledTimes(1);
    });
  });

  describe("quick actions", () => {
    it("calls the leak-check handler", async () => {
      const onLeakCheck = vi.fn();
      const user = userEvent.setup();
      renderWithProviders(<BottomBar {...baseProps({ onLeakCheck })} />);

      await user.click(screen.getByRole("button", { name: /leak-check/ }));
      expect(onLeakCheck).toHaveBeenCalledTimes(1);
    });

    it("calls the settings handler", async () => {
      const onSettings = vi.fn();
      const user = userEvent.setup();
      renderWithProviders(<BottomBar {...baseProps({ onSettings })} />);

      await user.click(screen.getByRole("button", { name: /settings/ }));
      expect(onSettings).toHaveBeenCalledTimes(1);
    });
  });
});
