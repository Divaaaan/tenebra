import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, fireEvent, waitFor } from "@testing-library/react";

import type { State } from "./api";
import { App } from "./App";
import { renderWithProviders } from "./test/renderWithProviders";

// Exercises the App's crash-reporting wiring end to end against a stubbed api:
// the first-run consent banner shows only until the choice is made and routes to
// set_crash_reports; and, once consented, a crash file surfaced by
// check_crash_report drives the "crashed last time" banner and its issue action.

const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  listProfiles: vi.fn(),
  ping: vi.fn(),
  setAutoconnect: vi.fn(),
  setCrashReports: vi.fn(),
  checkCrashReport: vi.fn(),
  openReportUrl: vi.fn(),
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
    setCrashReports: mocks.setCrashReports,
    checkCrashReport: mocks.checkCrashReport,
    openReportUrl: mocks.openReportUrl,
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

describe("App crash-reporting wiring", () => {
  beforeEach(() => {
    localStorage.clear();
    armEvents();
    mocks.listProfiles.mockResolvedValue([]);
    mocks.ping.mockResolvedValue([]);
    mocks.checkCrashReport.mockResolvedValue(null);
    mocks.setCrashReports.mockResolvedValue({
      state: "idle",
      crash_reports: true,
      crash_reports_asked: true,
    } as State);
    mocks.openReportUrl.mockResolvedValue(undefined);
  });

  it("offers first-run consent when the user hasn't been asked, and enabling calls the core", async () => {
    mocks.status.mockResolvedValue({ state: "idle" } as State);

    renderWithProviders(<App />);

    const banner = await screen.findByText(/Nothing is ever sent automatically/);
    expect(banner).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Enable" }));
    await waitFor(() =>
      expect(mocks.setCrashReports).toHaveBeenCalledWith(true),
    );
  });

  it("does not show the consent banner once the choice has been made", async () => {
    mocks.status.mockResolvedValue({
      state: "idle",
      crash_reports: false,
      crash_reports_asked: true,
    } as State);

    renderWithProviders(<App />);
    // Let the initial status settle.
    await waitFor(() => expect(mocks.status).toHaveBeenCalled());
    expect(
      screen.queryByText(/Nothing is ever sent automatically/),
    ).not.toBeInTheDocument();
  });

  it("surfaces a saved crash report when consented, and the issue action opens the URL", async () => {
    mocks.status.mockResolvedValue({
      state: "idle",
      crash_reports: true,
      crash_reports_asked: true,
    } as State);
    mocks.checkCrashReport.mockResolvedValue({
      text: "--- tenebra gui crash ---\nmessage: boom",
      signature: "sig-1",
    });

    renderWithProviders(<App />);

    expect(
      await screen.findByText("Tenebra crashed last time"),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Create GitHub issue" }));
    await waitFor(() => expect(mocks.openReportUrl).toHaveBeenCalledTimes(1));
  });

  it("does not surface a crash report when consent is off", async () => {
    mocks.status.mockResolvedValue({
      state: "idle",
      crash_reports: false,
      crash_reports_asked: true,
    } as State);
    mocks.checkCrashReport.mockResolvedValue({
      text: "message: boom",
      signature: "sig-2",
    });

    renderWithProviders(<App />);
    await waitFor(() => expect(mocks.status).toHaveBeenCalled());
    expect(
      screen.queryByText("Tenebra crashed last time"),
    ).not.toBeInTheDocument();
  });
});
