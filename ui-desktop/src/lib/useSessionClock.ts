import { useEffect, useState } from "react";

import type { ConnectionState } from "../api";

/**
 * Seconds elapsed in the current connected session. Starts counting when the
 * state becomes "connected" and resets to 0 the moment it leaves that state, so
 * the Session stat reads 0 while idle/connecting and climbs once the tunnel is
 * live. Time is measured from a wall-clock start (not interval counting) so it
 * stays accurate even if timers are throttled.
 */
export function useSessionClock(phase: ConnectionState): number {
  const [secs, setSecs] = useState(0);

  useEffect(() => {
    if (phase !== "connected") {
      setSecs(0);
      return;
    }
    const start = Date.now();
    setSecs(0);
    const id = setInterval(() => {
      setSecs(Math.floor((Date.now() - start) / 1000));
    }, 1000);
    return () => clearInterval(id);
  }, [phase]);

  return secs;
}

/** Format a second count as mm:ss (or hh:mm:ss past an hour). */
export function formatUptime(secs: number): string {
  const s = Math.max(0, Math.floor(secs));
  const hours = Math.floor(s / 3600);
  const mins = Math.floor((s % 3600) / 60);
  const rem = s % 60;
  const pad = (n: number) => String(n).padStart(2, "0");
  return hours > 0
    ? `${pad(hours)}:${pad(mins)}:${pad(rem)}`
    : `${pad(mins)}:${pad(rem)}`;
}
