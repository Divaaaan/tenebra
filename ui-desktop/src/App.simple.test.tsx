import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";

import type { State } from "./api";
import { App } from "./App";
import { renderWithProviders } from "./test/renderWithProviders";

// Covers the two v0.4.0 shell behaviours layered onto App: the localStorage-driven
// simple-mode swap (read at mount, live on a `storage` event) and the Konami-code
// eclipse. The api is stubbed exactly as App.shell does — no Tauri, no network.

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

// Wait until the profile's node has landed — it renders in both modes (a list row
// in the shell, a <select> option in simple mode), so the selection effect has run
// and the primary action has a profile to connect.
async function mountReady() {
  const utils = renderWithProviders(<App />);
  await screen.findAllByText("EX-TEST-01");
  return utils;
}

describe("App simple mode", () => {
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

  it("starts in the full shell on a fresh install, with the setup steps in it", async () => {
    // The rich view is the default. What a first-run user lacked was never
    // fewer controls — it was the two setup steps being somewhere else, so they
    // are on the main screen instead of the shell being stripped.
    localStorage.removeItem(SIMPLE_KEY);
    const { container } = await mountReady();
    expect(container.querySelector(".app--simple")).not.toBeInTheDocument();
    expect(container.querySelector(".setup")).toBeInTheDocument();
  });

  it("renders the full shell once the user has opted out", async () => {
    localStorage.setItem(SIMPLE_KEY, "0");
    const { container } = await mountReady();
    expect(container.querySelector(".app--simple")).not.toBeInTheDocument();
    expect(
      screen.getByPlaceholderText("search node · de-fra"),
    ).toBeInTheDocument();
  });

  it("renders SimpleView when the flag is set at mount", async () => {
    localStorage.setItem(SIMPLE_KEY, "1");
    const { container } = await mountReady();

    expect(container.querySelector(".app--simple")).toBeInTheDocument();
    // The advanced surfaces (the node search, the routing/leak bottom bar) are gone.
    expect(
      screen.queryByPlaceholderText("search node · de-fra"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /leak-check/ }),
    ).not.toBeInTheDocument();
    // The minimal picker and its automatic option are present.
    expect(
      screen.getByRole("option", { name: "Automatic — fastest" }),
    ).toBeInTheDocument();
  });

  it("also accepts a plain \"true\" as the on value", async () => {
    localStorage.setItem(SIMPLE_KEY, "true");
    const { container } = await mountReady();
    expect(container.querySelector(".app--simple")).toBeInTheDocument();
  });

  it("switches live when a storage event reports the flag changed", async () => {
    // Start from the opted-out shell explicitly: simple mode is now the default,
    // so "no flag" would begin in the state this test is trying to switch INTO.
    localStorage.setItem(SIMPLE_KEY, "0");
    const { container } = await mountReady();
    expect(container.querySelector(".app--simple")).not.toBeInTheDocument();

    // Turn it on: another window wrote the key, we hear the storage event.
    act(() => {
      localStorage.setItem(SIMPLE_KEY, "1");
      window.dispatchEvent(new StorageEvent("storage", { key: SIMPLE_KEY }));
    });
    await waitFor(() =>
      expect(container.querySelector(".app--simple")).toBeInTheDocument(),
    );

    // And back off again — the swap is reversible.
    act(() => {
      localStorage.setItem(SIMPLE_KEY, "0");
      window.dispatchEvent(new StorageEvent("storage", { key: SIMPLE_KEY }));
    });
    await waitFor(() =>
      expect(container.querySelector(".app--simple")).not.toBeInTheDocument(),
    );
  });

  it("ignores a storage event for an unrelated key", async () => {
    localStorage.setItem(SIMPLE_KEY, "0");
    const { container } = await mountReady();
    act(() => {
      localStorage.setItem(SIMPLE_KEY, "1");
      window.dispatchEvent(
        new StorageEvent("storage", { key: "tenebra.lang" }),
      );
    });
    // Our key changed but the event named another; we don't re-read on it.
    expect(container.querySelector(".app--simple")).not.toBeInTheDocument();
  });

  it("connects from the simple-mode primary button", async () => {
    localStorage.setItem(SIMPLE_KEY, "1");
    await mountReady();

    fireEvent.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    expect(mocks.connect.mock.calls[0][0]).toBe("p1");
  });

  // The whole point of measuring before connecting: the exit is chosen by what
  // carried traffic, not by what answered a TCP dial fastest. A node that dials
  // instantly and then carries nothing is exactly what auto-select used to pick.
  it("connects to the node the check picked, not to auto", async () => {
    mocks.checkNodes.mockResolvedValue({
      best: "n-alive",
      results: [
        {
          node: "n-alive",
          targets: [
            { target: "https://a.example/204", stage: "ok", rttMs: 120 },
          ],
        },
      ],
    });
    localStorage.setItem(SIMPLE_KEY, "1");
    await mountReady();

    fireEvent.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    expect(mocks.checkNodes).toHaveBeenCalledWith("p1");
    expect(mocks.connect.mock.calls[0][1]).toBe("n-alive");
  });

  // With nothing usable the connect must still be attempted — the core's
  // fallback walk tries nodes in turn and may get through where one probe did
  // not. Refusing to connect would be a worse answer than a slow connect.
  it("still connects when the check finds no usable node", async () => {
    mocks.checkNodes.mockResolvedValue({ best: "", results: [] });
    localStorage.setItem(SIMPLE_KEY, "1");
    await mountReady();

    fireEvent.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    expect(mocks.connect.mock.calls[0][1]).toBeUndefined();
  });

  it("escapes back to the full shell from the advanced-view link", async () => {
    localStorage.setItem(SIMPLE_KEY, "1");
    const { container } = await mountReady();
    expect(container.querySelector(".app--simple")).toBeInTheDocument();

    // Simple mode hides Settings, so this link is the only exit. Clicking it must
    // return the full shell without a reload.
    fireEvent.click(screen.getByRole("button", { name: "Advanced view" }));

    await waitFor(() =>
      expect(container.querySelector(".app--simple")).not.toBeInTheDocument(),
    );
    // The advanced surfaces are back — the node search returns.
    expect(
      screen.getByPlaceholderText("search node · de-fra"),
    ).toBeInTheDocument();
    // And the flag is persisted off, so the escape survives a relaunch.
    expect(localStorage.getItem(SIMPLE_KEY)).toBe("false");
  });
});

// The eclipse egg moved from the Konami code to three wordmark taps; it is now
// covered by TopBar.test.tsx, so App only owns the simple-mode swap above.
