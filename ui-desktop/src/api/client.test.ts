import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  api,
  onDeepLink,
  onLog,
  onProfilesChanged,
  onState,
  onTraffic,
  onTrayConnect,
  onTrayShow,
  takeLaunchDeepLinks,
} from "./client";
import type {
  DeepLinkAction,
  LeakCheck,
  PingResult,
  Profile,
  State,
} from "./index";

vi.mock("@tauri-apps/api/core", () => ({ invoke: vi.fn() }));
vi.mock("@tauri-apps/api/event", () => ({ listen: vi.fn() }));

import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";

const mockInvoke = vi.mocked(invoke);
const mockListen = vi.mocked(listen);

beforeEach(() => {
  vi.clearAllMocks();
});

const sampleState: State = { state: "connected", node: "node-1", profile: "p1" };

const sampleProfile: Profile = {
  id: "p1",
  name: "Demo",
  source: "subscription",
  nodes: [],
  updatedAt: "2026-01-01T00:00:00Z",
};

describe("api command wrappers", () => {
  it("status passes the State through untouched", async () => {
    mockInvoke.mockResolvedValueOnce(sampleState);
    await expect(api.status()).resolves.toEqual(sampleState);
    expect(mockInvoke).toHaveBeenCalledWith("status");
  });

  it("listProfiles unwraps the profiles array from the envelope", async () => {
    mockInvoke.mockResolvedValueOnce({ profiles: [sampleProfile] });
    await expect(api.listProfiles()).resolves.toEqual([sampleProfile]);
    expect(mockInvoke).toHaveBeenCalledWith("list_profiles");
  });

  it("importSubscription forwards url+name and unwraps the profile", async () => {
    mockInvoke.mockResolvedValueOnce({ profile: sampleProfile });
    await expect(
      api.importSubscription("https://example.invalid/sub", "Demo"),
    ).resolves.toEqual(sampleProfile);
    expect(mockInvoke).toHaveBeenCalledWith("import_subscription", {
      url: "https://example.invalid/sub",
      name: "Demo",
    });
  });

  it("importLink forwards link+name and unwraps the profile", async () => {
    mockInvoke.mockResolvedValueOnce({ profile: sampleProfile });
    await expect(api.importLink("vless://x", "Manual")).resolves.toEqual(
      sampleProfile,
    );
    expect(mockInvoke).toHaveBeenCalledWith("import_link", {
      link: "vless://x",
      name: "Manual",
    });
  });

  it("importLink passes an undefined name when omitted", async () => {
    mockInvoke.mockResolvedValueOnce({ profile: sampleProfile });
    await api.importLink("vless://x");
    expect(mockInvoke).toHaveBeenCalledWith("import_link", {
      link: "vless://x",
      name: undefined,
    });
  });

  it("removeProfile forwards the profile id", async () => {
    mockInvoke.mockResolvedValueOnce(undefined);
    await api.removeProfile("p1");
    expect(mockInvoke).toHaveBeenCalledWith("remove_profile", { profile: "p1" });
  });

  it("refreshSubscription forwards the id and unwraps the profile", async () => {
    mockInvoke.mockResolvedValueOnce({ profile: sampleProfile });
    await expect(api.refreshSubscription("p1")).resolves.toEqual(sampleProfile);
    expect(mockInvoke).toHaveBeenCalledWith("refresh_subscription", {
      profile: "p1",
    });
  });

  it("connect forwards profile+node and returns the State", async () => {
    mockInvoke.mockResolvedValueOnce(sampleState);
    await expect(api.connect("p1", "node-1")).resolves.toEqual(sampleState);
    expect(mockInvoke).toHaveBeenCalledWith("connect", {
      profile: "p1",
      node: "node-1",
      auto: undefined,
    });
  });

  it("connect passes undefined node/auto for the default selection", async () => {
    mockInvoke.mockResolvedValueOnce(sampleState);
    await api.connect("p1");
    expect(mockInvoke).toHaveBeenCalledWith("connect", {
      profile: "p1",
      node: undefined,
      auto: undefined,
    });
  });

  it("connect forwards the auto flag for fastest-node selection", async () => {
    mockInvoke.mockResolvedValueOnce(sampleState);
    await api.connect("p1", undefined, true);
    expect(mockInvoke).toHaveBeenCalledWith("connect", {
      profile: "p1",
      node: undefined,
      auto: true,
    });
  });

  it("disconnect returns the State", async () => {
    const idle: State = { state: "idle" };
    mockInvoke.mockResolvedValueOnce(idle);
    await expect(api.disconnect()).resolves.toEqual(idle);
    expect(mockInvoke).toHaveBeenCalledWith("disconnect");
  });

  it("ping unwraps the results array", async () => {
    const results: PingResult[] = [{ node: "node-1", rttMs: 42, ok: true }];
    mockInvoke.mockResolvedValueOnce({ results });
    await expect(api.ping("p1")).resolves.toEqual(results);
    expect(mockInvoke).toHaveBeenCalledWith("ping", { profile: "p1" });
  });

  it("setRouting forwards the mode and returns the State", async () => {
    mockInvoke.mockResolvedValueOnce(sampleState);
    await expect(api.setRouting("global")).resolves.toEqual(sampleState);
    expect(mockInvoke).toHaveBeenCalledWith("set_routing", { mode: "global" });
  });

  it("setSplit forwards the mode+apps and returns the State", async () => {
    mockInvoke.mockResolvedValueOnce(sampleState);
    await api.setSplit("exclude", ["chrome.exe", "steam.exe"]);
    expect(mockInvoke).toHaveBeenCalledWith("set_split", {
      mode: "exclude",
      apps: ["chrome.exe", "steam.exe"],
    });
  });

  it("setKillSwitch forwards the flag and returns the State", async () => {
    const armed: State = { state: "connected", kill_switch: true };
    mockInvoke.mockResolvedValueOnce(armed);
    await expect(api.setKillSwitch(true)).resolves.toEqual(armed);
    expect(mockInvoke).toHaveBeenCalledWith("set_kill_switch", { on: true });
  });

  it("setTun forwards the stack and returns the State", async () => {
    const swapped: State = { state: "idle", tun_stack: "gvisor" };
    mockInvoke.mockResolvedValueOnce(swapped);
    await expect(api.setTun("gvisor")).resolves.toEqual(swapped);
    expect(mockInvoke).toHaveBeenCalledWith("set_tun", { stack: "gvisor" });
  });

  it("leakCheck passes the LeakCheck verdict through", async () => {
    const leak: LeakCheck = {
      connected: false,
      ip_verdict: "neutral",
      ip_message: "Not connected.",
      dns: { status: "inconclusive", message: "n/a" },
    };
    mockInvoke.mockResolvedValueOnce(leak);
    await expect(api.leakCheck()).resolves.toEqual(leak);
    expect(mockInvoke).toHaveBeenCalledWith("leak_check");
  });

  it("quit invokes the explicit exit command", async () => {
    mockInvoke.mockResolvedValueOnce(undefined);
    await api.quit();
    expect(mockInvoke).toHaveBeenCalledWith("quit_app");
  });

  it("takeLaunchDeepLinks drains the launch queue via the command", async () => {
    const actions: DeepLinkAction[] = [
      { action: "import", url: "https://example.invalid/sub" },
      { action: "connect", profile: "p1" },
    ];
    mockInvoke.mockResolvedValueOnce(actions);
    await expect(takeLaunchDeepLinks()).resolves.toEqual(actions);
    expect(mockInvoke).toHaveBeenCalledWith("take_launch_deep_links");
  });
});

describe("event subscriptions", () => {
  // Capture (channel, callback) so each test can fire the callback the helper
  // registered and check the handler sees the right payload.
  function capture() {
    let channel = "";
    let cb: ((event: { payload: unknown }) => void) | undefined;
    const unlisten = vi.fn();
    mockListen.mockImplementationOnce((ch, fn) => {
      channel = ch;
      cb = fn as typeof cb;
      return Promise.resolve(unlisten);
    });
    return {
      get channel() {
        return channel;
      },
      fire(payload: unknown) {
        cb?.({ payload });
      },
      unlisten,
    };
  }

  it("onState listens on the state channel and delivers the payload", async () => {
    const c = capture();
    const handler = vi.fn();
    const result = await onState(handler);

    expect(c.channel).toBe("state");
    const payload: State = { state: "connecting" };
    c.fire(payload);
    expect(handler).toHaveBeenCalledWith(payload);
    expect(result).toBe(c.unlisten);
  });

  it("onTraffic listens on the traffic channel and delivers the payload", async () => {
    const c = capture();
    const handler = vi.fn();
    const result = await onTraffic(handler);

    expect(c.channel).toBe("traffic");
    const payload = { up: 1, down: 2, up_rate: 3, down_rate: 4 };
    c.fire(payload);
    expect(handler).toHaveBeenCalledWith(payload);
    expect(result).toBe(c.unlisten);
  });

  it("onLog listens on the log channel and delivers the payload", async () => {
    const c = capture();
    const handler = vi.fn();
    const result = await onLog(handler);

    expect(c.channel).toBe("log");
    const payload = { level: "warn", msg: "heads up" };
    c.fire(payload);
    expect(handler).toHaveBeenCalledWith(payload);
    expect(result).toBe(c.unlisten);
  });

  it("onProfilesChanged listens on the profiles channel and fires (payload-less)", async () => {
    const c = capture();
    const handler = vi.fn();
    const result = await onProfilesChanged(handler);

    expect(c.channel).toBe("profiles");
    c.fire(undefined);
    expect(handler).toHaveBeenCalledTimes(1);
    expect(result).toBe(c.unlisten);
  });

  it("onTrayConnect listens on tray://connect and fires (payload-less)", async () => {
    const c = capture();
    const handler = vi.fn();
    const result = await onTrayConnect(handler);

    expect(c.channel).toBe("tray://connect");
    c.fire(undefined);
    expect(handler).toHaveBeenCalledTimes(1);
    expect(result).toBe(c.unlisten);
  });

  it("onTrayShow listens on tray://show and fires (payload-less)", async () => {
    const c = capture();
    const handler = vi.fn();
    const result = await onTrayShow(handler);

    expect(c.channel).toBe("tray://show");
    c.fire(undefined);
    expect(handler).toHaveBeenCalledTimes(1);
    expect(result).toBe(c.unlisten);
  });

  it("onDeepLink listens on deep-link://action and delivers the action", async () => {
    const c = capture();
    const handler = vi.fn();
    const result = await onDeepLink(handler);

    expect(c.channel).toBe("deep-link://action");
    const payload: DeepLinkAction = {
      action: "connect",
      profile: "demo-sub",
    };
    c.fire(payload);
    expect(handler).toHaveBeenCalledWith(payload);
    expect(result).toBe(c.unlisten);
  });
});
