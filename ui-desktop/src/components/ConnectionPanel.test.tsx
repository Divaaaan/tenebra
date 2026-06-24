import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ConnectionPanel } from "./ConnectionPanel";
import { renderWithProviders } from "../test/renderWithProviders";
import type { ConnectionState } from "../api";

// Shared baseline props; each test overrides phase and the few fields that vary.
function baseProps(overrides: Partial<Parameters<typeof ConnectionPanel>[0]> = {}) {
  return {
    phase: "idle" as ConnectionState,
    nodeCode: "DE-FRA-01",
    nodeCity: "frankfurt",
    exitServer: null as string | null,
    protocolLabel: "vless",
    uptime: "12:34",
    mbps: "94.2",
    ping: "27",
    history: { down: [], up: [] },
    cumulativeDown: 0,
    cumulativeUp: 0,
    onPrimary: vi.fn(),
    onChange: vi.fn(),
    ...overrides,
  };
}

describe("ConnectionPanel", () => {
  describe("idle", () => {
    it("shows the disconnected status, a Connect label and dimmed stats", () => {
      renderWithProviders(<ConnectionPanel {...baseProps({ phase: "idle" })} />);

      expect(screen.getByText("Disconnected")).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: /Connect/ }),
      ).toBeInTheDocument();
      // Dimmed placeholders stand in for the live stats while idle.
      expect(screen.getByText("00:00")).toBeInTheDocument();
      expect(screen.getByText("0.0")).toBeInTheDocument();
      expect(screen.getByText("—")).toBeInTheDocument();
      // The off sub-line copy.
      expect(
        screen.getByText("traffic unprotected · select a node and connect"),
      ).toBeInTheDocument();
    });
  });

  describe("connected", () => {
    it("shows the connected status, the disconnect label, the exit + protocol and live stats", () => {
      renderWithProviders(
        <ConnectionPanel
          {...baseProps({
            phase: "connected",
            exitServer: "203.0.113.7",
            protocolLabel: "vless",
          })}
        />,
      );

      expect(screen.getByText("Connected")).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: /Disconnect/ }),
      ).toBeInTheDocument();
      // The exit address shows in both the sub-line and the current-server block.
      expect(screen.getAllByText("203.0.113.7").length).toBeGreaterThanOrEqual(
        1,
      );
      // Protocol appears on the connected sub-line (matched loosely so the
      // surrounding "·" separators don't break the lookup).
      expect(screen.getByText(/vless/)).toBeInTheDocument();
      // Live values replace the idle placeholders.
      expect(screen.getByText("12:34")).toBeInTheDocument();
      expect(screen.getByText("94.2")).toBeInTheDocument();
      expect(screen.getByText("27")).toBeInTheDocument();
    });
  });

  describe("connecting", () => {
    it("shows the connecting status and the abort label", () => {
      renderWithProviders(
        <ConnectionPanel {...baseProps({ phase: "connecting" })} />,
      );

      expect(screen.getByText("Connecting…")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /ABORT/ })).toBeInTheDocument();
    });
  });

  describe("interactions", () => {
    it("calls onPrimary when the primary button is clicked", async () => {
      const onPrimary = vi.fn();
      const user = userEvent.setup();
      renderWithProviders(
        <ConnectionPanel {...baseProps({ phase: "idle", onPrimary })} />,
      );

      await user.click(screen.getByRole("button", { name: /Connect/ }));
      expect(onPrimary).toHaveBeenCalledTimes(1);
    });

    it("calls onChange when the change affordance is clicked", async () => {
      const onChange = vi.fn();
      const user = userEvent.setup();
      renderWithProviders(
        <ConnectionPanel {...baseProps({ phase: "idle", onChange })} />,
      );

      await user.click(screen.getByRole("button", { name: /change/ }));
      expect(onChange).toHaveBeenCalledTimes(1);
    });
  });
});
