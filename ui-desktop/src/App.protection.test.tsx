import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import type { State } from "./api";
import { App } from "./App";
import { makeTenebra } from "./test/fixtures";
import { renderWithProviders } from "./test/renderWithProviders";

const m = vi.hoisted(() => ({
  state: { state: "connected", kill_switch: true } as State,
  coreError: null as string | null,
  setKillSwitch: vi.fn(), disconnect: vi.fn(),
  pings: new Map(),
}));
vi.mock("./state/useTenebra", () => ({ useTenebra: () => makeTenebra({ state: m.state, coreError: m.coreError, setKillSwitch: m.setKillSwitch, disconnect: m.disconnect }) }));
vi.mock("./api", () => ({
  api: { checkServices: vi.fn(async () => ({checks:[]})) },
  onTrayConnect: vi.fn(async () => () => {}), onTrayShow: vi.fn(async () => () => {}),
  onDeepLink: vi.fn(async () => () => {}), takeLaunchDeepLinks: vi.fn(async () => []),
}));
vi.mock("./lib/useNodePings", () => ({ useNodePings: () => ({ results: m.pings, pinging: false }) }));
vi.mock("./lib/useUpdateCheck", () => ({ useUpdateCheck: () => ({ available: null, stalled: false, confirming: false }) }));
beforeEach(() => {
  localStorage.clear();
  m.coreError = null;
  m.setKillSwitch.mockResolvedValue(undefined);
  m.disconnect.mockResolvedValue(undefined);
});

it("does not promise persistent protection from an old daemon's preference alone", async () => {
  m.state = { state: "connected", kill_switch: true };
  renderWithProviders(<App />);
  await act(async () => {});
  expect(screen.queryByText(/traffic blocked if the tunnel drops/i)).toBeNull();
  expect(screen.getByText(/does not confirm persistent protection/i)).toBeInTheDocument();
  expect(document.querySelector('[data-protection="active"]')).toBeNull();
});

it("shows blocked traffic and lets the user explicitly disconnect to release it in simple mode", async () => {
  localStorage.setItem("tenebra.simpleMode", "1");
  m.state = { state: "error", kill_switch: true, protection: { status: "blocked", enforced: true, persistent: true } };
  renderWithProviders(<App />);
  expect(screen.getByText(/Internet traffic is blocked/i)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: /disconnect and allow traffic/i }));
  await waitFor(() => expect(m.disconnect).toHaveBeenCalledTimes(1));
});

it("retries a failed guard or turns it off only after an explicit action", async () => {
  m.state = { state: "error", kill_switch: true, protection: { status: "error", enforced: true, persistent: true, error: "WFP remove failed: access denied" } };
  renderWithProviders(<App />);
  const banner = screen.getByRole("alert");
  expect(banner).toHaveTextContent("WFP remove failed: access denied");
  expect(m.setKillSwitch).not.toHaveBeenCalled();
  fireEvent.click(within(banner).getByRole("button", { name: /retry protection/i }));
  await waitFor(() => expect(m.setKillSwitch).toHaveBeenCalledWith(true));
  fireEvent.click(within(banner).getByRole("button", { name: /turn protection off/i }));
  await waitFor(() => expect(m.setKillSwitch).toHaveBeenCalledWith(false));
});

it("stops showing active protection when the service connection is lost", async () => {
  m.state = { state: "connected", kill_switch: true, protection: { status: "active", enforced: true, persistent: true } };
  const { rerender } = renderWithProviders(<App />);
  expect(document.querySelector('[data-protection="active"]')).toBeInTheDocument();
  m.coreError = "Lost the connection to the Tenebra service";
  m.state = { ...m.state, state: "connecting" };
  rerender(<App />);
  expect(document.querySelector('[data-protection="active"]')).toBeNull();
  expect(screen.getByText(/last confirmed guard/i)).toBeInTheDocument();
  await act(async () => {});
});

it.each(["off", "applying", "unavailable"] as const)("never claims active protection from status %s", async (status) => {
  m.state = { state: "connected", kill_switch: true, protection: { status, enforced: false, persistent: false } };
  renderWithProviders(<App />);
  expect(document.querySelector(`[data-protection="${status}"]`)).toBeInTheDocument();
  expect(document.querySelector('[data-protection="active"]')).toBeNull();
  expect(m.setKillSwitch).not.toHaveBeenCalled();
  await act(async () => {});
});
