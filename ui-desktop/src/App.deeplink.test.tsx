import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, fireEvent, waitFor, act } from "@testing-library/react";

import type { DeepLinkAction } from "./api";
import { App } from "./App";
import { renderWithProviders } from "./test/renderWithProviders";

// The security property under test: a `tenebra://connect` deep link — which any
// visited web page can hand to the app — must NOT connect on arrival. It has to
// pass through an explicit user confirmation first. We mount the real App with
// the api module stubbed, drive a live connect link into the captured
// `onDeepLink` handler, and assert the backend `connect` fires only after the
// user approves.

const profile = {
  id: "p1",
  name: "Demo subscription",
  source: "subscription" as const,
  url: "https://example.invalid/sub",
  nodes: [
    {
      id: "n1",
      name: "Amsterdam",
      protocol: "vless" as const,
      server: "198.51.100.10",
      port: 443,
    },
  ],
  updatedAt: "2026-01-01T00:00:00Z",
};

// Stable mock handles, created before the module factories run. The suite's
// `restoreMocks` wipes implementations between tests, so each one is re-armed in
// beforeEach rather than in the factory.
const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  listProfiles: vi.fn(),
  connect: vi.fn(),
  disconnect: vi.fn(),
  ping: vi.fn(),
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

// The live-deep-link handler App registers, captured so the test can play the
// role of the OS delivering a `tenebra://` URL while the app runs.
let deepLinkHandler: ((action: DeepLinkAction) => void) | null = null;

vi.mock("./api", () => ({
  api: {
    status: mocks.status,
    listProfiles: mocks.listProfiles,
    connect: mocks.connect,
    disconnect: mocks.disconnect,
    ping: mocks.ping,
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

// The launch update check would otherwise reach the (absent) updater plugin.
vi.mock("./lib/updates", () => ({
  checkForUpdate: vi.fn().mockResolvedValue(null),
  installUpdate: vi.fn().mockResolvedValue(undefined),
}));

async function mountAndDeliverConnect() {
  renderWithProviders(<App />);
  // Wait until App has registered its live deep-link handler.
  await waitFor(() => expect(deepLinkHandler).not.toBeNull());
  // The OS hands the running app a connect link. Wrapped in act because it flows
  // straight into React state (the confirmation prompt opening).
  await act(async () => {
    deepLinkHandler?.({ action: "connect", profile: "p1" });
  });
}

describe("App deep-link connect gate", () => {
  beforeEach(() => {
    const noopUnlisten = () => {};
    deepLinkHandler = null;
    localStorage.clear();

    mocks.status.mockResolvedValue({ state: "idle" });
    mocks.listProfiles.mockResolvedValue([profile]);
    mocks.connect.mockResolvedValue({ state: "connecting" });
    mocks.disconnect.mockResolvedValue({ state: "idle" });
    mocks.ping.mockResolvedValue([]);
    mocks.onState.mockResolvedValue(noopUnlisten);
    mocks.onTraffic.mockResolvedValue(noopUnlisten);
    mocks.onLog.mockResolvedValue(noopUnlisten);
    mocks.onProfilesChanged.mockResolvedValue(noopUnlisten);
    mocks.onAttempts.mockResolvedValue(noopUnlisten);
    mocks.onTrayConnect.mockResolvedValue(noopUnlisten);
    mocks.onTrayShow.mockResolvedValue(noopUnlisten);
    mocks.takeLaunchDeepLinks.mockResolvedValue([]);
    mocks.onDeepLink.mockImplementation(
      (h: (a: DeepLinkAction) => void) => {
        deepLinkHandler = h;
        return Promise.resolve(noopUnlisten);
      },
    );
  });

  it("shows a confirmation and does not connect on arrival", async () => {
    await mountAndDeliverConnect();

    // The prompt is up, naming the subscription…
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent(/Demo subscription/);
    // …and crucially nothing has connected yet.
    expect(mocks.connect).not.toHaveBeenCalled();
  });

  it("connects only after the user approves", async () => {
    await mountAndDeliverConnect();
    await screen.findByRole("alertdialog");

    fireEvent.click(screen.getByRole("button", { name: "Connect" }));

    // Now — and only now — the backend connect fires, for the named profile.
    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    expect(mocks.connect.mock.calls[0][0]).toBe("p1");
  });

  it("never connects when the user declines", async () => {
    await mountAndDeliverConnect();
    await screen.findByRole("alertdialog");

    fireEvent.click(screen.getByRole("button", { name: "Not now" }));

    // The prompt is gone and no connect was issued.
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(mocks.connect).not.toHaveBeenCalled();
  });
});
