import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ProfilesScreen } from "./ProfilesScreen";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeProfile, makeTenebra } from "../test/fixtures";
import { api } from "../api";

// Only the profile-mutating calls matter here; keep the rest of the module intact
// so the screen and its import dialog mount normally.
vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      removeProfile: vi.fn().mockResolvedValue(undefined),
      ping: vi.fn().mockResolvedValue([]),
    },
  };
});

function renderScreen(overrides = {}) {
  const tenebra = makeTenebra({ profiles: [makeProfile({ name: "vpsxd.pro" })] });
  renderWithProviders(
    <ProfilesScreen
      tenebra={tenebra}
      selectedProfileId={null}
      onSelectProfile={vi.fn()}
      {...overrides}
    />,
  );
  return tenebra;
}

describe("ProfilesScreen two-step delete", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("arms on the first click and only deletes on the second", async () => {
    const tenebra = renderScreen();
    const user = userEvent.setup();

    // First click arms the button — the label flips to the confirm copy and
    // nothing is removed yet (this replaces the old blocking window.confirm).
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(
      screen.getByRole("button", { name: "Really remove?" }),
    ).toBeInTheDocument();
    expect(api.removeProfile).not.toHaveBeenCalled();

    // Second click within the window confirms the delete.
    await user.click(screen.getByRole("button", { name: "Really remove?" }));
    expect(api.removeProfile).toHaveBeenCalledWith("profile-1");
    await waitFor(() => expect(tenebra.refreshProfiles).toHaveBeenCalled());
  });

  it("reverts to the resting label after the 3s window lapses", () => {
    vi.useFakeTimers();
    try {
      renderScreen();

      fireEvent.click(screen.getByRole("button", { name: "Remove" }));
      expect(
        screen.getByRole("button", { name: "Really remove?" }),
      ).toBeInTheDocument();

      // Let the arm window elapse: the button disarms and no delete fires.
      act(() => {
        vi.advanceTimersByTime(3000);
      });

      expect(screen.getByRole("button", { name: "Remove" })).toBeInTheDocument();
      expect(api.removeProfile).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("arms only one card at a time", async () => {
    const tenebra = makeTenebra({
      profiles: [
        makeProfile({ id: "a", name: "alpha" }),
        makeProfile({ id: "b", name: "bravo" }),
      ],
    });
    renderWithProviders(
      <ProfilesScreen
        tenebra={tenebra}
        selectedProfileId={null}
        onSelectProfile={vi.fn()}
      />,
    );
    const user = userEvent.setup();

    const removeButtons = () =>
      screen.getAllByRole("button", { name: /^(Remove|Really remove\?)$/ });

    // Arm the first card.
    await user.click(removeButtons()[0]);
    expect(
      screen.getAllByRole("button", { name: "Really remove?" }),
    ).toHaveLength(1);

    // Arming the second supersedes it: still exactly one armed button.
    await user.click(removeButtons()[1]);
    const armed = screen.getAllByRole("button", { name: "Really remove?" });
    expect(armed).toHaveLength(1);
    expect(api.removeProfile).not.toHaveBeenCalled();
  });
});

describe("ProfilesScreen connect from a card", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("connects the chosen node and asks the shell to close the overlay", async () => {
    const onConnected = vi.fn();
    const tenebra = renderScreen({ onConnected });
    const user = userEvent.setup();

    // Expand the node list via the count toggle, then connect the first node.
    await user.click(screen.getByRole("button", { name: /nodes/i }));
    const connectButtons = screen.getAllByRole("button", { name: "Connect" });
    await user.click(connectButtons[0]);

    expect(tenebra.connect).toHaveBeenCalledWith("profile-1", "node-1");
    await waitFor(() => expect(onConnected).toHaveBeenCalledTimes(1));
  });
});
