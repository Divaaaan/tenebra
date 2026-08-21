import { useCallback, useRef, useState } from "react";

import { api, type NodeCheckResult } from "../api";

export interface NodeCheckState {
  /** node id → what the last check measured through it. */
  results: Map<string, NodeCheckResult>;
  /** The node to connect to, or null when nothing usable was found. */
  best: string | null;
  checking: boolean;
  /**
   * True once a run has finished, so the UI can tell "no working node" apart
   * from "not measured yet" — both of which otherwise show an empty `best`.
   */
  checked: boolean;
  /** The error from the last run, if it failed outright. */
  error: string | null;
  /** Run a check over the given profile. Resolves to the best node, if any. */
  run: (profileId: string) => Promise<string | null>;
  /** Drop everything measured, e.g. when the profile changes. */
  reset: () => void;
}

/**
 * Drives `check_nodes`: which nodes actually carry traffic, and which one to
 * connect to.
 *
 * Deliberately not automatic on profile change, unlike the ping hook. A check
 * opens real connections through every node to several destinations — seconds of
 * work and real traffic through the user's own nodes — so it runs when something
 * asks for it: pressing connect, or the explicit re-check action.
 *
 * Concurrent runs are collapsed: pressing connect twice must not start a second
 * probe process while the first still holds its loopback ports.
 */
export function useNodeCheck(): NodeCheckState {
  const [results, setResults] = useState<Map<string, NodeCheckResult>>(
    new Map(),
  );
  const [best, setBest] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const [checked, setChecked] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inFlight = useRef<Promise<string | null> | null>(null);

  const run = useCallback((profileId: string): Promise<string | null> => {
    if (inFlight.current) return inFlight.current;

    setChecking(true);
    setError(null);
    // Wrapped rather than called directly so that *nothing* here can take a
    // connect down with it: the measurement is advisory, and a throw on the way
    // in (an older core with no such command, a bridge that has not exposed it)
    // must degrade to "not measured", not to a button that refuses to connect.
    const p = Promise.resolve()
      .then(() => api.checkNodes(profileId))
      .then((check) => {
        setResults(
          new Map(check.results.map((r: NodeCheckResult) => [r.node, r])),
        );
        // The core answers with an empty string when nothing works; keep that
        // distinct from "a node was chosen" all the way to the screen.
        const winner = check.best || null;
        setBest(winner);
        setChecked(true);
        return winner;
      })
      .catch((e: unknown) => {
        setError(e instanceof Error ? e.message : String(e));
        setChecked(true);
        return null;
      })
      .finally(() => {
        setChecking(false);
        inFlight.current = null;
      });

    inFlight.current = p;
    return p;
  }, []);

  const reset = useCallback(() => {
    setResults(new Map());
    setBest(null);
    setChecked(false);
    setError(null);
  }, []);

  return { results, best, checking, checked, error, run, reset };
}
