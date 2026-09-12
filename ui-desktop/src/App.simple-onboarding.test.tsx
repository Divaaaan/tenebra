import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";

import { App } from "./App";
import { makeNode, makeProfile } from "./test/fixtures";
import { renderWithProviders } from "./test/renderWithProviders";

const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  listProfiles: vi.fn(),
  importSubscription: vi.fn(),
  connect: vi.fn(),
  disconnect: vi.fn(),
  ping: vi.fn(),
  checkNodes: vi.fn(),
  checkCrashReport: vi.fn(),
  onState: vi.fn(),
  onTraffic: vi.fn(),
  onLog: vi.fn(),
  onProfilesChanged: vi.fn(),
  onAttempts: vi.fn(),
  onPickProgress: vi.fn(),
  onTrayConnect: vi.fn(),
  onTrayShow: vi.fn(),
  onDeepLink: vi.fn(),
  takeLaunchDeepLinks: vi.fn(),
}));

vi.mock("./api", () => ({
  api: {
    status: mocks.status,
    listProfiles: mocks.listProfiles,
    importSubscription: mocks.importSubscription,
    connect: mocks.connect,
    disconnect: mocks.disconnect,
    ping: mocks.ping,
    checkNodes: mocks.checkNodes,
    checkCrashReport: mocks.checkCrashReport,
  },
  onState: mocks.onState,
  onTraffic: mocks.onTraffic,
  onLog: mocks.onLog,
  onProfilesChanged: mocks.onProfilesChanged,
  onAttempts: mocks.onAttempts,
  onPickProgress: mocks.onPickProgress,
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

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("tenebra.simpleMode", "1");
  for (const listener of [
    mocks.onState,
    mocks.onTraffic,
    mocks.onLog,
    mocks.onProfilesChanged,
    mocks.onAttempts,
    mocks.onPickProgress,
    mocks.onTrayConnect,
    mocks.onTrayShow,
    mocks.onDeepLink,
  ]) {
    // Register without delivering any event, including a profiles notification.
    listener.mockResolvedValue(() => {});
  }
  mocks.takeLaunchDeepLinks.mockResolvedValue([]);
  mocks.status.mockResolvedValue({ state: "idle", crash_reports_asked: true });
  mocks.ping.mockResolvedValue([]);
  mocks.checkCrashReport.mockResolvedValue(null);
  mocks.connect.mockResolvedValue({ state: "connecting" });
  mocks.disconnect.mockResolvedValue({ state: "idle" });
});

it("retries a failed list refresh without importing the same subscription twice", async () => {
  const profile = makeProfile({ id: "saved", name: "Saved subscription", nodes: [makeNode({ name: "Saved server" })] });
  mocks.listProfiles.mockResolvedValueOnce([]).mockRejectedValueOnce(new Error("list timeout")).mockResolvedValue([profile]);
  mocks.importSubscription.mockResolvedValue(profile);
  renderWithProviders(<App />);
  const input = await screen.findByRole("textbox", { name: /subscription link/i });
  fireEvent.change(input, { target: { value: "https://example.invalid/retry" } });
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Your subscription was saved");
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  expect(await screen.findByRole("option", { name: "Saved server" })).toBeInTheDocument();
  expect(mocks.importSubscription).toHaveBeenCalledTimes(1);
  expect(mocks.listProfiles).toHaveBeenCalledTimes(3);
});

it("refreshes after the first inline import without a profile event and makes the imported server connectable", async () => {
  const url = "https://subscription.example.invalid/demo";
  const node = makeNode({ id: "imported-node", name: "Imported Amsterdam" });
  const profile = makeProfile({
    id: "imported-profile",
    name: "subscription.example.invalid",
    url,
    nodes: [node],
  });

  // Keep the real useTenebra hook. Its first list is empty; only a subsequent
  // explicit refresh can expose the stored profile. The import reply itself
  // neither changes the hook's state nor invokes an event listener.
  mocks.listProfiles.mockResolvedValueOnce([]).mockResolvedValue([profile]);
  mocks.importSubscription.mockResolvedValue(profile);
  mocks.checkNodes.mockResolvedValue({ best: node.id, results: [] });

  renderWithProviders(<App />);
  await waitFor(() => expect(mocks.onProfilesChanged).toHaveBeenCalledTimes(1));
  expect(mocks.listProfiles).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button", { name: "Connect" })).toBeNull();

  fireEvent.change(screen.getByRole("textbox", { name: /subscription link/i }), {
    target: { value: url },
  });
  fireEvent.click(screen.getByRole("button", { name: "Import" }));

  await waitFor(() => expect(mocks.listProfiles).toHaveBeenCalledTimes(2));
  expect(mocks.importSubscription).toHaveBeenCalledWith(url, profile.name);
  expect(await screen.findByRole("option", { name: node.name })).toBeInTheDocument();
  const connect = await screen.findByRole("button", { name: "Connect" });
  expect(connect).toBeEnabled();
  expect(screen.queryByRole("textbox", { name: /subscription link/i })).toBeNull();
  expect(mocks.connect).not.toHaveBeenCalled();

  // Import grants no implicit permission to connect. The existing primary
  // action must use the imported profile only after an explicit user click.
  fireEvent.click(connect);
  await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
  expect(mocks.connect.mock.calls[0].slice(0, 2)).toEqual([profile.id, node.id]);
});
