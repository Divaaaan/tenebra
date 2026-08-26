import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { State, StateEvent } from "./api";
import { App } from "./App";
import { renderWithProviders } from "./test/renderWithProviders";

// The one place the app speaks first. The post-connect check already knows when
// video stopped getting through — it is the same check that draws the ✕ — and
// that knowledge went nowhere: the user was left to work out on their own that
// the project has an issue tracker. Two failing runs in a row, then it offers.
//
// Driven through simple mode because that is where the checks are on screen, so
// a run landing is something the DOM can be waited on rather than guessed at.

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
  ping: vi.fn(),
  checkServices: vi.fn(),
  checkCrashReport: vi.fn(),
  collectDiagnostics: vi.fn(),
  openProblemUrl: vi.fn(),
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
    ping: mocks.ping,
    checkServices: mocks.checkServices,
    checkCrashReport: mocks.checkCrashReport,
    collectDiagnostics: mocks.collectDiagnostics,
    openProblemUrl: mocks.openProblemUrl,
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

/** Pushes state events the way the core does, once the app has subscribed. */
let pushState: (e: StateEvent) => void = () => {};

function armEvents() {
  const noopUnlisten = () => {};
  mocks.onState.mockImplementation((handler: (e: StateEvent) => void) => {
    pushState = handler;
    return Promise.resolve(noopUnlisten);
  });
  for (const on of [
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

const VIDEO_DOWN = {
  checks: [
    { service: "video", ok: false, rttMs: 0, detail: "youtube" },
    { service: "voice", ok: true, rttMs: 31, detail: "discord" },
  ],
};

const VIDEO_UP = {
  checks: [
    { service: "video", ok: true, rttMs: 44, detail: "youtube" },
    { service: "voice", ok: true, rttMs: 31, detail: "discord" },
  ],
};

/**
 * Bring the tunnel up and hold there until the post-connect check has landed on
 * screen. The verdict has to arrive while the tunnel is up — one that lands
 * after a disconnect describes a session that is already over.
 */
async function runCheck() {
  act(() => pushState({ state: "connected", node: "n1" } as StateEvent));
  await screen.findByText("Discord voice");
}

/** Drop the tunnel and wait for the checks to be cleared with it. */
async function drop() {
  act(() => pushState({ state: "idle" } as StateEvent));
  await waitFor(() => expect(screen.queryByText("Discord voice")).toBeNull());
}

/** The offer's own row, so its buttons aren't confused with the footer's. */
function prompt(): HTMLElement {
  const text = screen.getByText(/Video isn't getting through/);
  return text.closest(".report-nudge") as HTMLElement;
}

describe("App offers to report when video keeps failing", () => {
  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem("tenebra.simpleMode", "1");
    armEvents();
    mocks.status.mockResolvedValue({ state: "idle" } as State);
    mocks.listProfiles.mockResolvedValue([profile]);
    mocks.ping.mockResolvedValue([]);
    mocks.checkCrashReport.mockResolvedValue(null);
    mocks.checkServices.mockResolvedValue(VIDEO_DOWN);
    mocks.collectDiagnostics.mockResolvedValue({
      path: "/tmp/bundle.txt",
      text: "Tenebra core diagnostics\n",
    });
    mocks.openProblemUrl.mockResolvedValue(undefined);
  });

  async function mounted() {
    renderWithProviders(<App />);
    await screen.findByRole("button", { name: "Connect" });
  }

  it("keeps quiet after one failing check", async () => {
    await mounted();

    await runCheck();

    expect(
      screen.queryByText(/Video isn't getting through/),
    ).not.toBeInTheDocument();
  });

  it("offers after the second failing check in a row", async () => {
    await mounted();

    await runCheck();
    await drop();
    await runCheck();

    expect(
      await screen.findByText(/Video isn't getting through/),
    ).toBeInTheDocument();
  });

  it("says nothing when video is working", async () => {
    mocks.checkServices.mockResolvedValue(VIDEO_UP);
    await mounted();

    await runCheck();
    await drop();
    await runCheck();

    expect(
      screen.queryByText(/Video isn't getting through/),
    ).not.toBeInTheDocument();
  });

  it("hands the offer to the same report flow every other entry point uses", async () => {
    const user = userEvent.setup();
    await mounted();

    await runCheck();
    await drop();
    await runCheck();
    await screen.findByText(/Video isn't getting through/);

    await user.click(
      within(prompt()).getByRole("button", { name: "Report a problem" }),
    );

    expect(await screen.findByRole("dialog")).toHaveTextContent(
      "Tenebra core diagnostics",
    );
    // Still nothing sent: the report was assembled, not filed.
    expect(mocks.openProblemUrl).not.toHaveBeenCalled();
  });

  it("can be waved away and does not come straight back", async () => {
    const user = userEvent.setup();
    await mounted();

    await runCheck();
    await drop();
    await runCheck();
    await screen.findByText(/Video isn't getting through/);

    await user.click(within(prompt()).getByRole("button", { name: "Not now" }));
    expect(
      screen.queryByText(/Video isn't getting through/),
    ).not.toBeInTheDocument();

    await drop();
    await runCheck();
    await drop();
    await runCheck();
    expect(
      screen.queryByText(/Video isn't getting through/),
    ).not.toBeInTheDocument();
  });
});
