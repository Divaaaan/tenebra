import { useCallback, useEffect, useRef, useState } from "react";

import { api, type PingResult } from "../api";

export interface NodePings {
  /** node id → latest probe result. */
  results: Map<string, PingResult>;
  pinging: boolean;
  stale: boolean;
  error: string | null;
  /** Re-probe every node in the active profile. */
  refresh: () => void;
}

/**
 * Measures round-trip latency for every node in a profile via the core's batch
 * `ping`. Runs once when the profile changes and on demand (the "test nodes"
 * action), feeding the per-row ping value and the dead/`ok:false` flag. A failed
 * probe call is swallowed: stale results simply remain until the next refresh.
 */
export function useNodePings(profileId: string | null): NodePings {
  const [results, setResults] = useState<Map<string, PingResult>>(new Map());
  const [pinging, setPinging] = useState(false);
  const [stale, setStale] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const generation = useRef(0);

  const run = useCallback((id: string | null) => {
    const current = ++generation.current;
    setError(null);
    if (!id) {
      setResults(new Map());
      setPinging(false);
      setStale(false);
      return;
    }
    setStale(true);
    setPinging(true);
    void Promise.resolve().then(() => api.ping(id))
      .then((list) => {
        if (current !== generation.current) return;
        setResults(new Map(list.map((r) => [r.node, r])));
        setStale(false);
      })
      .catch((e: unknown) => {
        if (current !== generation.current) return;
        setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (current === generation.current) setPinging(false);
      });
  }, []);

  // Re-probe whenever the selected profile changes. Clears first so rows from
  // the previous profile don't show another profile's latencies.
  useEffect(() => {
    setResults(new Map());
    run(profileId);
    return () => { generation.current += 1; };
  }, [profileId, run]);

  const refresh = useCallback(() => run(profileId), [run, profileId]);

  return { results, pinging, stale, error, refresh };
}
