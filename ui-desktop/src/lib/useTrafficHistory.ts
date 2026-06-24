import { useEffect, useRef, useState } from "react";

import type { ConnectionState } from "../api";

/** Ring-buffer length feeding the traffic chart — ~30s of one-per-second
 *  samples, matching the chart's fixed x-resolution. */
export const TRAFFIC_BUFFER = 44;

export interface TrafficHistory {
  /** Download rate samples, in Mbps, oldest → newest. */
  down: number[];
  /** Upload rate samples, in Mbps. */
  up: number[];
}

const zeros = () => Array<number>(TRAFFIC_BUFFER).fill(0);

/**
 * A fixed-length history of download/upload rates (in Mbps) for the sparkline.
 * Pushes one sample per distinct traffic event while connected and resets to a
 * flat line when the session ends, so the chart never replays a stale tail. The
 * buffer stays a constant length so the chart's x-spacing is stable.
 */
export function useTrafficHistory(
  phase: ConnectionState,
  downRate: number,
  upRate: number,
): TrafficHistory {
  const [history, setHistory] = useState<TrafficHistory>(() => ({
    down: zeros(),
    up: zeros(),
  }));
  const lastStamp = useRef<number | null>(null);

  useEffect(() => {
    if (phase !== "connected") {
      if (lastStamp.current !== null) {
        lastStamp.current = null;
        setHistory({ down: zeros(), up: zeros() });
      }
      return;
    }
    // Coalesce equal back-to-back renders: a new sample only lands when the core
    // emits a fresh rate pair.
    const stamp = downRate + upRate * 1e-3;
    if (lastStamp.current === stamp) {
      return;
    }
    lastStamp.current = stamp;
    const toMbps = (bps: number) => (bps * 8) / 1_000_000;
    setHistory((prev) => ({
      down: prev.down.slice(1).concat(toMbps(downRate)),
      up: prev.up.slice(1).concat(toMbps(upRate)),
    }));
  }, [phase, downRate, upRate]);

  return history;
}
