import { describe, expect, it } from "vitest";
import { fireEvent, screen } from "@testing-library/react";

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

  it("draws the consumption meter, filled to the used fraction, for a metered plan", () => {
    const profile = makeProfile({
      trafficUsed: 25 * 1024 ** 3,
      trafficTotal: 100 * 1024 ** 3,
    });
    const { container } = renderWithProviders(
      <TopBar activeProfile={profile} />,
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
      <TopBar activeProfile={profile} />,
    );
    expect(container.querySelector("svg.usage-bar")).not.toBeInTheDocument();
  });

  it("shows the premium badge when the active profile's tier is premium", () => {
    const profile = makeProfile({ name: "Acme VPN", managed: true, tier: "premium" });
    const { container } = renderWithProviders(
      <TopBar activeProfile={profile} />,
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
        <TopBar activeProfile={makeProfile(overrides)} />,
      );
      expect(container.querySelector(".acct-badge")).not.toBeInTheDocument();
      unmount();
    }
  });

  it("hides the Latin motto on the version badge for hover discovery", () => {
    // A quiet flavour string, surfaced only as the version's tooltip — never in
    // the visible chrome.
    renderWithProviders(<TopBar activeProfile={null} />);
    const ver = screen.getByText(`v${__APP_VERSION__}`);
    expect(ver).toHaveAttribute("title", "In tenebris lux");
  });

  it("reveals the credits card after seven wordmark taps", () => {
    const { container } = renderWithProviders(<TopBar activeProfile={null} />);
    const mark = screen.getByText("Tenebra");

    // Six taps are not enough — the reward is deliberately hard to hit by accident.
    for (let i = 0; i < 6; i += 1) {
      fireEvent.click(mark);
    }
    expect(container.querySelector(".credits")).not.toBeInTheDocument();

    // The seventh crosses the threshold.
    fireEvent.click(mark);
    expect(container.querySelector(".credits")).toBeInTheDocument();
    expect(screen.getByText("made in the dark")).toBeInTheDocument();
  });

  it("dismisses the credits card on click", () => {
    const { container } = renderWithProviders(<TopBar activeProfile={null} />);
    const mark = screen.getByText("Tenebra");
    for (let i = 0; i < 7; i += 1) {
      fireEvent.click(mark);
    }
    const card = container.querySelector(".credits-card");
    expect(card).toBeInTheDocument();

    fireEvent.click(card as Element);
    expect(container.querySelector(".credits")).not.toBeInTheDocument();
  });
});
