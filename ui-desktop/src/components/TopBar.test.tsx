import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";

import { TopBar } from "./TopBar";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeProfile } from "../test/fixtures";

describe("TopBar", () => {
  it("renders the Tenebra wordmark", () => {
    renderWithProviders(<TopBar activeProfile={null} />);
    expect(screen.getByText("Tenebra")).toBeInTheDocument();
  });

  it("shows the package version, not a stale literal", () => {
    // __APP_VERSION__ is injected from package.json by the vite/vitest define,
    // so a release bump can never leave the UI badge behind again.
    renderWithProviders(<TopBar activeProfile={null} />);
    expect(screen.getByText(`v${__APP_VERSION__}`)).toBeInTheDocument();
    expect(__APP_VERSION__).toMatch(/^\d+\.\d+\.\d+$/);
  });

  it("shows the no-subscription string when there is no active profile", () => {
    renderWithProviders(<TopBar activeProfile={null} />);
    expect(screen.getByText("no subscription")).toBeInTheDocument();
  });

  it("shows the subscription name, traffic usage and an expiry phrase", () => {
    // A date five calendar days out yields the "in N days" phrasing rather than
    // an absolute date (which only kicks in past a month) or today/tomorrow.
    const inFiveDays = new Date();
    inFiveDays.setDate(inFiveDays.getDate() + 5);

    const profile = makeProfile({
      name: "Acme VPN",
      // 1 GiB used of 50 GiB → "1.0 GB / 50.0 GB" (formatBytes keeps one decimal
      // below 100) via formatTrafficUsage.
      trafficUsed: 1024 ** 3,
      trafficTotal: 50 * 1024 ** 3,
      expiresAt: inFiveDays.toISOString(),
    });
    renderWithProviders(<TopBar activeProfile={profile} />);

    expect(screen.getByText("Acme VPN")).toBeInTheDocument();
    expect(screen.getByText("1.0 GB / 50.0 GB")).toBeInTheDocument();
    expect(screen.getByText("in 5 days")).toBeInTheDocument();
    // The fallback copy must not appear once a subscription is active.
    expect(screen.queryByText("no subscription")).not.toBeInTheDocument();
  });
});
