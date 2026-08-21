import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { ServiceCheck } from "../api";
import { renderWithProviders } from "../test/renderWithProviders";
import { ServiceChecks } from "./ServiceChecks";

const ok = (service: string, rttMs: number): ServiceCheck => ({
  service,
  ok: true,
  rttMs,
  detail: `probe://${service}`,
});

describe("ServiceChecks", () => {
  it("shows nothing before a check has run", () => {
    const { container } = renderWithProviders(
      <ServiceChecks checks={[]} checking={false} />,
    );
    expect(container.querySelector(".svc-checks")).not.toBeInTheDocument();
  });

  // The latency is the point, not the tick: "voice works" and "voice works at
  // 240ms" are different answers, and the whole routing split exists to move
  // between them.
  it("names each service with what it measured", () => {
    renderWithProviders(
      <ServiceChecks
        checks={[ok("video", 41), ok("voice", 89), ok("games", 9)]}
        checking={false}
      />,
    );
    expect(screen.getByText("YouTube")).toBeInTheDocument();
    expect(screen.getByText("41 ms")).toBeInTheDocument();
    expect(screen.getByText("89 ms")).toBeInTheDocument();
    expect(screen.getByText("9 ms")).toBeInTheDocument();
  });

  // A failure must read as a failure, not as a suspiciously fast zero.
  it("marks a failed check and never shows it a latency", () => {
    const { container } = renderWithProviders(
      <ServiceChecks
        checks={[{ service: "voice", ok: false, rttMs: 0, detail: "gw:443" }]}
        checking={false}
      />,
    );
    expect(screen.getByText("not working")).toBeInTheDocument();
    expect(screen.queryByText("0 ms")).not.toBeInTheDocument();
    expect(container.querySelector(".svc-check.is-bad")).toBeInTheDocument();
  });

  // The destination rides along so a failure can be repeated by hand rather
  // than argued about.
  it("carries the probed destination for a failure", () => {
    renderWithProviders(
      <ServiceChecks
        checks={[
          { service: "video", ok: false, rttMs: 0, detail: "https://x/204" },
        ]}
        checking={false}
      />,
    );
    expect(screen.getByTitle("https://x/204")).toBeInTheDocument();
  });

  it("says it is working while the probes are in flight", () => {
    renderWithProviders(<ServiceChecks checks={[]} checking />);
    expect(screen.getByText("checking what works…")).toBeInTheDocument();
  });
});
