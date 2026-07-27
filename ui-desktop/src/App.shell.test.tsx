import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";

import type { State } from "./api";
import { App } from "./App";
import { renderWithProviders } from "./test/renderWithProviders";

// Exercises the App-level keyboard shortcuts against a stubbed api: Space is the
// primary connect/disconnect/abort (gated by field focus and by an open
// overlay), and "/" fires the cross-zone focus-search event. Demo data is
// neutral — no handoff node/profile names leak into the suite.

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

// Render App and wait until the profile's node has landed (it shows in both the
// current-node card and the list, hence findAll) — the selection effect has run
// by then, so the primary action has a profile to connect.
async function mountReady() {
  renderWithProviders(<App />);
  await screen.findAllByText("EX-TEST-01");
}

describe("App keyboard shortcuts", () => {
  beforeEach(() => {
    localStorage.clear();
    armEvents();
    mocks.status.mockResolvedValue({ state: "idle" } as State);
    mocks.listProfiles.mockResolvedValue([profile]);
    mocks.connect.mockResolvedValue({ state: "connecting" } as State);
    mocks.disconnect.mockResolvedValue({ state: "idle" } as State);
    mocks.ping.mockResolvedValue([]);
    mocks.leakCheck.mockResolvedValue({
      connected: false,
      ip_verdict: "neutral",
      ip_message: "",
      dns: { status: "unavailable", message: "" },
    });
  });

  describe("Space", () => {
    it("starts a connect when nothing owns the keyboard", async () => {
      await mountReady();

      fireEvent.keyDown(document.body, { key: " " });

      await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
      expect(mocks.connect.mock.calls[0][0]).toBe("p1");
    });

    it("is ignored while a text field is focused", async () => {
      await mountReady();

      const search = screen.getByPlaceholderText("search node · de-fra");
      fireEvent.keyDown(search, { key: " " });

      // Give any errant async connect a chance to fire before asserting it didn't.
      await Promise.resolve();
      expect(mocks.connect).not.toHaveBeenCalled();
    });

    it("is ignored while an overlay is open", async () => {
      await mountReady();

      fireEvent.click(screen.getByRole("button", { name: /leak-check/ }));
      await screen.findByRole("heading", { name: "Logs" });

      fireEvent.keyDown(document.body, { key: " " });

      await Promise.resolve();
      expect(mocks.connect).not.toHaveBeenCalled();
    });
  });

  describe("slash", () => {
    let focusSearch: ReturnType<typeof vi.fn>;

    beforeEach(() => {
      focusSearch = vi.fn();
      window.addEventListener("tenebra:focus-search", focusSearch);
    });
    afterEach(() => {
      window.removeEventListener("tenebra:focus-search", focusSearch);
    });

    it("dispatches the focus-search event", async () => {
      await mountReady();

      fireEvent.keyDown(document.body, { key: "/" });

      expect(focusSearch).toHaveBeenCalledTimes(1);
    });

    it("does not dispatch while an overlay is open", async () => {
      await mountReady();

      fireEvent.click(screen.getByRole("button", { name: /leak-check/ }));
      await screen.findByRole("heading", { name: "Logs" });

      fireEvent.keyDown(document.body, { key: "/" });

      expect(focusSearch).not.toHaveBeenCalled();
    });
  });
});
