import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { DiagnosticsPanel } from "./DiagnosticsPanel";
import { renderWithProviders } from "../test/renderWithProviders";
import { api } from "../api";
import type { SpeedTestResult, StunResult } from "../api";

// The panel talks straight to the core (like the leak check on the Logs screen),
// so stub the two probe commands; every test arms its own resolution.
vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>();
  return {
    ...actual,
    api: { ...actual.api, runStunCheck: vi.fn(), runSpeedTest: vi.fn() },
  };
});

const P2P_STUN: StunResult = {
  udp_ok: true,
  nat_type: "endpoint-independent",
  external_ip: "198.51.100.7",
};

const SPEED: SpeedTestResult = {
  mbps: 94.3,
  sample_bytes: 10 * 1024 * 1024,
  duration_ms: 890,
};

describe("DiagnosticsPanel", () => {
  describe("STUN / NAT probe", () => {
    it("runs the probe and shows the reachability facts", async () => {
      vi.mocked(api.runStunCheck).mockResolvedValue(P2P_STUN);
      const user = userEvent.setup();
      renderWithProviders(<DiagnosticsPanel connected={false} />);

      await user.click(screen.getByRole("button", { name: "Check UDP / NAT" }));

      expect(await screen.findByText("reachable")).toBeInTheDocument();
      expect(
        screen.getByText("endpoint-independent (P2P-friendly)"),
      ).toBeInTheDocument();
      expect(screen.getByText("198.51.100.7")).toBeInTheDocument();
      expect(api.runStunCheck).toHaveBeenCalledTimes(1);
    });

    it("shows the blocked verdict and a dash when no server answered", async () => {
      vi.mocked(api.runStunCheck).mockResolvedValue({
        udp_ok: false,
        nat_type: "blocked",
      });
      const user = userEvent.setup();
      renderWithProviders(<DiagnosticsPanel connected={false} />);

      await user.click(screen.getByRole("button", { name: "Check UDP / NAT" }));

      // Scope to each fact: both the UDP verdict and the NAT class read "blocked"
      // here, so assert per-row rather than by bare text.
      const udpFact = (await screen.findByText("Outbound UDP")).closest(
        ".set-fact",
      );
      expect(udpFact).toHaveTextContent("blocked");
      // A blocked verdict is toned with the accent, not the healthy green.
      expect(udpFact).toHaveAttribute("data-tone", "signal");
      // external_ip omitted → the honest dash, not a stale/blank value.
      const ipFact = screen.getByText("External IP").closest(".set-fact");
      expect(ipFact).toHaveTextContent("—");
    });

    it("relabels and disables the button while the probe is in flight", async () => {
      // Hold the probe open so the transient loading state is observable.
      let resolve!: (value: StunResult) => void;
      vi.mocked(api.runStunCheck).mockReturnValue(
        new Promise<StunResult>((r) => {
          resolve = r;
        }),
      );
      const user = userEvent.setup();
      renderWithProviders(<DiagnosticsPanel connected={false} />);

      await user.click(screen.getByRole("button", { name: "Check UDP / NAT" }));
      expect(screen.getByRole("button", { name: "Probing…" })).toBeDisabled();

      resolve(P2P_STUN);
      expect(await screen.findByText("reachable")).toBeInTheDocument();
    });

    it("surfaces a probe failure as an alert and shows no facts", async () => {
      vi.mocked(api.runStunCheck).mockRejectedValue(new Error("no route"));
      const user = userEvent.setup();
      renderWithProviders(<DiagnosticsPanel connected={false} />);

      await user.click(screen.getByRole("button", { name: "Check UDP / NAT" }));

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "Couldn't reach any STUN server",
      );
      expect(screen.queryByText("External IP")).not.toBeInTheDocument();
    });
  });

  describe("speed test", () => {
    it("is gated off until connected, with a hint explaining why", () => {
      renderWithProviders(<DiagnosticsPanel connected={false} />);

      expect(screen.getByRole("button", { name: "Speed test" })).toBeDisabled();
      expect(
        screen.getByText("Connect to a node to measure tunnel throughput."),
      ).toBeInTheDocument();
    });

    it("runs when connected and reports the throughput and sample", async () => {
      vi.mocked(api.runSpeedTest).mockResolvedValue(SPEED);
      const user = userEvent.setup();
      renderWithProviders(<DiagnosticsPanel connected={true} />);

      const button = screen.getByRole("button", { name: "Speed test" });
      expect(button).toBeEnabled();
      await user.click(button);

      expect(await screen.findByText("94.3")).toBeInTheDocument();
      expect(screen.getByText("Mbps")).toBeInTheDocument();
      // Sample note: 10 MiB read in 0.89 s, formatted to one decimal.
      expect(screen.getByText(/10\.0 MB in 0\.9 s/)).toBeInTheDocument();
      expect(api.runSpeedTest).toHaveBeenCalledTimes(1);
    });

    it("never fires the command while disconnected", async () => {
      vi.mocked(api.runSpeedTest).mockResolvedValue(SPEED);
      const user = userEvent.setup();
      renderWithProviders(<DiagnosticsPanel connected={false} />);

      // The button is disabled, but prove the guard holds even if a click lands.
      await user.click(screen.getByRole("button", { name: "Speed test" }));
      expect(api.runSpeedTest).not.toHaveBeenCalled();
    });

    it("surfaces a speed-test failure as an alert", async () => {
      vi.mocked(api.runSpeedTest).mockRejectedValue(new Error("reset"));
      const user = userEvent.setup();
      renderWithProviders(<DiagnosticsPanel connected={true} />);

      await user.click(screen.getByRole("button", { name: "Speed test" }));

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "The speed test failed",
      );
    });
  });
});
