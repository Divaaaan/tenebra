import type { ConnectionState } from "../api";

/**
 * Whether the tunnel is up or on its way up — connected, mid-connect, or in an
 * automatic health failover. An in-app update relaunches the process to apply
 * itself (and on Windows the updater stops the background service to swap it),
 * which would tear a live tunnel down mid-session. So the updater treats this as
 * "don't disturb": it never installs while this is true, deferring until the
 * tunnel is down. The idle and error phases are not busy — nothing is riding the
 * tunnel — so an install there is safe.
 */
export function tunnelBusy(phase: ConnectionState): boolean {
  return (
    phase === "connected" ||
    phase === "connecting" ||
    phase === "health_reconnecting"
  );
}
