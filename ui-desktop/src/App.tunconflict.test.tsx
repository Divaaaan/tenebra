import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";

import type { State } from "./api";
import { App } from "./App";
import { dictionaries } from "./i18n/strings";
import { subscribeToast } from "./lib/toast";
import { renderWithProviders } from "./test/renderWithProviders";

// The tun-conflict override, exercised through App rather than through
// TunConflictConfirm on its own.
//
// The component had tests; the wiring did not, and that is exactly where it
// broke: simple mode returns its own tree several hundred lines above the shell's
// modal layer, so the prompt was never rendered there. The connect then sat
// awaiting an answer nobody could give — `busy` stayed true and the one button
// the screen has was dead until the app was restarted. Anyone with a second VPN
// up hit it on the first press.
//
// So these tests mount the whole App, in both views, and drive the guard the way
// the core does: by refusing the connect.

const SIMPLE_KEY = "tenebra.simpleMode";

// The core's wording, verbatim (core/tunguard/tunguard.go). isTunConflict matches
// on the phrase, so a paraphrase here would test nothing.
const CONFLICT = new Error(
  "another VPN already owns the default route: Wintun (metric 5). Two tunnels " +
    "routing everything take the machine offline rather than sharing. Disconnect " +
    "it first, or override this check if you know the routes do not overlap",
);

const profile = {
  id: "p1",
  name: "Example subscription",
  source: "subscription" as const,
  url: "https://example.invalid/sub",
  nodes: [
    {
      id: "n1",
      name: "EX-TEST-01",
      protocol: "vless" as const,
      server: "198.51.100.10",
      port: 443,
    },
  ],
  updatedAt: "2026-01-01T00:00:00Z",
};

const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  listProfiles: vi.fn(),
  connect: vi.fn(),
  disconnect: vi.fn(),
  ping: vi.fn(),
  checkNodes: vi.fn(),
  leakCheck: vi.fn(),
  onState: vi.fn(),
  onTraffic: vi.fn(),
  onLog: vi.fn(),
  onProfilesChanged: vi.fn(),
  onAttempts: vi.fn(),
  onTrayConnect: vi.fn(),
  onTrayShow: vi.fn(),
  onDeepLink: vi.fn(),
  takeLaunchDeepLinks: vi.fn(),
}));

vi.mock("./api", () => ({
  api: {
    status: mocks.status,
    listProfiles: mocks.listProfiles,
    connect: mocks.connect,
    disconnect: mocks.disconnect,
    ping: mocks.ping,
    checkNodes: mocks.checkNodes,
    leakCheck: mocks.leakCheck,
  },
  onState: mocks.onState,
  onTraffic: mocks.onTraffic,
  onLog: mocks.onLog,
  onProfilesChanged: mocks.onProfilesChanged,
  onAttempts: mocks.onAttempts,
  onTrayConnect: mocks.onTrayConnect,
  onTrayShow: mocks.onTrayShow,
  onDeepLink: mocks.onDeepLink,
  takeLaunchDeepLinks: mocks.takeLaunchDeepLinks,
}));

vi.mock("./lib/updates", () => ({
  checkForUpdate: vi.fn().mockResolvedValue(null),
  inAppUpdatesSupported: vi.fn().mockResolvedValue(true),
  installUpdate: vi.fn().mockResolvedValue(undefined),
}));

function armEvents() {
  const noopUnlisten = () => {};
  for (const on of [
    mocks.onState,
    mocks.onTraffic,
    mocks.onLog,
    mocks.onProfilesChanged,
    mocks.onAttempts,
    mocks.onTrayConnect,
    mocks.onTrayShow,
  ]) {
    on.mockResolvedValue(noopUnlisten);
  }
  mocks.onDeepLink.mockResolvedValue(noopUnlisten);
  mocks.takeLaunchDeepLinks.mockResolvedValue([]);
}

// Wait until the profile's node has landed — it renders in both views (a list row
// in the shell, a <select> option in simple mode), so the selection effect has run
// and the primary action has a profile to connect.
async function mountReady() {
  const utils = renderWithProviders(<App />);
  await screen.findAllByText("EX-TEST-01");
  return utils;
}

// The one connect control, in whichever view is mounted: simple mode labels it
// "Connect", the shell prefixes the same word with its play mark. Anchored so it
// can never match the prompt's "Connect anyway".
function primaryButton() {
  return screen.getByRole("button", { name: /^(▶\s*)?Connect$/ });
}

const en = dictionaries.en;

describe("App tun-conflict override", () => {
  let toasts: string[] = [];
  let unsubscribe = () => {};

  beforeEach(() => {
    localStorage.clear();
    armEvents();
    mocks.status.mockResolvedValue({ state: "idle" } as State);
    mocks.listProfiles.mockResolvedValue([profile]);
    mocks.disconnect.mockResolvedValue({ state: "idle" } as State);
    mocks.ping.mockResolvedValue([]);
    mocks.checkNodes.mockResolvedValue({
      best: "n1",
      results: [
        {
          node: "n1",
          targets: [{ target: "https://a.example/204", stage: "ok", rttMs: 40 }],
        },
      ],
    });
    mocks.leakCheck.mockResolvedValue({
      connected: false,
      ip_verdict: "neutral",
      ip_message: "",
      dns: { status: "unavailable", message: "" },
    });
    // The guard refuses the first attempt; an override attempt gets through, the
    // way the core behaves once AllowTunConflict is set.
    mocks.connect
      .mockRejectedValueOnce(CONFLICT)
      .mockResolvedValue({ state: "connecting" } as State);

    toasts = [];
    unsubscribe = subscribeToast((m) => toasts.push(m));
  });

  afterEach(() => {
    unsubscribe();
  });

  it("asks in simple mode instead of hanging the button", async () => {
    localStorage.setItem(SIMPLE_KEY, "1");
    await mountReady();

    fireEvent.click(primaryButton());

    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent(en.daemon.tunConflictOverrideTitle);
    expect(dialog).toHaveTextContent(en.daemon.tunConflictOverride);
  });

  it("re-arms the simple-mode button when the override is declined", async () => {
    localStorage.setItem(SIMPLE_KEY, "1");
    await mountReady();

    fireEvent.click(primaryButton());
    await screen.findByRole("alertdialog");
    // Mid-question the button is held down — that part was never the bug.
    expect(primaryButton()).toBeDisabled();

    fireEvent.click(
      screen.getByRole("button", { name: en.daemon.tunConflictOverrideCancel }),
    );

    // Declining ends the attempt: the prompt closes and the button is pressable
    // again, so the user can turn the other VPN off and retry in place.
    await waitFor(() => expect(primaryButton()).toBeEnabled());
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    // And it stayed declined: no second, overriding connect went out.
    expect(mocks.connect).toHaveBeenCalledTimes(1);
  });

  it("connects with the override when the user confirms, in simple mode", async () => {
    localStorage.setItem(SIMPLE_KEY, "1");
    await mountReady();

    fireEvent.click(primaryButton());
    await screen.findByRole("alertdialog");
    fireEvent.click(
      screen.getByRole("button", { name: en.daemon.tunConflictOverrideConfirm }),
    );

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(2));
    // Same profile, same measured node — only the override flag is added, and
    // only for this attempt.
    expect(mocks.connect.mock.calls[0]).toEqual(["p1", "n1", undefined, undefined]);
    expect(mocks.connect.mock.calls[1]).toEqual(["p1", "n1", undefined, true]);
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
  });

  // The refusal was reported twice: once by the handler that offers the override,
  // and again by the outer catch after declining rethrew the same error. Two
  // identical lines read as two separate failures.
  it("reports the refusal once, not twice, when declined", async () => {
    localStorage.setItem(SIMPLE_KEY, "1");
    await mountReady();

    fireEvent.click(primaryButton());
    await screen.findByRole("alertdialog");
    fireEvent.click(
      screen.getByRole("button", { name: en.daemon.tunConflictOverrideCancel }),
    );
    await waitFor(() => expect(primaryButton()).toBeEnabled());

    expect(toasts.filter((m) => m === en.daemon.tunConflict)).toHaveLength(1);
  });

  // The shell has always rendered the prompt; keep it covered here too, so the
  // two views are known to behave the same rather than assumed to.
  it("asks in the full shell as well", async () => {
    localStorage.setItem(SIMPLE_KEY, "0");
    await mountReady();

    fireEvent.click(primaryButton());

    expect(await screen.findByRole("alertdialog")).toHaveTextContent(
      en.daemon.tunConflictOverrideTitle,
    );
    fireEvent.click(
      screen.getByRole("button", { name: en.daemon.tunConflictOverrideCancel }),
    );
    await waitFor(() => expect(primaryButton()).toBeEnabled());
  });

  // Switching views mid-question must not lose it: App keeps the pending ask in
  // its own state, and both trees render the same prompt.
  it("keeps the question up across a switch to the full shell", async () => {
    localStorage.setItem(SIMPLE_KEY, "1");
    const { container } = await mountReady();

    fireEvent.click(primaryButton());
    await screen.findByRole("alertdialog");

    fireEvent.click(screen.getByRole("button", { name: "Advanced view" }));
    await waitFor(() =>
      expect(container.querySelector(".app--simple")).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
  });
});
