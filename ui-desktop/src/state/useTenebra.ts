import { useCallback, useEffect, useRef, useState } from "react";
import type { UnlistenFn } from "@tauri-apps/api/event";

import {
  api,
  onAttempts,
  onLog,
  onProfilesChanged,
  onState,
  onTraffic,
  type AttemptsEvent,
  type LogEvent,
  type LogLevel,
  type Profile,
  type RoutingMode,
  type SplitMode,
  type State,
  type TrafficEvent,
  type TunStack,
} from "../api";

export interface LogLine {
  id: number;
  at: Date;
  level: LogLevel;
  msg: string;
}

export interface Traffic {
  up: number;
  down: number;
  upRate: number;
  downRate: number;
}

const ZERO_TRAFFIC: Traffic = { up: 0, down: 0, upRate: 0, downRate: 0 };

// Keep the log panel bounded; a long-running session would otherwise grow it
// without limit.
const MAX_LOG_LINES = 500;

export interface Tenebra {
  ready: boolean;
  state: State;
  traffic: Traffic;
  profiles: Profile[];
  logs: LogLine[];
  /**
   * The latest fallback-walk snapshot (the anti-DPI attempt sequence), or null
   * until the first attempts event of the session. It is the full current
   * picture — the core re-emits it on every change — and is kept after the walk
   * settles (the UI decides when to hide it), so it is never reset to null once
   * seen.
   */
  attempts: AttemptsEvent | null;

  connect: (profile: string, node?: string, auto?: boolean) => Promise<void>;
  disconnect: () => Promise<void>;
  setRouting: (mode: RoutingMode) => Promise<void>;
  setSplit: (mode: SplitMode, apps: string[]) => Promise<void>;
  setKillSwitch: (on: boolean) => Promise<void>;
  setTlsFragment: (on: boolean) => Promise<void>;
  setMultihop: (
    profile: string,
    enabled: boolean,
    entryId: string,
    exitId: string,
  ) => Promise<void>;
  setTun: (stack: TunStack) => Promise<void>;
  setAutoconnect: (on: boolean) => Promise<void>;
  setAutoFailover: (on: boolean) => Promise<void>;
  setCrashReports: (on: boolean) => Promise<void>;
  setDns: (
    adBlock: boolean,
    dnsRemote: string,
    dnsDirect: string,
    ipv4Only: boolean,
  ) => Promise<void>;
  setRules: (
    rulesDirect: string[],
    rulesProxy: string[],
    presetRuBanking: boolean,
    presetRuGov: boolean,
  ) => Promise<void>;
  refreshProfiles: () => Promise<void>;
  clearLogs: () => void;
}

/**
 * Single source of truth for the renderer. It pulls the initial snapshot from
 * the core, then keeps itself current from the state/traffic/log event stream.
 * Screens read from here and call back into the same actions, so connection
 * state stays consistent no matter which screen triggered the change.
 */
export function useTenebra(): Tenebra {
  const [ready, setReady] = useState(false);
  const [state, setState] = useState<State>({ state: "idle" });
  const [traffic, setTraffic] = useState<Traffic>(ZERO_TRAFFIC);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [logs, setLogs] = useState<LogLine[]>([]);
  const [attempts, setAttempts] = useState<AttemptsEvent | null>(null);

  const logSeq = useRef(0);

  const appendLog = useCallback((e: LogEvent) => {
    setLogs((prev) => {
      const line: LogLine = {
        id: logSeq.current++,
        at: new Date(),
        level: e.level,
        msg: e.msg,
      };
      const next = [...prev, line];
      return next.length > MAX_LOG_LINES
        ? next.slice(next.length - MAX_LOG_LINES)
        : next;
    });
  }, []);

  const refreshProfiles = useCallback(async () => {
    setProfiles(await api.listProfiles());
  }, []);

  // Initial load and event wiring. Unlisten handles are resolved
  // asynchronously, so we guard against tearing down before they arrive.
  useEffect(() => {
    let active = true;
    const unlisteners: UnlistenFn[] = [];

    const applyTraffic = (e: TrafficEvent) => {
      setTraffic({
        up: e.up,
        down: e.down,
        upRate: e.up_rate,
        downRate: e.down_rate,
      });
    };

    (async () => {
      const [initialState, initialProfiles] = await Promise.all([
        api.status(),
        api.listProfiles(),
      ]);
      if (!active) {
        return;
      }
      setState(initialState);
      setProfiles(initialProfiles);
      setReady(true);

      const subs = await Promise.all([
        onState((e) => {
          setState((prev) => ({
            ...prev,
            state: e.state,
            node: e.node ?? prev.node,
            error: e.error,
          }));
          // A clean disconnect zeroes the live counters.
          if (e.state === "idle") {
            setTraffic(ZERO_TRAFFIC);
          }
        }),
        onTraffic(applyTraffic),
        onLog(appendLog),
        // Each attempts event is a full fallback-walk snapshot; keep the latest.
        // We never reset it to null once seen — the walk data outlives the walk
        // and the UI decides when to stop showing it.
        onAttempts((e) => setAttempts(e)),
        // A background (or another window's) refresh changed the stored
        // profiles; reload so usage and node lists stay live.
        onProfilesChanged(() => {
          void api.listProfiles().then((next) => {
            if (active) {
              setProfiles(next);
            }
          });
        }),
      ]);

      if (!active) {
        subs.forEach((u) => u());
        return;
      }
      unlisteners.push(...subs);
    })();

    return () => {
      active = false;
      unlisteners.forEach((u) => u());
    };
  }, [appendLog]);

  const connect = useCallback(
    async (profile: string, node?: string, auto?: boolean) => {
      const next = await api.connect(profile, node, auto);
      setState(next);
    },
    [],
  );

  const disconnect = useCallback(async () => {
    const next = await api.disconnect();
    setState(next);
  }, []);

  const setRouting = useCallback(async (mode: RoutingMode) => {
    const next = await api.setRouting(mode);
    setState(next);
  }, []);

  const setSplit = useCallback(async (mode: SplitMode, apps: string[]) => {
    const next = await api.setSplit(mode, apps);
    setState(next);
  }, []);

  const setKillSwitch = useCallback(async (on: boolean) => {
    const next = await api.setKillSwitch(on);
    setState(next);
  }, []);

  const setTlsFragment = useCallback(async (on: boolean) => {
    const next = await api.setTlsFragment(on);
    setState(next);
  }, []);

  const setMultihop = useCallback(
    async (profile: string, enabled: boolean, entryId: string, exitId: string) => {
      const next = await api.setMultihop(profile, enabled, entryId, exitId);
      setState(next);
    },
    [],
  );

  const setTun = useCallback(async (stack: TunStack) => {
    const next = await api.setTun(stack);
    setState(next);
  }, []);

  const setAutoconnect = useCallback(async (on: boolean) => {
    const next = await api.setAutoconnect(on);
    setState(next);
  }, []);

  const setAutoFailover = useCallback(async (on: boolean) => {
    const next = await api.setAutoFailover(on);
    setState(next);
  }, []);

  const setCrashReports = useCallback(async (on: boolean) => {
    const next = await api.setCrashReports(on);
    setState(next);
  }, []);

  const setDns = useCallback(
    async (
      adBlock: boolean,
      dnsRemote: string,
      dnsDirect: string,
      ipv4Only: boolean,
    ) => {
      const next = await api.setDns(adBlock, dnsRemote, dnsDirect, ipv4Only);
      setState(next);
    },
    [],
  );

  const setRules = useCallback(
    async (
      rulesDirect: string[],
      rulesProxy: string[],
      presetRuBanking: boolean,
      presetRuGov: boolean,
    ) => {
      const next = await api.setRules(
        rulesDirect,
        rulesProxy,
        presetRuBanking,
        presetRuGov,
      );
      setState(next);
    },
    [],
  );

  const clearLogs = useCallback(() => setLogs([]), []);

  return {
    ready,
    state,
    traffic,
    profiles,
    logs,
    attempts,
    connect,
    disconnect,
    setRouting,
    setSplit,
    setKillSwitch,
    setTlsFragment,
    setMultihop,
    setTun,
    setAutoconnect,
    setAutoFailover,
    setCrashReports,
    setDns,
    setRules,
    refreshProfiles,
    clearLogs,
  };
}
