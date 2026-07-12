import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";

import { TopBar } from "./TopBar";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeProfile } from "../test/fixtures";

// A no-op eclipse handler for the tests that don't exercise the egg.
const noEclipse = () => {};

describe("TopBar", () => {
  it("renders the Tenebra wordmark", () => {
    renderWithProviders(<TopBar activeProfile={null} onEclipse={noEclipse} />);
    expect(screen.getByText("Tenebra")).toBeInTheDocument();
  });

  it("shows the package version, not a stale literal", () => {
    // __APP_VERSION__ is injected from package.json by the vite/vitest define,
    // so a release bump can never leave the UI badge behind again.
    renderWithProviders(<TopBar activeProfile={null} onEclipse={noEclipse} />);
    expect(screen.getByText(`v${__APP_VERSION__}`)).toBeInTheDocument();
    expect(__APP_VERSION__).toMatch(/^\d+\.\d+\.\d+$/);
  });

  it("shows the no-subscription string when there is no active profile", () => {
    renderWithProviders(<TopBar activeProfile={null} onEclipse={noEclipse} />);
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
    renderWithProviders(<TopBar activeProfile={profile} onEclipse={noEclipse} />);

    expect(screen.getByText("Acme VPN")).toBeInTheDocument();
    expect(screen.getByText("1.0 GB / 50.0 GB")).toBeInTheDocument();
    expect(screen.getByText("in 5 days")).toBeInTheDocument();
    // The fallback copy must not appear once a subscription is active.
    expect(screen.queryByText("no subscription")).not.toBeInTheDocument();
  });

  it("draws the consumption meter, filled to the used fraction, for a metered plan", () => {
    const profile = makeProfile({
      trafficUsed: 25 * 1024 ** 3,
      trafficTotal: 100 * 1024 ** 3,
    });
    const { container } = renderWithProviders(
      <TopBar activeProfile={profile} onEclipse={noEclipse} />,
    );
    const bar = container.querySelector("svg.usage-bar");
    expect(bar).toBeInTheDocument();
    // Two rects: the track, then the fill at 25/100 of the 64px track.
    const rects = container.querySelectorAll("svg.usage-bar rect");
    expect(rects).toHaveLength(2);
    expect(rects[1]).toHaveAttribute("width", "16.0");
  });

  it("omits the consumption meter on an unmetered plan", () => {
    const profile = makeProfile({
      trafficUsed: 5 * 1024 ** 3,
      trafficTotal: 0,
    });
    const { container } = renderWithProviders(
      <TopBar activeProfile={profile} onEclipse={noEclipse} />,
    );
    expect(container.querySelector("svg.usage-bar")).not.toBeInTheDocument();
  });

  it("shows the premium badge when the active profile's tier is premium", () => {
    const profile = makeProfile({ name: "Acme VPN", managed: true, tier: "premium" });
    const { container } = renderWithProviders(
      <TopBar activeProfile={profile} onEclipse={noEclipse} />,
    );
    const badge = container.querySelector(".acct-badge--premium");
    expect(badge).toBeInTheDocument();
    expect(badge).toHaveTextContent("premium");
  });

  it("omits the premium badge for a free or unmarked subscription", () => {
    // No tier, an explicit free tier, and a managed-but-not-premium profile all
    // render without the badge — premium is the only state that earns it.
    for (const overrides of [
      {},
      { tier: "free" as const },
      { managed: true, tier: "free" as const },
    ]) {
      const { container, unmount } = renderWithProviders(
        <TopBar activeProfile={makeProfile(overrides)} onEclipse={noEclipse} />,
      );
      expect(container.querySelector(".acct-badge")).not.toBeInTheDocument();
      unmount();
    }
  });

  it("plays the eclipse after three wordmark taps", () => {
    // The eclipse egg: three taps on the wordmark within the window fire onEclipse
    // (the host plays the overlay). Two taps must not — the reward stays hidden
    // until the third.
    const onEclipse = vi.fn();
    renderWithProviders(<TopBar activeProfile={null} onEclipse={onEclipse} />);
    const mark = screen.getByText("Tenebra");

    fireEvent.click(mark);
    fireEvent.click(mark);
    expect(onEclipse).not.toHaveBeenCalled();

    fireEvent.click(mark);
    expect(onEclipse).toHaveBeenCalledTimes(1);
  });

  it("reveals the credits card after three version taps", () => {
    const { container } = renderWithProviders(
      <TopBar activeProfile={null} onEclipse={noEclipse} />,
    );
    const ver = screen.getByText(`v${__APP_VERSION__}`);

    // Two taps are not enough.
    fireEvent.click(ver);
    fireEvent.click(ver);
    expect(container.querySelector(".credits")).not.toBeInTheDocument();

    // The third crosses the threshold.
    fireEvent.click(ver);
    expect(container.querySelector(".credits")).toBeInTheDocument();
    expect(screen.getByText("made in the dark")).toBeInTheDocument();
  });

  it("dismisses the credits card on click", () => {
    const { container } = renderWithProviders(
      <TopBar activeProfile={null} onEclipse={noEclipse} />,
    );
    const ver = screen.getByText(`v${__APP_VERSION__}`);
    for (let i = 0; i < 3; i += 1) {
      fireEvent.click(ver);
    }
    const card = container.querySelector(".credits-card");
    expect(card).toBeInTheDocument();

    fireEvent.click(card as Element);
    expect(container.querySelector(".credits")).not.toBeInTheDocument();
  });
});
