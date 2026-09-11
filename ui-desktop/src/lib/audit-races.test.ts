import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import type { ConnectionState, State, StateEvent, ServiceCheck } from "../api";
import { useTenebra } from "../state/useTenebra";
import { useUpdateCheck } from "./useUpdateCheck";
import { useServiceChecks } from "./useServiceChecks";
import { setAutoInstallUpdates } from "./settings";

const m = vi.hoisted(() => ({
  state: undefined as ((e: StateEvent) => void) | undefined,
  status: vi.fn(), listProfiles: vi.fn(), checkServices: vi.fn(),
  checkForUpdate: vi.fn(), installUpdate: vi.fn(),
}));
vi.mock("../api", () => ({
  api: { status: m.status, listProfiles: m.listProfiles, checkServices: m.checkServices },
  onState: vi.fn((handler) => { m.state = handler; return Promise.resolve(() => {}); }),
  onTraffic: vi.fn(() => Promise.resolve(() => {})),
  onLog: vi.fn(() => Promise.resolve(() => {})),
  onAttempts: vi.fn(() => Promise.resolve(() => {})),
  onPickProgress: vi.fn(() => Promise.resolve(() => {})),
  onProfilesChanged: vi.fn(() => Promise.resolve(() => {})),
}));
vi.mock("./updates", () => ({
  checkForUpdate: m.checkForUpdate, installUpdate: m.installUpdate,
  inAppUpdatesSupported: vi.fn(async () => true),
  notifyUpdateAvailable: vi.fn(async () => {}),
}));
beforeEach(() => {
  localStorage.clear();
  m.state = undefined;
  m.listProfiles.mockResolvedValue([]);
  m.status.mockResolvedValue({ state: "idle" });
  m.checkForUpdate.mockResolvedValue({ version: "9.9.9" });
  m.installUpdate.mockImplementation(() => new Promise<void>(() => {}));
});

it("merges bootstrap metadata without rolling back the latest event phase", async () => {
  let resolveStatus!: (s: State) => void;
  m.status.mockImplementation(() => new Promise((resolve) => { resolveStatus = resolve; }));
  const { result } = renderHook(() => useTenebra());
  await waitFor(() => expect(m.state).toBeTypeOf("function"));
  act(() => m.state!({ state: "connected", node: "new-node" }));
  await act(async () => resolveStatus({ state: "connecting", node: "old-node", profile: "p1", kill_switch: true, routing: "global", daemon_version: "0.5.11" }));
  await waitFor(() => expect(result.current.ready).toBe(true));
  expect(result.current.state).toMatchObject({ state: "connected", node: "new-node", profile: "p1", kill_switch: true, routing: "global", daemon_version: "0.5.11" });
});

it("holds automatic and manual installation until daemon status is ready", async () => {
  setAutoInstallUpdates(true);
  let resolveStatus!: (s: State) => void;
  m.status.mockImplementation(() => new Promise((resolve) => { resolveStatus = resolve; }));
  const { result } = renderHook(() => {
    const tenebra = useTenebra();
    const update = useUpdateCheck(tenebra.state.state, tenebra.ready);
    return { tenebra, update };
  });
  await waitFor(() => expect(result.current.update.available).toBe("9.9.9"));
  act(() => result.current.update.install());
  expect(m.installUpdate).not.toHaveBeenCalled();
  await act(async () => resolveStatus({ state: "connected" }));
  expect(m.installUpdate).not.toHaveBeenCalled();
  act(() => m.state!({ state: "idle" }));
  await waitFor(() => expect(m.installUpdate).toHaveBeenCalledTimes(1));
});

it("disarms a deferred install immediately when the current preference is OFF", async () => {
  setAutoInstallUpdates(true);
  const { result, rerender } = renderHook(({ phase }) => useUpdateCheck(phase, true), { initialProps: { phase: "connected" as ConnectionState } });
  await waitFor(() => expect(result.current.deferred).toBe(true));
  act(() => setAutoInstallUpdates(false));
  expect(result.current.deferred).toBe(false);
  rerender({ phase: "idle" });
  expect(m.installUpdate).not.toHaveBeenCalled();
});

it("ignores a service verdict that returns after disconnect and checks the new session", async () => {
  let finish!: (r: {checks: ServiceCheck[]}) => void;
  m.checkServices.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  const { result, rerender } = renderHook(({ phase }) => useServiceChecks(phase), { initialProps: { phase: "connected" as ConnectionState } });
  await waitFor(() => expect(m.checkServices).toHaveBeenCalledTimes(1));
  rerender({ phase: "idle" });
  await act(async () => finish({ checks: [{ service: "youtube", ok: true } as ServiceCheck] }));
  expect(result.current.checks).toEqual([]);
  expect(result.current.checking).toBe(false);
  expect(result.current.runs).toBe(0);
  m.checkServices.mockResolvedValue({ checks: [] });
  rerender({ phase: "connected" });
  await waitFor(() => expect(result.current.runs).toBe(1));
});
