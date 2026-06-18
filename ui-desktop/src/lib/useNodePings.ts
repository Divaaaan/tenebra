import { useCallback, useEffect, useState } from "react";

import { api, type PingResult } from "../api";

export interface NodePings {
  /** node id → latest probe result. */
  results: Map<string, PingResult>;
  pinging: boolean;
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

  const run = useCallback((id: string | null) => {
    if (!id) {
      setResults(new Map());
      return;
    }
    let cancelled = false;
    setPinging(true);
    api
      .ping(id)
      .then((list) => {
        if (cancelled) return;
        setResults(new Map(list.map((r) => [r.node, r])));
      })
      .catch(() => {
        // Surfaced on the log channel; leave prior results in place.
      })
      .finally(() => {
        if (!cancelled) setPinging(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Re-probe whenever the selected profile changes. Clears first so rows from
  // the previous profile don't show another profile's latencies.
  useEffect(() => {
    setResults(new Map());
    const cancel = run(profileId);
    return cancel;
  }, [profileId, run]);

  const refresh = useCallback(() => run(profileId), [run, profileId]);

  return { results, pinging, refresh };
}
