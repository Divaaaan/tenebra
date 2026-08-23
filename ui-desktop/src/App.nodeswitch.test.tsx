import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";

import type { State } from "./api";
import { App } from "./App";
import { renderWithProviders } from "./test/renderWithProviders";

// Picking another node while the tunnel is up must report what actually
// happened. The core steers the running sing-box when it can — the reply stays
// `connected`, because nothing was reconnected — and rebuilds the tunnel when it
// cannot, replying `connecting`. Showing "reconnecting" for both would teach the
// user that changing exits costs them their session, which is the very thing the
// live switch removed.

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
    {
      id: "n2",
      name: "EX-TEST-02",
      protocol: "vless" as const,
      server: "198.51.100.11",
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

// Mount with the tunnel already up on the first node, then hand back the row of
// the second one so a test can pick it.
async function mountConnected() {
  renderWithProviders(<App />);
  await screen.findAllByText("EX-TEST-01");
  const row = (await screen.findAllByText("EX-TEST-02"))[0].closest(".srv-row");
  if (!row) throw new Error("no row for EX-TEST-02");
  return row;
}

describe("picking another node while connected", () => {
  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem("tenebra.simpleMode", "0");
    armEvents();
    mocks.status.mockResolvedValue({
      state: "connected",
      profile: "p1",
      node: "n1",
    } as State);
    mocks.listProfiles.mockResolvedValue([profile]);
    mocks.disconnect.mockResolvedValue({ state: "idle" } as State);
    mocks.ping.mockResolvedValue([]);
    mocks.leakCheck.mockResolvedValue({
      connected: true,
      ip_verdict: "neutral",
      ip_message: "",
      dns: { status: "unavailable", message: "" },
    });
  });

  it("says the connection was kept when the core steered the live tunnel", async () => {
    mocks.connect.mockResolvedValue({
      state: "connected",
      profile: "p1",
      node: "n2",
    } as State);

    const row = await mountConnected();
    fireEvent.click(row);

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    expect(mocks.connect.mock.calls[0].slice(0, 2)).toEqual(["p1", "n2"]);
    await screen.findByText("exit · EX-TEST-02 · connection kept");
  });

  it("says it is reconnecting when the tunnel had to be rebuilt", async () => {
    mocks.connect.mockResolvedValue({
      state: "connecting",
      profile: "p1",
      node: "n2",
    } as State);

    const row = await mountConnected();
    fireEvent.click(row);

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    await screen.findByText("exit · EX-TEST-02 · reconnecting");
  });
});
