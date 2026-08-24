import { describe, expect, it, vi } from "vitest";
import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ConnectionPanel, shouldShowFallback } from "./ConnectionPanel";
import { renderWithProviders } from "../test/renderWithProviders";
import type { AttemptsEvent, ConnectionState } from "../api";

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

  describe("primary button", () => {
    it("is clickable by default", async () => {
      const props = baseProps({ phase: "idle" });
      const user = userEvent.setup();
      renderWithProviders(<ConnectionPanel {...props} />);

      await user.click(screen.getByRole("button", { name: /Connect/ }));
      expect(props.onPrimary).toHaveBeenCalledTimes(1);
    });

    it("is disabled when the host says there is nothing to act on", async () => {
      // The shell sets this when a click could not do anything (no profile to
      // connect to). A button that looks live and silently eats the click is
      // the thing being fixed.
      const props = baseProps({ phase: "idle", disabled: true });
      const user = userEvent.setup();
      renderWithProviders(<ConnectionPanel {...props} />);

      const button = screen.getByRole("button", { name: /Connect/ });
      expect(button).toBeDisabled();
      await user.click(button);
      expect(props.onPrimary).not.toHaveBeenCalled();
    });
  });

  describe("connecting", () => {
    it("shows the connecting status, the abort label and the generic sub-line", () => {
      renderWithProviders(
        <ConnectionPanel {...baseProps({ phase: "connecting" })} />,
      );

      expect(screen.getByText("Connecting…")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /ABORT/ })).toBeInTheDocument();
      expect(
        screen.getByText("establishing tunnel · negotiating · · ·"),
      ).toBeInTheDocument();
    });

    it("shows a backend transport notice in place of the generic sub-line", () => {
      // The GUI synthesizes a connecting state with a message while it
      // re-dials a restarted service; that message must reach the user
      // instead of the (untrue) tunnel-negotiation line.
      renderWithProviders(
        <ConnectionPanel
          {...baseProps({
            phase: "connecting",
            errorMsg: "Reconnecting to the Tenebra service…",
          })}
        />,
      );

      expect(
        screen.getByText("Reconnecting to the Tenebra service…"),
      ).toBeInTheDocument();
      expect(
        screen.queryByText("establishing tunnel · negotiating · · ·"),
      ).not.toBeInTheDocument();
    });
  });

  describe("health_reconnecting", () => {
    it("reads apart from a plain connect: own word, sub-line and accent", () => {
      const { container } = renderWithProviders(
        <ConnectionPanel {...baseProps({ phase: "health_reconnecting" })} />,
      );

      // Its own status word, not the connecting one.
      expect(screen.getByText("Reconnecting…")).toBeInTheDocument();
      expect(screen.queryByText("Connecting…")).not.toBeInTheDocument();

      // The auto-failover sub-line, distinct from the generic negotiating line so
      // an unattended switch never looks like a user connect.
      expect(
        screen.getByText("node failed · switching to a healthy exit on its own"),
      ).toBeInTheDocument();
      expect(
        screen.queryByText("establishing tunnel · negotiating · · ·"),
      ).not.toBeInTheDocument();

      // The distinct visual hook the stylesheet keys the accent/animation off.
      expect(
        container.querySelector(".conn-word.health_reconnecting"),
      ).toBeInTheDocument();
      expect(
        container.querySelector(".connect-btn.reconnecting"),
      ).toBeInTheDocument();
    });

    it("offers the in-flight abort affordance while recovering", () => {
      renderWithProviders(
        <ConnectionPanel {...baseProps({ phase: "health_reconnecting" })} />,
      );
      // A recovery is in progress, so the CTA is the abort, not connect/disconnect.
      expect(screen.getByRole("button", { name: /ABORT/ })).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: /Connect/ }),
      ).not.toBeInTheDocument();
    });
  });

  // The node check runs before any tunnel exists, so the core has no phase to
  // report and the screen sat on "Disconnected", motionless, for the several
  // seconds it takes — the longest wait in the product, and the one place the UI
  // said nothing at all.
  describe("measuring nodes", () => {
    it("says work is under way instead of leaving the idle screen still", () => {
      const { container } = renderWithProviders(
        <ConnectionPanel {...baseProps({ phase: "idle", checking: true })} />,
      );

      expect(screen.getByText("Measuring…")).toBeInTheDocument();
      expect(screen.queryByText("Disconnected")).not.toBeInTheDocument();
      expect(
        screen.getByText("probing every node · finding one that carries traffic"),
      ).toBeInTheDocument();

      // The hooks the stylesheet drives the scanning indicator and the rail off.
      expect(container.querySelector(".conn-word.checking")).toBeInTheDocument();
      expect(container.querySelector(".conn-rail.is-live")).toBeInTheDocument();
      expect(container.querySelector(".connect-btn.checking")).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: /MEASURING/ }),
      ).toHaveAttribute("aria-busy", "true");
    });

    it("never speaks over a phase the core is actually reporting", () => {
      // A check left running as the connect proceeds must not keep claiming the
      // screen: a stale "measuring" over a live handshake is the same lie in the
      // other direction.
      const { container } = renderWithProviders(
        <ConnectionPanel
          {...baseProps({ phase: "connecting", checking: true })}
        />,
      );

      expect(screen.getByText("Connecting…")).toBeInTheDocument();
      expect(screen.queryByText("Measuring…")).not.toBeInTheDocument();
      expect(container.querySelector(".conn-word.checking")).toBeNull();
      expect(container.querySelector(".connect-btn.pending")).toBeInTheDocument();
    });

    it("keeps the rail in the layout at rest, so nothing jumps when it starts", () => {
      // The rail used to mount with the work, shifting everything under it by 4px
      // at the exact moment the status changed.
      const { container } = renderWithProviders(
        <ConnectionPanel {...baseProps({ phase: "idle" })} />,
      );
      expect(container.querySelector(".conn-rail")).toBeInTheDocument();
      expect(container.querySelector(".conn-rail.is-live")).toBeNull();
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

    it("broadcasts a focus-search intent when the change affordance is clicked", async () => {
      const user = userEvent.setup();
      const onFocusSearch = vi.fn();
      window.addEventListener("tenebra:focus-search", onFocusSearch);
      try {
        renderWithProviders(
          <ConnectionPanel {...baseProps({ phase: "idle" })} />,
        );
        await user.click(screen.getByRole("button", { name: /change/ }));
        expect(onFocusSearch).toHaveBeenCalledTimes(1);
      } finally {
        window.removeEventListener("tenebra:focus-search", onFocusSearch);
      }
    });
  });

  describe("premium chrome", () => {
    it("shows the active route in the eyebrow when a mode is given", () => {
      renderWithProviders(
        <ConnectionPanel {...baseProps({ phase: "connected", routing: "smart" })} />,
      );
      expect(screen.getByText("Routing · Smart")).toBeInTheDocument();
    });

    it("omits the route when no routing mode is given", () => {
      renderWithProviders(<ConnectionPanel {...baseProps({ phase: "idle" })} />);
      expect(screen.queryByText(/Routing ·/)).not.toBeInTheDocument();
    });

    it("renders the space keycap hint beside the CTA", () => {
      renderWithProviders(<ConnectionPanel {...baseProps({ phase: "idle" })} />);
      expect(screen.getByText("space")).toBeInTheDocument();
    });

    it("marks an auto-picked exit with the AUTO chip", () => {
      renderWithProviders(
        <ConnectionPanel
          {...baseProps({ phase: "connected", auto: true, exitServer: "203.0.113.7" })}
        />,
      );
      expect(screen.getByText("auto")).toBeInTheDocument();
    });

    it("omits the AUTO chip for a hand-picked node", () => {
      renderWithProviders(
        <ConnectionPanel
          {...baseProps({ phase: "connected", auto: false, exitServer: "203.0.113.7" })}
        />,
      );
      expect(screen.queryByText("auto")).not.toBeInTheDocument();
    });

    it("shows the ping meter when a latency is known and hides it otherwise", () => {
      const { container, rerender } = renderWithProviders(
        <ConnectionPanel {...baseProps({ phase: "connected", ping: "27" })} />,
      );
      expect(container.querySelector(".ping-scale")).toBeInTheDocument();

      rerender(<ConnectionPanel {...baseProps({ phase: "idle", ping: "—" })} />);
      expect(container.querySelector(".ping-scale")).not.toBeInTheDocument();
    });
  });

  describe("fallback swap", () => {
    const walk: AttemptsEvent = {
      outcome: "",
      items: [
        { seq: 1, protocol: "vless", node: "n1", status: "trying", last_good: false },
      ],
    };

    it("replaces the node card with the fallback walk while connecting", () => {
      renderWithProviders(
        <ConnectionPanel {...baseProps({ phase: "connecting", attempts: walk })} />,
      );
      expect(screen.getByText("Protocol fallback")).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: /change/ }),
      ).not.toBeInTheDocument();
    });

    it("keeps the walk visible after an exhausted outcome", () => {
      const dead: AttemptsEvent = {
        outcome: "exhausted",
        items: [
          { seq: 1, protocol: "vless", node: "n1", status: "blocked", last_good: false },
        ],
      };
      renderWithProviders(
        <ConnectionPanel
          {...baseProps({
            phase: "error",
            errorMsg: "every protocol was blocked",
            attempts: dead,
          })}
        />,
      );
      expect(screen.getByText("Protocol fallback")).toBeInTheDocument();
      // The header's terse "all blocked" status, distinct from the error sub-line.
      expect(screen.getByText("all blocked")).toBeInTheDocument();
    });

    it("lingers on success, then restores the node card after the window", () => {
      vi.useFakeTimers();
      try {
        const ok: AttemptsEvent = {
          outcome: "ok",
          items: [
            { seq: 1, protocol: "hysteria2", node: "n1", status: "ok", last_good: false },
          ],
        };
        renderWithProviders(
          <ConnectionPanel {...baseProps({ phase: "connected", attempts: ok })} />,
        );
        expect(screen.getByText("Protocol fallback")).toBeInTheDocument();
        expect(
          screen.queryByRole("button", { name: /change/ }),
        ).not.toBeInTheDocument();

        act(() => {
          vi.advanceTimersByTime(2200);
        });

        expect(screen.queryByText("Protocol fallback")).not.toBeInTheDocument();
        expect(
          screen.getByRole("button", { name: /change/ }),
        ).toBeInTheDocument();
      } finally {
        vi.useRealTimers();
      }
    });
  });
});

describe("shouldShowFallback", () => {
  const items: AttemptsEvent["items"] = [];

  it("is hidden without a snapshot", () => {
    expect(shouldShowFallback(null, "connecting", false)).toBe(false);
    expect(shouldShowFallback(undefined, "connecting", false)).toBe(false);
  });

  it("shows an in-flight walk only while connecting", () => {
    const walk: AttemptsEvent = { outcome: "", items };
    expect(shouldShowFallback(walk, "connecting", false)).toBe(true);
    expect(shouldShowFallback(walk, "connected", false)).toBe(false);
  });

  it("shows a settled 'ok' walk only during the linger window", () => {
    const ok: AttemptsEvent = { outcome: "ok", items };
    expect(shouldShowFallback(ok, "connected", true)).toBe(true);
    expect(shouldShowFallback(ok, "connected", false)).toBe(false);
  });

  it("shows an exhausted walk only while the error state persists", () => {
    const dead: AttemptsEvent = { outcome: "exhausted", items };
    expect(shouldShowFallback(dead, "error", false)).toBe(true);
    expect(shouldShowFallback(dead, "idle", false)).toBe(false);
  });
});
