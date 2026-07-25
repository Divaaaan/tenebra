import { afterEach, describe, expect, it, vi, beforeEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { State } from "./api";
import { App } from "./App";
import { dictionaries } from "./i18n/strings";
import { subscribeToast } from "./lib/toast";
import { renderWithProviders } from "./test/renderWithProviders";

// How the shell behaves against a core that is not there, has nothing to offer,
// or refuses what it is sent. All three used to look identical from the outside:
// a fully drawn window where clicking did nothing whatsoever. The api is stubbed
// exactly as App.shell does — no Tauri, no network.

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
  setKillSwitch: vi.fn(),
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
    setKillSwitch: mocks.setKillSwitch,
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

/** The connect / disconnect / abort control in the connection panel. */
function primaryButton() {
  return screen.getByRole("button", { name: /Connect|Disconnect|ABORT/ });
}

describe("App bootstrap", () => {
  beforeEach(() => {
    localStorage.clear();
    armEvents();
    mocks.status.mockResolvedValue({ state: "idle" } as State);
    mocks.listProfiles.mockResolvedValue([]);
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

  describe("core unreachable", () => {
    const DOWN = "the connection to tenebra-core is closed";

    it("says the background service is not answering", async () => {
      mocks.status.mockRejectedValue(DOWN);
      mocks.listProfiles.mockRejectedValue(DOWN);

      renderWithProviders(<App />);

      // A named condition in the banner strip the shell already uses for the
      // update and daemon-skew notices — not a window that merely sits there.
      expect(
        await screen.findByText(/No connection to the background service/),
      ).toBeInTheDocument();
    });

    it("does not also accuse the service of being out of date", async () => {
      mocks.status.mockRejectedValue(DOWN);
      mocks.listProfiles.mockRejectedValue(DOWN);

      renderWithProviders(<App />);
      await screen.findByText(/No connection to the background service/);

      // The skew check latches its verdict from the hook's placeholder "idle"
      // state, so an unreachable core used to read as a *stale* one — the wrong
      // diagnosis, contradicting the banner right above it. A version is only
      // knowable from a snapshot that actually arrived.
      expect(screen.queryByText(/predates/)).not.toBeInTheDocument();
    });

    it("clears the notice once a retry reaches the core", async () => {
      mocks.status.mockRejectedValueOnce(DOWN).mockResolvedValue({
        state: "idle",
      } as State);
      mocks.listProfiles.mockRejectedValueOnce(DOWN).mockResolvedValue([profile]);

      renderWithProviders(<App />);
      await screen.findByText(/No connection to the background service/);

      // The retry lands on its own (real timers, ~500ms of backoff).
      await waitFor(
        () =>
          expect(
            screen.queryByText(/No connection to the background service/),
          ).not.toBeInTheDocument(),
        { timeout: 4000 },
      );
      expect(await screen.findAllByText("EX-TEST-01")).not.toHaveLength(0);
    });
  });

  describe("primary button", () => {
    it("is disabled while there is no profile to connect to", async () => {
      renderWithProviders(<App />);

      // handlePrimary has no branch for a null profile, so a live-looking
      // button here is a button that silently eats the click. SimpleView
      // already disables its own for exactly this reason.
      await waitFor(() => expect(primaryButton()).toBeDisabled());
    });

    it("is live once a profile has loaded", async () => {
      mocks.listProfiles.mockResolvedValue([profile]);

      renderWithProviders(<App />);
      await screen.findAllByText("EX-TEST-01");

      expect(primaryButton()).toBeEnabled();
    });

    it("stays live while connected so the tunnel can always be taken down", async () => {
      // A tunnel is up but the profile list never arrived: disconnect must not
      // be gated on the selection.
      mocks.status.mockResolvedValue({
        state: "connected",
        node: "n1",
      } as State);

      renderWithProviders(<App />);

      await waitFor(() =>
        expect(
          screen.getByRole("button", { name: /Disconnect/ }),
        ).toBeEnabled(),
      );
    });
  });

  // The bottom bar drives two core-owned settings the same way the Settings
  // screen drives the rest, and used to swallow their failures the same way:
  // the control is drawn from the core's echo, so a refusal left it sitting
  // where it was with nothing said.
  describe("refused commands", () => {
    const en = dictionaries.en;
    const detach: (() => void)[] = [];
    afterEach(() => {
      detach.splice(0).forEach((fn) => fn());
    });

    function captureToasts(): string[] {
      const seen: string[] = [];
      detach.push(subscribeToast((m) => seen.push(m)));
      return seen;
    }

    it("reports a refused kill-switch toggle", async () => {
      mocks.listProfiles.mockResolvedValue([profile]);
      mocks.setKillSwitch.mockRejectedValue("the service is still down");
      const toasts = captureToasts();
      renderWithProviders(<App />);
      await screen.findAllByText("EX-TEST-01");

      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: /kill-switch/i }));

      await waitFor(() => expect(toasts).toContain(en.daemon.commandFailed));
    });

    it("blames the out-of-date service for an unknown kill-switch command", async () => {
      mocks.listProfiles.mockResolvedValue([profile]);
      mocks.setKillSwitch.mockRejectedValue(
        'unknown command "set_kill_switch"',
      );
      const toasts = captureToasts();
      renderWithProviders(<App />);
      await screen.findAllByText("EX-TEST-01");

      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: /kill-switch/i }));

      await waitFor(() => expect(toasts).toContain(en.daemon.commandUnknown));
    });
  });
});
