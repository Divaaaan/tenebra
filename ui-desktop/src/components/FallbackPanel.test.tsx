import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";

import {
  FallbackPanel,
  attemptDisplayStatus,
  fallbackHead,
  protocolLabel,
} from "./FallbackPanel";
import { renderWithProviders } from "../test/renderWithProviders";
import type { Attempt, AttemptsEvent } from "../api";

function attempt(overrides: Partial<Attempt> & Pick<Attempt, "seq">): Attempt {
  return {
    protocol: "vless",
    node: "node-a",
    status: "waiting",
    last_good: false,
    ...overrides,
  };
}

describe("protocolLabel", () => {
  it("names the transports the walk steps through", () => {
    expect(protocolLabel("vless")).toBe("REALITY");
    expect(protocolLabel("hysteria2")).toBe("HYSTERIA2");
    expect(protocolLabel("amneziawg")).toBe("AMNEZIAWG");
  });
});

describe("attemptDisplayStatus", () => {
  it("keeps a waiting candidate 'waiting' before anything succeeds", () => {
    const items = [
      attempt({ seq: 1, status: "trying" }),
      attempt({ seq: 2, status: "waiting" }),
    ];
    expect(attemptDisplayStatus(items[1], items)).toBe("waiting");
  });

  it("reads a waiting candidate after a success as 'reserve'", () => {
    const items = [
      attempt({ seq: 1, status: "ok" }),
      attempt({ seq: 2, status: "waiting" }),
    ];
    expect(attemptDisplayStatus(items[1], items)).toBe("reserve");
  });

  it("passes settled statuses through unchanged", () => {
    const items = [attempt({ seq: 1, status: "blocked" })];
    expect(attemptDisplayStatus(items[0], items)).toBe("blocked");
  });
});

describe("fallbackHead", () => {
  it("counts the live attempt while the walk runs", () => {
    const event: AttemptsEvent = {
      outcome: "",
      items: [
        attempt({ seq: 1, status: "blocked" }),
        attempt({ seq: 2, status: "trying" }),
        attempt({ seq: 3, status: "waiting" }),
      ],
    };
    expect(fallbackHead(event)).toEqual({ kind: "attempt", tried: 2, total: 3 });
  });

  it("floors the count at one on the very first frame", () => {
    const event: AttemptsEvent = {
      outcome: "",
      items: [attempt({ seq: 1, status: "waiting" })],
    };
    expect(fallbackHead(event)).toEqual({ kind: "attempt", tried: 1, total: 1 });
  });

  it("reports the winning protocol on success", () => {
    const event: AttemptsEvent = {
      outcome: "ok",
      items: [
        attempt({ seq: 1, status: "blocked" }),
        attempt({ seq: 2, protocol: "hysteria2", status: "ok" }),
      ],
    };
    expect(fallbackHead(event)).toEqual({
      kind: "success",
      protocol: "hysteria2",
    });
  });

  it("reports a blocked walk once exhausted", () => {
    const event: AttemptsEvent = {
      outcome: "exhausted",
      items: [attempt({ seq: 1, status: "blocked" })],
    };
    expect(fallbackHead(event)).toEqual({ kind: "blocked" });
  });
});

describe("FallbackPanel", () => {
  it("renders one row per candidate with its protocol, node and status", () => {
    const event: AttemptsEvent = {
      outcome: "ok",
      items: [
        attempt({
          seq: 1,
          protocol: "vless",
          node: "n-ams",
          status: "blocked",
          last_good: true,
        }),
        attempt({ seq: 2, protocol: "hysteria2", node: "n-hel", status: "ok" }),
        attempt({
          seq: 3,
          protocol: "amneziawg",
          node: "n-ist",
          status: "waiting",
        }),
      ],
    };
    renderWithProviders(
      <FallbackPanel attempts={event} resolveNodeName={(id) => id} />,
    );

    expect(screen.getByText("REALITY")).toBeInTheDocument();
    expect(screen.getByText("HYSTERIA2")).toBeInTheDocument();
    expect(screen.getByText("AMNEZIAWG")).toBeInTheDocument();
    expect(screen.getByText("n-ams")).toBeInTheDocument();
    // The lead candidate carries the last-good chip.
    expect(screen.getByText("last-good")).toBeInTheDocument();
    // Header reports success and the winning transport, lowercased.
    expect(screen.getByText("success · hysteria2")).toBeInTheDocument();
    // Row statuses, including the derived "reserve" for the trailing candidate.
    expect(screen.getByText("blocked")).toBeInTheDocument();
    expect(screen.getByText("ok")).toBeInTheDocument();
    expect(screen.getByText("reserve")).toBeInTheDocument();
  });

  it("resolves node ids to display names", () => {
    const event: AttemptsEvent = {
      outcome: "",
      items: [attempt({ seq: 1, node: "id-1", status: "trying" })],
    };
    renderWithProviders(
      <FallbackPanel
        attempts={event}
        resolveNodeName={(id) => (id === "id-1" ? "amsterdam" : id)}
      />,
    );
    expect(screen.getByText("amsterdam")).toBeInTheDocument();
  });

  it("shows the exhausted header when every candidate is blocked", () => {
    const event: AttemptsEvent = {
      outcome: "exhausted",
      items: [
        attempt({ seq: 1, status: "blocked" }),
        attempt({ seq: 2, protocol: "hysteria2", status: "blocked" }),
      ],
    };
    renderWithProviders(<FallbackPanel attempts={event} />);
    expect(screen.getByText("all blocked")).toBeInTheDocument();
  });
});
