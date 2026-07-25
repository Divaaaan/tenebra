import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  AttemptsEvent,
  LogEvent,
  Profile,
  State,
  StateEvent,
  TrafficEvent,
} from "../api";
import { makeProfile } from "../test/fixtures";
import {
  BOOTSTRAP_RETRY_MAX_MS,
  BOOTSTRAP_RETRY_MS,
  useTenebra,
} from "./useTenebra";

// Captured event handlers and the api stub live in a hoisted block so the
// vi.mock factory (itself hoisted) can close over them without tripping the
// "cannot access before initialization" guard. Tests fire a captured handler to
// simulate a core-pushed event.
const h = vi.hoisted(() => ({
  state: undefined as ((e: StateEvent) => void) | undefined,
  traffic: undefined as ((e: TrafficEvent) => void) | undefined,
  log: undefined as ((e: LogEvent) => void) | undefined,
  profiles: undefined as (() => void) | undefined,
  attempts: undefined as ((e: AttemptsEvent) => void) | undefined,
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
  setAutoconnect: vi.fn(),
  setRules: vi.fn(),
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
    onAttempts: vi.fn((handler) => {
      h.attempts = handler;
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
  h.attempts = undefined;
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

// The core is not always up when the window is: the privileged service can be
// starting, restarting after an update, or briefly unreachable over the pipe.
// A single unguarded snapshot request left the app drawn but permanently mute —
// no ready, no profiles, no event subscriptions, no way back short of a
// restart. These cover the way out of that.
describe("bootstrap resilience", () => {
  const DOWN = "the connection to tenebra-core is closed";

  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  /** Let the pending microtasks (the in-flight snapshot) settle. */
  async function settle(ms = 0) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  }

  it("retries the initial snapshot until the core answers", async () => {
    const profile = makeProfile();
    mockApi.status.mockRejectedValueOnce(DOWN).mockResolvedValue(idleState);
    mockApi.listProfiles.mockRejectedValueOnce(DOWN).mockResolvedValue([profile]);

    const { result } = renderHook(() => useTenebra());

    // First attempt failed: nothing from the core, so nothing is ready yet.
    await settle();
    expect(result.current.ready).toBe(false);
    expect(result.current.profiles).toEqual([]);

    // The backoff elapses and the retry lands — the app comes fully alive.
    await settle(BOOTSTRAP_RETRY_MS);
    expect(result.current.ready).toBe(true);
    expect(result.current.profiles).toEqual([profile]);
    expect(result.current.state).toEqual(idleState);
  });

  it("surfaces the failure as coreError and clears it once the core answers", async () => {
    mockApi.status.mockRejectedValueOnce(DOWN);
    mockApi.listProfiles.mockRejectedValueOnce(DOWN);

    const { result } = renderHook(() => useTenebra());

    await settle();
    // A named failure the shell can render — silence is what made the bug
    // undiagnosable from the outside.
    expect(result.current.coreError).toContain("tenebra-core");

    await settle(BOOTSTRAP_RETRY_MS);
    expect(result.current.coreError).toBeNull();
    expect(result.current.ready).toBe(true);
  });

  it("keeps the event stream wired when the first snapshot fails", async () => {
    mockApi.status.mockRejectedValue(DOWN);
    mockApi.listProfiles.mockRejectedValue(DOWN);

    const { result } = renderHook(() => useTenebra());
    await settle();

    // Subscribing is a webview-side registration, not a core round-trip, so a
    // failing snapshot must not cost us the stream: whatever the core pushes
    // once it is up has to land in a live UI.
    expect(h.state).toBeDefined();
    expect(h.log).toBeDefined();

    act(() => h.state?.({ state: "connected", node: "node-1" }));
    expect(result.current.state.state).toBe("connected");

    act(() => h.log?.({ level: "info", msg: "core says hello" }));
    // The failed attempt logs a line of its own first, so assert on the tail.
    const logs = result.current.logs;
    expect(logs[logs.length - 1].msg).toBe("core says hello");
  });

  it("does not roll a pushed state back to a late snapshot", async () => {
    mockApi.status.mockRejectedValueOnce(DOWN).mockResolvedValue(idleState);
    mockApi.listProfiles.mockRejectedValueOnce(DOWN).mockResolvedValue([]);

    const { result } = renderHook(() => useTenebra());
    await settle();

    // The core came up and announced itself before the retried status returned.
    act(() => h.state?.({ state: "connected", node: "node-1" }));
    await settle(BOOTSTRAP_RETRY_MS);

    expect(result.current.ready).toBe(true);
    expect(result.current.state.state).toBe("connected");
  });

  it("backs off exponentially up to the ceiling", async () => {
    mockApi.status.mockRejectedValue(DOWN);
    mockApi.listProfiles.mockRejectedValue(DOWN);

    renderHook(() => useTenebra());
    await settle();
    expect(mockApi.status).toHaveBeenCalledTimes(1);

    // 500 → 1000 → 2000 → 4000 → capped at 5000 from there on.
    for (const delay of [500, 1000, 2000, 4000, 5000, 5000]) {
      const before = mockApi.status.mock.calls.length;
      await settle(delay - 1);
      expect(mockApi.status.mock.calls.length).toBe(before);
      await settle(1);
      expect(mockApi.status.mock.calls.length).toBe(before + 1);
    }
  });

  it("abandons a snapshot that fails after the hook is gone", async () => {
    let rejectStatus: (e: unknown) => void = () => {};
    mockApi.status.mockReturnValue(
      new Promise((_, reject) => {
        rejectStatus = reject;
      }),
    );
    mockApi.listProfiles.mockResolvedValue([]);

    const { unmount } = renderHook(() => useTenebra());
    unmount();

    // The request only fails once the hook is torn down: nothing may be written
    // behind it and no retry may be armed, or the loop would outlive the window.
    rejectStatus(DOWN);
    await settle();

    expect(vi.getTimerCount()).toBe(0);
    expect(mockApi.status).toHaveBeenCalledTimes(1);
  });

  it("stops retrying on unmount, leaving no timer and no late setState", async () => {
    mockApi.status.mockRejectedValue(DOWN);
    mockApi.listProfiles.mockRejectedValue(DOWN);

    const { unmount } = renderHook(() => useTenebra());
    await settle();
    const attempts = mockApi.status.mock.calls.length;

    unmount();
    await settle(BOOTSTRAP_RETRY_MAX_MS * 4);

    expect(mockApi.status.mock.calls.length).toBe(attempts);
    expect(vi.getTimerCount()).toBe(0);
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

describe("attempts events", () => {
  const walking: AttemptsEvent = {
    items: [
      { seq: 1, protocol: "vless", node: "n1", status: "trying", last_good: true },
      { seq: 2, protocol: "hysteria2", node: "n2", status: "waiting", last_good: false },
    ],
    outcome: "",
  };
  const settled: AttemptsEvent = {
    items: [
      { seq: 1, protocol: "vless", node: "n1", status: "blocked", last_good: true },
      { seq: 2, protocol: "hysteria2", node: "n2", status: "ok", last_good: false },
    ],
    outcome: "ok",
  };

  it("starts null and stores the latest snapshot from each event", async () => {
    const { result } = await mountReady();
    expect(result.current.attempts).toBeNull();

    act(() => h.attempts?.(walking));
    expect(result.current.attempts).toEqual(walking);

    // A later event fully replaces the snapshot (the core sends the whole walk).
    act(() => h.attempts?.(settled));
    expect(result.current.attempts).toEqual(settled);
  });

  it("keeps the last snapshot after the connection ends (no reset to null)", async () => {
    const { result } = await mountReady();

    act(() => h.attempts?.(settled));
    // A disconnect zeroes traffic but must not wipe the walk snapshot — the UI
    // owns when to stop showing it.
    act(() => h.state?.({ state: "idle" }));

    expect(result.current.attempts).toEqual(settled);
  });

  it("preserves the adaptive strategy and reason annotations on items", async () => {
    const { result } = await mountReady();
    // A walk that escalated a censored node's transport strategy: the item that
    // came up names the non-default strategy, the abandoned one the censored
    // reason. Both must survive verbatim so a future UI can surface them.
    const adapted: AttemptsEvent = {
      items: [
        { seq: 1, protocol: "vless", node: "n1", status: "blocked", last_good: true, reason: "censored" },
        { seq: 2, protocol: "vless", node: "n2", status: "ok", last_good: false, strategy: "firefox-fp" },
      ],
      outcome: "ok",
    };

    act(() => h.attempts?.(adapted));

    expect(result.current.attempts).toEqual(adapted);
    expect(result.current.attempts?.items[0].reason).toBe("censored");
    expect(result.current.attempts?.items[1].strategy).toBe("firefox-fp");
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

  it("setAutoconnect calls api.setAutoconnect and applies the returned state", async () => {
    const next: State = { state: "idle", autoconnect: true };
    mockApi.setAutoconnect.mockResolvedValue(next);
    const { result } = await mountReady();

    await act(async () => {
      await result.current.setAutoconnect(true);
    });

    expect(mockApi.setAutoconnect).toHaveBeenCalledWith(true);
    expect(result.current.state.autoconnect).toBe(true);
  });

  it("setRules calls api.setRules and applies the returned state", async () => {
    const next: State = {
      state: "idle",
      rules_direct: ["bank.example"],
      rules_proxy: ["work.example"],
      preset_ru_banking: true,
    };
    mockApi.setRules.mockResolvedValue(next);
    const { result } = await mountReady();

    await act(async () => {
      await result.current.setRules(
        ["bank.example"],
        ["work.example"],
        true,
        false,
      );
    });

    expect(mockApi.setRules).toHaveBeenCalledWith(
      ["bank.example"],
      ["work.example"],
      true,
      false,
    );
    expect(result.current.state.rules_direct).toEqual(["bank.example"]);
    expect(result.current.state.preset_ru_banking).toBe(true);
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
