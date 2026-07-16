import { useEffect, useState } from "react";

import type { State } from "../api/types";

/**
 * What the app knows about the daemon build it is attached to, latched from
 * state snapshots.
 *
 * `stale` means the daemon provably comes from a different build than this UI:
 * either it reported another version, or a stable snapshot arrived without the
 * `daemon_version` field at all — a daemon predating 0.4.4, reported as
 * `daemonVersion: null`. Commands added after that build fail as unknown while
 * their toggles silently do nothing, which is exactly the skew this surfaces.
 *
 * The verdict is latched, not recomputed per snapshot: the synthetic
 * reconnecting/lost states the backend pushes while the daemon is away carry no
 * version and no verdict, so the last trusted one holds through an outage, and
 * a reattach to a freshly updated daemon clears the flag without a restart.
 */
export interface DaemonSkew {
  stale: boolean;
  /** The daemon's reported version; null = predates the field (≤0.4.3). */
  daemonVersion: string | null;
}

export function useDaemonSkew(state: State, appVersion: string): DaemonSkew {
  // undefined = no trusted snapshot yet; null = stable snapshot without the
  // field (pre-0.4.4 daemon); string = the version the daemon reported.
  const [seen, setSeen] = useState<string | null | undefined>(undefined);

  useEffect(() => {
    if (state.daemon_version) {
      setSeen(state.daemon_version);
    } else if (state.state === "idle" || state.state === "connected") {
      // Only idle/connected are trustworthy without the field: the synthetic
      // states the backend fabricates while the daemon is away are always
      // connecting/error, and a real daemon that new would have sent a version.
      setSeen(null);
    }
  }, [state.daemon_version, state.state]);

  return {
    stale: seen !== undefined && seen !== appVersion,
    daemonVersion: seen ?? null,
  };
}
