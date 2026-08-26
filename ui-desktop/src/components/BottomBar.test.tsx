import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { BottomBar } from "./BottomBar";
import { renderWithProviders } from "../test/renderWithProviders";
import { dictionaries } from "../i18n/strings";

const en = dictionaries.en;

function baseProps(overrides: Partial<Parameters<typeof BottomBar>[0]> = {}) {
  return {
    routing: "smart" as const,
    onSetRouting: vi.fn(),
    killSwitch: false,
    onToggleKillSwitch: vi.fn(),
    onLeakCheck: vi.fn(),
    onSettings: vi.fn(),
    onReportProblem: vi.fn(),
    bypassInstalled: false,
    bypassOn: false,
    bypassStrategy: "",
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

    it("marks only the active segment with the on class (its filled state)", () => {
      renderWithProviders(<BottomBar {...baseProps({ routing: "global" })} />);

      expect(screen.getByRole("button", { name: "Global" })).toHaveClass("on");
      expect(screen.getByRole("button", { name: "Smart" })).not.toHaveClass(
        "on",
      );
    });

    it("titles each segment with its routing hint", () => {
      renderWithProviders(<BottomBar {...baseProps()} />);

      expect(screen.getByRole("button", { name: "Smart" })).toHaveAttribute(
        "title",
        en.settings.routingSmartHint,
      );
      expect(screen.getByRole("button", { name: "Global" })).toHaveAttribute(
        "title",
        en.settings.routingGlobalHint,
      );
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

    it("gives the armed kill-switch the on class (its filled state)", () => {
      renderWithProviders(<BottomBar {...baseProps({ killSwitch: true })} />);

      expect(screen.getByRole("button", { name: /kill-switch/ })).toHaveClass(
        "on",
      );
    });

    it("titles the kill-switch with its hint", () => {
      renderWithProviders(<BottomBar {...baseProps()} />);

      expect(screen.getByRole("button", { name: /kill-switch/ })).toHaveAttribute(
        "title",
        en.bottom.killSwitchHint,
      );
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

    // Out here with leak-check and settings, not tucked into a menu: the app
    // had no way to say "this is broken" that didn't require a crash first.
    it("calls the report handler", async () => {
      const onReportProblem = vi.fn();
      const user = userEvent.setup();
      renderWithProviders(<BottomBar {...baseProps({ onReportProblem })} />);

      await user.click(
        screen.getByRole("button", { name: /report a problem/i }),
      );
      expect(onReportProblem).toHaveBeenCalledTimes(1);
    });
  });

  // The bar used to carry a button opening an import panel, badged with how many
  // files had been dropped this session. That count said nothing about whether
  // the packet filter was up — and it read as zero on every launch, over a bypass
  // the core had installed and started by itself. What stands here now is the
  // core's own answer, and only that.
  describe("bypass readout", () => {
    it("says nothing until the core reports a bundle", () => {
      const { container } = renderWithProviders(
        <BottomBar {...baseProps({ bypassInstalled: false })} />,
      );

      expect(container.querySelector(".bypass-stat")).toBeNull();
    });

    it("names the running strategy when the core reports the filter up", () => {
      const { container } = renderWithProviders(
        <BottomBar
          {...baseProps({
            bypassInstalled: true,
            bypassOn: true,
            bypassStrategy: "general (FAKE TLS AUTO)",
          })}
        />,
      );

      const stat = container.querySelector(".bypass-stat");
      expect(stat).toHaveClass("is-on");
      expect(stat?.textContent).toContain("general (FAKE TLS AUTO)");
    });

    it("shows an installed-but-idle bundle as off, naming no strategy", () => {
      const { container } = renderWithProviders(
        <BottomBar
          {...baseProps({
            bypassInstalled: true,
            bypassOn: false,
            bypassStrategy: "general (FAKE TLS AUTO)",
          })}
        />,
      );

      const stat = container.querySelector(".bypass-stat");
      expect(stat).not.toHaveClass("is-on");
      // The strategy is the one the last probe picked, not one that is running:
      // naming it here would read as "this is what is carrying your traffic".
      expect(stat?.textContent).not.toContain("general (FAKE TLS AUTO)");
    });

    it("is a readout, not a control", () => {
      // Switching the bypass lives in Settings. A status that answers a click is
      // how the old panel-opening button got mistaken for the switch itself.
      const { container } = renderWithProviders(
        <BottomBar {...baseProps({ bypassInstalled: true, bypassOn: true })} />,
      );

      expect(container.querySelector(".bypass-stat")?.tagName).toBe("SPAN");
    });
  });
});
