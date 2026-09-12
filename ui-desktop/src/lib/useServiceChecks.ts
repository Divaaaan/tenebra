import { useCallback, useEffect, useRef, useState } from "react";

import { api, type ConnectionState, type ServiceCheck } from "../api";

export interface ServiceChecksState {
  checks: ServiceCheck[];
  checking: boolean;
  /**
   * How many checks have finished this session, counting the ones that came
   * back empty. A watcher that wants to reason across runs — "video has failed
   * twice in a row" — needs to know when a new verdict has landed, and `checks`
   * is a fresh array on every render whether or not anything was measured.
   */
  runs: number;
  /** Re-run the checks on demand. */
  refresh: () => void;
}

/**
 * Runs the post-connect service checks: video, voice, game latency.
 *
 * It fires once each time the tunnel reaches connected, and not while idle —
 * the answers are only meaningful for the connection that is actually up, and
 * probing an idle machine would report the state the user is trying to leave.
 *
 * Results are cleared the moment the connection drops rather than left on
 * screen. Stale ticks next to a disconnected tunnel are worse than no ticks:
 * they say "everything works" about a session that no longer exists.
 */
export function useServiceChecks(phase: ConnectionState, sessionKey = ""): ServiceChecksState {
  const [checks, setChecks] = useState<ServiceCheck[]>([]);
  const [checking, setChecking] = useState(false);
  const [runs, setRuns] = useState(0);
  const inFlight = useRef(false);
  const generation = useRef(0);
  const phaseRef = useRef(phase);
  phaseRef.current = phase;

  const run = useCallback(() => {
    if (inFlight.current || phaseRef.current !== "connected") return;
    const current = generation.current;
    inFlight.current = true;
    setChecking(true);
    // Wrapped so a core without the command degrades to "no checks" rather than
    // throwing into the render path.
    void Promise.resolve()
      .then(() => api.checkServices())
      .then((r) => { if (current === generation.current) setChecks(r.checks); })
      .catch(() => { if (current === generation.current) setChecks([]); })
      .finally(() => {
        if (current !== generation.current) return;
        inFlight.current = false;
        setChecking(false);
        setRuns((n) => n + 1);
      });
  }, []);

  useEffect(() => {
    if (phase === "connected") {
      setChecks([]);
      setRuns(0);
      run();
    } else {
      setChecks([]);
      setRuns(0);
      setChecking(false);
    }
    return () => {
      generation.current += 1;
      inFlight.current = false;
    };
  }, [phase, sessionKey, run]);

  return { checks, checking, runs, refresh: run };
}
