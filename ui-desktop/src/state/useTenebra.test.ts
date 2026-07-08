import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type {
  LogEvent,
  Profile,
  State,
  StateEvent,
  TrafficEvent,
} from "../api";
import { makeProfile } from "../test/fixtures";
import { useTenebra } from "./useTenebra";

// Captured event handlers and the api stub live in a hoisted block so the
// vi.mock factory (itself hoisted) can close over them without tripping the
// "cannot access before initialization" guard. Tests fire a captured handler to
// simulate a core-pushed event.
const h = vi.hoisted(() => ({
  state: undefined as ((e: StateEvent) => void) | undefined,
  traffic: undefined as ((e: TrafficEvent) => void) | undefined,
  log: undefined as ((e: LogEvent) => void) | undefined,
  profiles: undefined as (() => void) | undefined,
}));

const mockApi = vi.hoisted(() => ({
  status: vi.fn(),
  listProfiles: vi.fn(),
  connect: vi.fn(),
  disconnect: vi.fn(),
  setRouting: vi.fn(),
  setSplit: vi.fn(),
  setKillSwitch: vi.fn(),
  setTun: vi.fn(),
}));

vi.mock("../api", async (orig) => {
  const actual = await orig<typeof import("../api")>();
  return {
    ...actual,
    api: mockApi,
    onState: vi.fn((handler) => {
      h.state = handler;
      return Promise.resolve(() => {});
    }),
    onTraffic: vi.fn((handler) => {
      h.traffic = handler;
      return Promise.resolve(() => {});
    }),
    onLog: vi.fn((handler) => {
      h.log = handler;
      return Promise.resolve(() => {});
    }),
    onProfilesChanged: vi.fn((handler) => {
      h.profiles = handler;
      return Promise.resolve(() => {});
    }),
  };
});

const idleState: State = { state: "idle" };

beforeEach(() => {
  vi.clearAllMocks();
  h.state = undefined;
  h.traffic = undefined;
  h.log = undefined;
  h.profiles = undefined;
  // Sensible defaults; individual tests override as needed.
  mockApi.status.mockResolvedValue(idleState);
  mockApi.listProfiles.mockResolvedValue([]);
});

/** Mount the hook and wait for the initial snapshot to settle. */
async function mountReady(profiles: Profile[] = []) {
  mockApi.listProfiles.mockResolvedValue(profiles);
  const view = renderHook(() => useTenebra());
  await waitFor(() => expect(view.result.current.ready).toBe(true));
  return view;
}

describe("initial load", () => {
  it("becomes ready once status and profiles resolve, reflecting the snapshot", async () => {
    const profile = makeProfile();
    mockApi.status.mockResolvedValue({ state: "connected", node: "node-1" });
    const { result } = await mountReady([profile]);

    expect(result.current.ready).toBe(true);
    expect(result.current.state).toEqual({ state: "connected", node: "node-1" });
    expect(result.current.profiles).toEqual([profile]);
    expect(mockApi.status).toHaveBeenCalledTimes(1);
    expect(mockApi.listProfiles).toHaveBeenCalledTimes(1);
  });

  it("starts idle with no profiles and empty logs", async () => {
    const { result } = await mountReady();
    expect(result.current.state).toEqual(idleState);
    expect(result.current.profiles).toEqual([]);
    expect(result.current.logs).toEqual([]);
    expect(result.current.traffic).toEqual({
      up: 0,
      down: 0,
      upRate: 0,
      downRate: 0,
    });
  });
});

describe("state events", () => {
  it("merges state/node/error from a state event", async () => {
    const { result } = await mountReady();

    act(() => h.state?.({ state: "connected", node: "node-1" }));

    expect(result.current.state.state).toBe("connected");
    expect(result.current.state.node).toBe("node-1");
    expect(result.current.state.error).toBeUndefined();
  });

  it("keeps the previous node when an event omits it", async () => {
    const { result } = await mountReady();

    act(() => h.state?.({ state: "connected", node: "node-7" }));
    // A follow-up event without a node must not clear the known node.
    act(() => h.state?.({ state: "error", error: "boom" }));

    expect(result.current.state.state).toBe("error");
    expect(result.current.state.node).toBe("node-7");
    expect(result.current.state.error).toBe("boom");
  });

  it("zeroes the live traffic counters on a clean disconnect", async () => {
    const { result } = await mountReady();

    act(() =>
      h.traffic?.({ up: 100, down: 200, up_rate: 10, down_rate: 20 }),
    );
    expect(result.current.traffic.down).toBe(200);

    act(() => h.state?.({ state: "idle" }));
    expect(result.current.traffic).toEqual({
      up: 0,
      down: 0,
      upRate: 0,
      downRate: 0,
    });
  });
});

describe("traffic events", () => {
  it("maps the wire snake_case rates onto camelCase fields", async () => {
    const { result } = await mountReady();

    act(() =>
      h.traffic?.({ up: 100, down: 200, up_rate: 10, down_rate: 20 }),
    );

    expect(result.current.traffic).toEqual({
      up: 100,
      down: 200,
      upRate: 10,
      downRate: 20,
    });
  });
});

describe("log events", () => {
  it("appends lines with incrementing ids and the event level/msg", async () => {
    const { result } = await mountReady();

    act(() => h.log?.({ level: "info", msg: "first" }));
    act(() => h.log?.({ level: "warn", msg: "second" }));

    expect(result.current.logs).toHaveLength(2);
    expect(result.current.logs[0]).toMatchObject({
      id: 0,
      level: "info",
      msg: "first",
    });
    expect(result.current.logs[1]).toMatchObject({
      id: 1,
      level: "warn",
      msg: "second",
    });
    expect(result.current.logs[0].at).toBeInstanceOf(Date);
  });

  it("clearLogs empties the buffer", async () => {
    const { result } = await mountReady();

    act(() => h.log?.({ level: "error", msg: "oops" }));
    expect(result.current.logs).toHaveLength(1);

    act(() => result.current.clearLogs());
    expect(result.current.logs).toEqual([]);
  });
});

describe("actions", () => {
  it("connect calls api.connect and applies the returned state", async () => {
    const next: State = { state: "connecting", node: "node-1", profile: "p1" };
    mockApi.connect.mockResolvedValue(next);
    const { result } = await mountReady();

    await act(async () => {
      await result.current.connect("p1", "node-1");
    });

    expect(mockApi.connect).toHaveBeenCalledWith("p1", "node-1", undefined);
    expect(result.current.state).toEqual(next);
  });

  it("connect forwards the auto flag to api.connect", async () => {
    const next: State = { state: "connecting", profile: "p1" };
    mockApi.connect.mockResolvedValue(next);
    const { result } = await mountReady();

    await act(async () => {
      await result.current.connect("p1", undefined, true);
    });

    expect(mockApi.connect).toHaveBeenCalledWith("p1", undefined, true);
    expect(result.current.state).toEqual(next);
  });

  it("disconnect calls api.disconnect and applies the returned state", async () => {
    mockApi.disconnect.mockResolvedValue(idleState);
    const { result } = await mountReady();

    // Move off idle first so the transition back is observable.
    act(() => h.state?.({ state: "connected", node: "node-1" }));
    await act(async () => {
      await result.current.disconnect();
    });

    expect(mockApi.disconnect).toHaveBeenCalledTimes(1);
    expect(result.current.state).toEqual(idleState);
  });

  it("setRouting calls api.setRouting and applies the returned state", async () => {
    const next: State = { state: "idle", routing: "global" };
    mockApi.setRouting.mockResolvedValue(next);
    const { result } = await mountReady();

    await act(async () => {
      await result.current.setRouting("global");
    });

    expect(mockApi.setRouting).toHaveBeenCalledWith("global");
    expect(result.current.state.routing).toBe("global");
  });

  it("setSplit calls api.setSplit and applies the returned state", async () => {
    const next: State = {
      state: "idle",
      split: "exclude",
      split_apps: ["chrome.exe"],
    };
    mockApi.setSplit.mockResolvedValue(next);
    const { result } = await mountReady();

    await act(async () => {
      await result.current.setSplit("exclude", ["chrome.exe"]);
    });

    expect(mockApi.setSplit).toHaveBeenCalledWith("exclude", ["chrome.exe"]);
    expect(result.current.state.split).toBe("exclude");
    expect(result.current.state.split_apps).toEqual(["chrome.exe"]);
  });

  it("setKillSwitch calls api.setKillSwitch and applies the returned state", async () => {
    const next: State = { state: "idle", kill_switch: true };
    mockApi.setKillSwitch.mockResolvedValue(next);
    const { result } = await mountReady();

    await act(async () => {
      await result.current.setKillSwitch(true);
    });

    expect(mockApi.setKillSwitch).toHaveBeenCalledWith(true);
    expect(result.current.state.kill_switch).toBe(true);
  });

  it("setTun calls api.setTun and applies the returned state", async () => {
    const next: State = { state: "idle", tun_stack: "gvisor" };
    mockApi.setTun.mockResolvedValue(next);
    const { result } = await mountReady();

    await act(async () => {
      await result.current.setTun("gvisor");
    });

    expect(mockApi.setTun).toHaveBeenCalledWith("gvisor");
    expect(result.current.state.tun_stack).toBe("gvisor");
  });
});

describe("profile reloads", () => {
  it("reloads the list when a profiles event fires", async () => {
    const listA = [makeProfile({ id: "a", name: "A" })];
    const listB = [
      makeProfile({ id: "a", name: "A" }),
      makeProfile({ id: "b", name: "B" }),
    ];
    const { result } = await mountReady(listA);
    expect(result.current.profiles).toEqual(listA);

    // The background refresh changed the stored set; the next fetch returns B.
    mockApi.listProfiles.mockResolvedValue(listB);
    await act(async () => {
      h.profiles?.();
    });

    await waitFor(() => expect(result.current.profiles).toEqual(listB));
  });

  it("refreshProfiles re-fetches and applies the list", async () => {
    const listB = [makeProfile({ id: "b", name: "B" })];
    const { result } = await mountReady([]);

    mockApi.listProfiles.mockResolvedValue(listB);
    await act(async () => {
      await result.current.refreshProfiles();
    });

    expect(result.current.profiles).toEqual(listB);
  });
});
