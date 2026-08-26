import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { State } from "./api";
import { App } from "./App";
import { renderWithProviders } from "./test/renderWithProviders";

// "Report a problem" is the path for the ordinary failure — the bypass stopped
// pushing video, a node went dead — where nothing crashed and so the crash
// banner (which needs consent *and* a crash file) never appears. These tests
// pin the two things that make it worth having: it is reachable with no crash
// at all, in both views; and it is inert until the user acts, because this app
// has no telemetry and the report only moves when a human moves it.

const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  listProfiles: vi.fn(),
  ping: vi.fn(),
  setAutoconnect: vi.fn(),
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
    setAutoconnect: mocks.setAutoconnect,
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

// The launch update check would otherwise reach the (absent) updater plugin.
vi.mock("./lib/updates", () => ({
  checkForUpdate: vi.fn().mockResolvedValue(null),
  inAppUpdatesSupported: vi.fn().mockResolvedValue(true),
  installUpdate: vi.fn().mockResolvedValue(undefined),
}));

const BUNDLE = {
  path: String.raw`C:\Users\me\AppData\Local\Tenebra\tenebra-diagnostics-20260824-011500.txt`,
  text: "Tenebra core diagnostics\nstate: idle\n",
};

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

/** The report entry point, wherever the current view keeps it. */
function reportButton() {
  return screen.getByRole("button", { name: /report a problem/i });
}

describe("App problem reporting", () => {
  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem("tenebra.simpleMode", "0");
    armEvents();
    mocks.status.mockResolvedValue({
      state: "idle",
      crash_reports: true,
      crash_reports_asked: true,
    } as State);
    mocks.listProfiles.mockResolvedValue([]);
    mocks.ping.mockResolvedValue([]);
    // Nothing crashed. This is the whole point: the report has to be reachable
    // anyway.
    mocks.checkCrashReport.mockResolvedValue(null);
    mocks.collectDiagnostics.mockResolvedValue(BUNDLE);
    mocks.openProblemUrl.mockResolvedValue(undefined);
  });

  it("lets the user report a problem when nothing crashed", async () => {
    const user = userEvent.setup();
    renderWithProviders(<App />);

    await waitFor(() => expect(mocks.status).toHaveBeenCalled());
    expect(
      screen.queryByText("Tenebra crashed last time"),
    ).not.toBeInTheDocument();

    await user.click(reportButton());

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Tenebra core diagnostics");
    expect(mocks.collectDiagnostics).toHaveBeenCalledTimes(1);
  });

  it("lets the user report a problem from simple mode too", async () => {
    localStorage.setItem("tenebra.simpleMode", "1");
    const user = userEvent.setup();
    renderWithProviders(<App />);

    await waitFor(() => expect(mocks.status).toHaveBeenCalled());

    await user.click(reportButton());

    expect(await screen.findByRole("dialog")).toHaveTextContent(
      "Tenebra core diagnostics",
    );
  });

  // The hard rule: no telemetry, ever. Assembling a report must not be a send —
  // the browser is only ever opened by a second, separate click. If a later
  // refactor "helpfully" opens the issue form as soon as the report is ready,
  // this fails.
  it("sends nothing while assembling the report — only an explicit click does", async () => {
    const user = userEvent.setup();
    renderWithProviders(<App />);

    await waitFor(() => expect(mocks.status).toHaveBeenCalled());
    await user.click(reportButton());
    await screen.findByRole("dialog");

    expect(mocks.collectDiagnostics).toHaveBeenCalledTimes(1);
    expect(mocks.openProblemUrl).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: /open github issue/i }));
    await waitFor(() => expect(mocks.openProblemUrl).toHaveBeenCalledTimes(1));
  });

  // Screens nested inside the settings overlay ask for the flow by event rather
  // than reaching for state they cannot see (see DiagnosticsPanel). This is the
  // other half of that handshake — the two are useless apart.
  it("opens the flow for a screen that asks by event", async () => {
    renderWithProviders(<App />);
    await waitFor(() => expect(mocks.status).toHaveBeenCalled());

    act(() => {
      window.dispatchEvent(new CustomEvent("tenebra:report-problem"));
    });

    expect(await screen.findByRole("dialog")).toHaveTextContent(
      "Tenebra core diagnostics",
    );
  });

  // A core that cannot answer is not a reason to lose the report: it is the
  // single most common thing a user wants to complain about.
  it("still produces a report when the core cannot answer", async () => {
    mocks.collectDiagnostics.mockRejectedValue(new Error("core unreachable"));
    const user = userEvent.setup();
    renderWithProviders(<App />);

    await waitFor(() => expect(mocks.status).toHaveBeenCalled());
    await user.click(reportButton());

    const dialog = await screen.findByRole("dialog");
    // What the shell knows on its own is still in there…
    expect(dialog).toHaveTextContent("Tenebra desktop diagnostics");
    // …and the gap is stated rather than papered over.
    expect(dialog).toHaveTextContent(/core unreachable/i);
  });
});
