import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen } from "@testing-library/react";

import type { State } from "./api";
import { App } from "./App";
import { renderWithProviders } from "./test/renderWithProviders";

/**
 * Where the screen learns whether the bypass exists and whether it is running.
 *
 * It used to be a React state filled in by a manual import and reset by every
 * restart, so a machine whose core had installed a bundle on the first connect
 * and left the packet filter up drew a screen that said nothing was installed —
 * and went on asking for the archive. The core has always carried the answer in
 * its snapshot (`zapret_active` / `zapret_strategy` / `zapret_version`); these
 * pin that this is what is read, with nothing imported in this session.
 */

const SIMPLE_KEY = "tenebra.simpleMode";

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

// The launch update check would otherwise reach the (absent) updater plugin.
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

/** Mount and wait for the profile's node, i.e. for the snapshot to have landed. */
async function mountReady() {
  const utils = renderWithProviders(<App />);
  await screen.findAllByText("EX-TEST-01");
  return utils;
}

describe("bypass state comes from the core's snapshot", () => {
  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem(SIMPLE_KEY, "0");
    armEvents();
    mocks.status.mockResolvedValue({ state: "idle" } as State);
    mocks.listProfiles.mockResolvedValue([profile]);
    mocks.connect.mockResolvedValue({ state: "connecting" } as State);
    mocks.disconnect.mockResolvedValue({ state: "idle" } as State);
    mocks.ping.mockResolvedValue([]);
    mocks.checkNodes.mockResolvedValue({ results: [], best: "" });
    mocks.leakCheck.mockResolvedValue({
      connected: false,
      ip_verdict: "neutral",
      ip_message: "",
      dns: { status: "unavailable", message: "" },
    });
  });

  it("shows a bundle the core installed by itself as installed and running", async () => {
    mocks.status.mockResolvedValue({
      state: "idle",
      zapret_active: true,
      zapret_strategy: "general (FAKE TLS AUTO)",
      zapret_version: "1.10.2",
    } as State);

    const { container } = await mountReady();

    const stat = container.querySelector(".bypass-stat");
    expect(stat).toBeTruthy();
    expect(stat).toHaveClass("is-on");
    expect(stat?.textContent).toContain("general (FAKE TLS AUTO)");
  });

  it("shows an installed bundle that is not running as off, not as absent", async () => {
    mocks.status.mockResolvedValue({
      state: "idle",
      zapret_active: false,
      zapret_strategy: "general (FAKE TLS AUTO)",
      zapret_version: "1.10.2",
    } as State);

    const { container } = await mountReady();

    const stat = container.querySelector(".bypass-stat");
    expect(stat).toBeTruthy();
    expect(stat).not.toHaveClass("is-on");
  });

  it("says nothing about a bypass the core has not installed", async () => {
    const { container } = await mountReady();

    expect(container.querySelector(".bypass-stat")).toBeNull();
  });

  // A bundle installed from a source that named no release leaves the version
  // empty. A filter that is up is proof of a bundle regardless, and reading the
  // version alone would draw "nothing installed" over a running bypass.
  it("counts a running filter as installed even with no version stamped", async () => {
    mocks.status.mockResolvedValue({
      state: "idle",
      zapret_active: true,
      zapret_strategy: "general",
    } as State);

    const { container } = await mountReady();

    expect(container.querySelector(".bypass-stat")).toHaveClass("is-on");
  });

  it("carries the same snapshot into simple mode", async () => {
    localStorage.setItem(SIMPLE_KEY, "1");
    mocks.status.mockResolvedValue({
      state: "idle",
      zapret_active: true,
      zapret_strategy: "general (FAKE TLS AUTO)",
      zapret_version: "1.10.2",
    } as State);

    await mountReady();

    expect(screen.getByText(/bypass on/)).toBeInTheDocument();
    expect(screen.getByText(/general \(FAKE TLS AUTO\)/)).toBeInTheDocument();
  });

  // The screen no longer has anywhere to drop an archive, in either view: the
  // core fetches and installs one. A drop zone that outlived that is how the app
  // kept asking for work it already does.
  it("asks for no archive anywhere on the first screen", async () => {
    mocks.listProfiles.mockResolvedValue([]);
    renderWithProviders(<App />);

    expect(
      await screen.findByLabelText("Paste your subscription link"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/archive/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/zapret/i)).not.toBeInTheDocument();
  });
});
