import { useCallback, useEffect, useRef, useState } from "react";

import { api, type PingResult } from "../api";

export type NodePingPhase = "idle" | "checking" | "ready" | "failed";

export interface NodePings {
  /** node id → last successfully completed batch result. */
  results: Map<string, PingResult>;
  phase: NodePingPhase;
  error: string | null;
  /** Re-probe every node in the active profile. */
  refresh: () => void;
}

interface NodePingState {
  profileId: string | null;
  results: Map<string, PingResult>;
  phase: NodePingPhase;
  error: string | null;
}

const EMPTY_RESULTS = new Map<string, PingResult>();

/**
 * Measures round-trip latency for every node in a profile via the core's batch
 * `ping`. Runs once when the profile changes and on demand (the "test nodes"
 * action), feeding the per-row ping value and the dead/`ok:false` flag. A failed
 * refresh keeps the last completed batch so the UI can show it as stale while
 * the explicit phase prevents it from being mistaken for current.
 */
export function useNodePings(profileId: string | null): NodePings {
  const [state, setState] = useState<NodePingState>(() => ({
    profileId,
    results: new Map(),
    phase: profileId ? "checking" : "idle",
    error: null,
  }));
  const generation = useRef(0);

  const run = useCallback((id: string | null, clearResults: boolean) => {
    const current = ++generation.current;
    setState((previous) => ({
      profileId: id,
      results: clearResults || previous.profileId !== id
        ? new Map()
        : previous.results,
      phase: id ? "checking" : "idle",
      error: null,
    }));
    if (!id) {
      return;
    }
    void Promise.resolve().then(() => api.ping(id))
      .then((list) => {
        if (current !== generation.current) return;
        setState({
          profileId: id,
          results: new Map(list.map((r) => [r.node, r])),
          phase: "ready",
          error: null,
        });
      })
      .catch((e: unknown) => {
        if (current !== generation.current) return;
        setState((previous) => ({
          ...previous,
          phase: "failed",
          error: e instanceof Error ? e.message : String(e),
        }));
      });
  }, []);

  // Re-probe whenever the selected profile changes. Clears first so rows from
  // the previous profile don't show another profile's latencies.
  useEffect(() => {
    run(profileId, true);
    return () => { generation.current += 1; };
  }, [profileId, run]);

  const refresh = useCallback(() => run(profileId, false), [run, profileId]);

  // Effects run after render. During the first render after a profile switch,
  // the stored batch still belongs to the previous profile; never expose it as
  // current while the effect starts the new batch.
  if (state.profileId !== profileId) {
    return {
      results: EMPTY_RESULTS,
      phase: profileId ? "checking" : "idle",
      error: null,
      refresh,
    };
  }

  return {
    results: state.results,
    phase: state.phase,
    error: state.error,
    refresh,
  };
}
