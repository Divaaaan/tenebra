import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { HomeScreen } from "./HomeScreen";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeProfile, makeTenebra } from "../test/fixtures";
import type { State } from "../api";

describe("HomeScreen", () => {
  // RollingNumber reads matchMedia via useReducedMotion. restoreMocks wipes the
  // setup's default between tests, so re-install a "motion allowed" stub here.
  beforeEach(() => {
    window.matchMedia = vi.fn().mockReturnValue({
      matches: false,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    } as unknown as MediaQueryList);
  });

  it("shows the empty state and routes to Profiles when there are none", async () => {
    const onGoToProfiles = vi.fn();
    const tenebra = makeTenebra({ profiles: [] });
    const user = userEvent.setup();

    renderWithProviders(
      <HomeScreen
        tenebra={tenebra}
        selectedProfile={null}
        onSelectProfile={vi.fn()}
        onGoToProfiles={onGoToProfiles}
      />,
    );

    expect(
      screen.getByRole("heading", { name: "No profile yet" }),
    ).toBeInTheDocument();
    // The empty-state CTA is the only button here; it jumps to Profiles.
    await user.click(screen.getByRole("button", { name: "Profiles" }));
    expect(onGoToProfiles).toHaveBeenCalledTimes(1);
  });

  it("connects the selected profile with auto node from idle", async () => {
    const profile = makeProfile();
    const tenebra = makeTenebra({
      profiles: [profile],
      state: { state: "idle" } as State,
    });
    const user = userEvent.setup();

    renderWithProviders(
      <HomeScreen
        tenebra={tenebra}
        selectedProfile={profile}
        onSelectProfile={vi.fn()}
        onGoToProfiles={vi.fn()}
      />,
    );

    const dial = screen.getByRole("button", { name: /Connect/ });
    expect(dial).toBeInTheDocument();
    await user.click(dial);

    // No node picked in the quick-connect select, so node is undefined (auto).
    expect(tenebra.connect).toHaveBeenCalledTimes(1);
    expect(tenebra.connect).toHaveBeenCalledWith(profile.id, undefined);
  });

  it("shows traffic and disconnects when connected", async () => {
    const profile = makeProfile();
    const tenebra = makeTenebra({
      profiles: [profile],
      state: { state: "connected", profile: profile.id, node: "node-1" },
      traffic: { up: 1024, down: 2048, upRate: 100, downRate: 200 },
    });
    const user = userEvent.setup();

    renderWithProviders(
      <HomeScreen
        tenebra={tenebra}
        selectedProfile={profile}
        onSelectProfile={vi.fn()}
        onGoToProfiles={vi.fn()}
      />,
    );

    // The traffic panel only renders while connected.
    expect(screen.getByText("Download")).toBeInTheDocument();
    expect(screen.getByText("Upload")).toBeInTheDocument();

    const dial = screen.getByRole("button", { name: /Disconnect/ });
    await user.click(dial);
    expect(tenebra.disconnect).toHaveBeenCalledTimes(1);
  });

  it("reports a profile change through onSelectProfile", async () => {
    const a = makeProfile({ id: "profile-a", name: "Profile A" });
    const b = makeProfile({ id: "profile-b", name: "Profile B" });
    const onSelectProfile = vi.fn();
    const tenebra = makeTenebra({
      profiles: [a, b],
      state: { state: "idle" } as State,
    });
    const user = userEvent.setup();

    renderWithProviders(
      <HomeScreen
        tenebra={tenebra}
        selectedProfile={a}
        onSelectProfile={onSelectProfile}
        onGoToProfiles={vi.fn()}
      />,
    );

    // Two selects: the first lists profiles, the second lists nodes.
    const [profileSelect] = screen.getAllByRole("combobox");
    await user.selectOptions(profileSelect, "profile-b");
    expect(onSelectProfile).toHaveBeenCalledWith("profile-b");
  });
});
