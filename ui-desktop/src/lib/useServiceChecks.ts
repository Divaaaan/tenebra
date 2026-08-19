import { useCallback, useEffect, useRef, useState } from "react";

import { api, type ConnectionState, type ServiceCheck } from "../api";

export interface ServiceChecksState {
  checks: ServiceCheck[];
  checking: boolean;
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
export function useServiceChecks(phase: ConnectionState): ServiceChecksState {
  const [checks, setChecks] = useState<ServiceCheck[]>([]);
  const [checking, setChecking] = useState(false);
  const inFlight = useRef(false);

  const run = useCallback(() => {
    if (inFlight.current) return;
    inFlight.current = true;
    setChecking(true);
    // Wrapped so a core without the command degrades to "no checks" rather than
    // throwing into the render path.
    void Promise.resolve()
      .then(() => api.checkServices())
      .then((r) => setChecks(r.checks))
      .catch(() => setChecks([]))
      .finally(() => {
        inFlight.current = false;
        setChecking(false);
      });
  }, []);

  useEffect(() => {
    if (phase === "connected") {
      run();
      return;
    }
    setChecks([]);
  }, [phase, run]);

  return { checks, checking, refresh: run };
}
