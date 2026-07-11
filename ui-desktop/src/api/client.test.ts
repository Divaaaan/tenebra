import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  api,
  onAttempts,
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
  SpeedTestResult,
  State,
  StunResult,
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

  it("setTlsFragment forwards the flag and returns the State", async () => {
    const armed: State = { state: "connected", tls_fragment: true };
    mockInvoke.mockResolvedValueOnce(armed);
    await expect(api.setTlsFragment(true)).resolves.toEqual(armed);
    expect(mockInvoke).toHaveBeenCalledWith("set_tls_fragment", { on: true });
  });

  it("setTun forwards the stack and returns the State", async () => {
    const swapped: State = { state: "idle", tun_stack: "gvisor" };
    mockInvoke.mockResolvedValueOnce(swapped);
    await expect(api.setTun("gvisor")).resolves.toEqual(swapped);
    expect(mockInvoke).toHaveBeenCalledWith("set_tun", { stack: "gvisor" });
  });

  it("setAutoconnect forwards the flag and returns the State", async () => {
    const armed: State = { state: "idle", autoconnect: true };
    mockInvoke.mockResolvedValueOnce(armed);
    await expect(api.setAutoconnect(true)).resolves.toEqual(armed);
    expect(mockInvoke).toHaveBeenCalledWith("set_autoconnect", { on: true });
  });

  it("setAutoFailover forwards the flag and returns the State", async () => {
    // Disarming the default-on watchdog: the core drops the field (absent = off).
    const disarmed: State = { state: "idle" };
    mockInvoke.mockResolvedValueOnce(disarmed);
    await expect(api.setAutoFailover(false)).resolves.toEqual(disarmed);
    expect(mockInvoke).toHaveBeenCalledWith("set_auto_failover", { on: false });
  });

  it("setCrashReports forwards the flag and returns the State", async () => {
    const declined: State = {
      state: "idle",
      crash_reports: false,
      crash_reports_asked: true,
    };
    mockInvoke.mockResolvedValueOnce(declined);
    await expect(api.setCrashReports(false)).resolves.toEqual(declined);
    expect(mockInvoke).toHaveBeenCalledWith("set_crash_reports", { on: false });
  });

  it("recordWebCrash forwards the message and stack with snake_case keys", async () => {
    mockInvoke.mockResolvedValueOnce(undefined);
    await api.recordWebCrash("boom", "at foo\nat bar");
    expect(mockInvoke).toHaveBeenCalledWith("record_web_crash", {
      message: "boom",
      stack_excerpt: "at foo\nat bar",
    });
  });

  it("checkCrashReport returns the report the core read back", async () => {
    const report = { text: "message: boom", signature: "12-99" };
    mockInvoke.mockResolvedValueOnce(report);
    await expect(api.checkCrashReport()).resolves.toEqual(report);
    expect(mockInvoke).toHaveBeenCalledWith("check_crash_report");
  });

  it("openReportUrl invokes the opener command with no arguments", async () => {
    mockInvoke.mockResolvedValueOnce(undefined);
    await api.openReportUrl();
    expect(mockInvoke).toHaveBeenCalledWith("open_report_url");
  });

  it("setDns forwards both toggles and resolvers with snake_case keys", async () => {
    const armed: State = {
      state: "connected",
      ad_block: true,
      dns_remote: "tls://9.9.9.9",
      dns_direct: "udp://8.8.8.8",
      ipv4_only: true,
    };
    mockInvoke.mockResolvedValueOnce(armed);
    await expect(
      api.setDns(true, "tls://9.9.9.9", "udp://8.8.8.8", true),
    ).resolves.toEqual(armed);
    // The Rust command is rename_all="snake_case", so the wire keys are snake_case.
    expect(mockInvoke).toHaveBeenCalledWith("set_dns", {
      ad_block: true,
      dns_remote: "tls://9.9.9.9",
      dns_direct: "udp://8.8.8.8",
      ipv4_only: true,
    });
  });

  it("setRules forwards the rule lists and presets with snake_case keys", async () => {
    const armed: State = {
      state: "connected",
      rules_direct: ["bank.example"],
      rules_proxy: ["work.example"],
      preset_ru_banking: true,
    };
    mockInvoke.mockResolvedValueOnce(armed);
    await expect(
      api.setRules(["bank.example"], ["work.example"], true, false),
    ).resolves.toEqual(armed);
    // rename_all="snake_case" on the Rust side, so the wire keys are snake_case.
    expect(mockInvoke).toHaveBeenCalledWith("set_rules", {
      rules_direct: ["bank.example"],
      rules_proxy: ["work.example"],
      preset_ru_banking: true,
      preset_ru_gov: false,
    });
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

  it("runStunCheck passes the STUN result through", async () => {
    const stun: StunResult = {
      udp_ok: true,
      nat_type: "endpoint-independent",
      external_ip: "203.0.113.7",
    };
    mockInvoke.mockResolvedValueOnce(stun);
    await expect(api.runStunCheck()).resolves.toEqual(stun);
    expect(mockInvoke).toHaveBeenCalledWith("run_stun_check");
  });

  it("runSpeedTest passes the throughput result through", async () => {
    const speed: SpeedTestResult = {
      mbps: 94.3,
      sample_bytes: 10485760,
      duration_ms: 890,
    };
    mockInvoke.mockResolvedValueOnce(speed);
    await expect(api.runSpeedTest()).resolves.toEqual(speed);
    expect(mockInvoke).toHaveBeenCalledWith("run_speed_test");
  });

  it("runSpeedTest rejects when the core reports no active connection", async () => {
    // The throughput probe is gated on a live tunnel; the core's rejection
    // propagates straight through the wrapper.
    mockInvoke.mockRejectedValueOnce("speed test requires an active connection");
    await expect(api.runSpeedTest()).rejects.toBe(
      "speed test requires an active connection",
    );
    expect(mockInvoke).toHaveBeenCalledWith("run_speed_test");
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

  it("onAttempts listens on the attempts channel and delivers the snapshot", async () => {
    const c = capture();
    const handler = vi.fn();
    const result = await onAttempts(handler);

    expect(c.channel).toBe("attempts");
    const payload = {
      items: [
        { seq: 1, protocol: "vless", node: "n1", status: "trying", last_good: true },
      ],
      outcome: "",
    };
    c.fire(payload);
    expect(handler).toHaveBeenCalledWith(payload);
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
