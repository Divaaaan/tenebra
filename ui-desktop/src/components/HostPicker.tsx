import { useId, useMemo } from "react";

import type { LiveHost } from "../api";
import { formatBytes } from "../lib/format";

/**
 * Every word the picker says, handed in by the screen that mounts it. Nothing is
 * baked in: the component is the same in both languages, and the copy stays in
 * one place with the rest of the app's strings.
 */
export interface HostPickerStrings {
  /** Label above the list. */
  title: string;
  /** The standing note: a snapshot, taken by hand, empty while the tunnel is down. */
  hint: string;
  /** Template with "{n}" — how many connections the snapshot holds. */
  count: string;
  /** Refresh button, idle. */
  refresh: string;
  /** Refresh button while a read is in flight. */
  refreshing: string;
  /** Stand-in line while the first read runs and there is nothing to show yet. */
  loading: string;
  /** The snapshot came back with nothing in it. */
  empty: string;
  /** Why an empty snapshot is ordinary rather than a fault. */
  emptyHint: string;
  /** Short badge on a row that never carried a hostname. */
  ipBadge: string;
  /** The badge spelled out, for the tooltip and for assistive tech. */
  ipHint: string;
  /** Stands in when the engine reported no owning process. */
  unknownProcess: string;
  /** Tooltip on the route tag. */
  routeHint: string;
  /** Template with "{host}" — what clicking a row does. */
  pick: string;
}

export interface HostPickerProps {
  /** The latest snapshot of the engine's connection table, in any order. */
  hosts: LiveHost[];
  /** A read is in flight. */
  loading: boolean;
  /** Why the last read failed, already worded by the caller. */
  error?: string;
  /** The reader picked a row: hand its host to the rule editor. */
  onPick(host: string): void;
  /** Take another snapshot. */
  onRefresh(): void;
  strings: HostPickerStrings;
}

/** Bytes a connection has moved in both directions — what the list ranks on. */
function volume(host: LiveHost): number {
  return host.up + host.down;
}

/**
 * The live-connections list: what the tunnel is carrying right now, one row per
 * host, heaviest first, each a single click away from becoming a rule. It exists
 * because the domain a service actually resolves to is rarely the one a person
 * would type, and typing it wrong fails silently.
 *
 * Presentational only — it reads the snapshot it is given and calls back. It
 * holds no state, keeps no timer, and never re-reads on its own: a list that
 * re-sorts itself under the pointer cannot be clicked, so the button is the only
 * thing that moves it.
 */
export function HostPicker({
  hosts,
  loading,
  error,
  onPick,
  onRefresh,
  strings,
}: HostPickerProps) {
  // Strip the colons React 18 emits so the id is a clean aria reference.
  const titleId = `tnb-hosts-${useId().replace(/:/g, "")}`;

  // Heaviest first: the connection someone is hunting for is nearly always the
  // one moving traffic. Sorted on a copy — the snapshot belongs to the caller —
  // and ties fall back to the host so the order never wobbles between renders.
  const rows = useMemo(
    () =>
      [...hosts].sort(
        (a, b) => volume(b) - volume(a) || a.host.localeCompare(b.host),
      ),
    [hosts],
  );

  const hasRows = rows.length > 0;

  function refresh() {
    // The button is disabled mid-read; the guard holds even if a click gets past
    // it, so a slow engine never gets a second request piled on the first.
    if (loading) {
      return;
    }
    onRefresh();
  }

  return (
    <div className="set-hosts">
      <div className="set-hosts-head">
        <span className="set-hosts-label">
          <span className="set-eyebrow" id={titleId}>
            {strings.title}
          </span>
          {hasRows && (
            <span className="set-hosts-count">
              {strings.count.replace("{n}", String(rows.length))}
            </span>
          )}
        </span>
        <button
          type="button"
          className="set-btn"
          onClick={refresh}
          disabled={loading}
        >
          {loading ? strings.refreshing : strings.refresh}
        </button>
      </div>

      {/* A still frame, not a feed — said up front, because an empty list of
          live connections otherwise reads as a broken screen rather than as an
          idle tunnel. */}
      <p className="set-hosts-note">{strings.hint}</p>

      {error && (
        <p className="set-error" role="alert">
          {error}
        </p>
      )}

      {hasRows && (
        <ul
          className="set-host-list"
          aria-labelledby={titleId}
          aria-busy={loading}
        >
          {rows.map((host) => (
            <li key={host.host} className="set-host-row">
              <button
                type="button"
                className="set-host-pick"
                title={strings.pick.replace("{host}", host.host)}
                onClick={() => onPick(host.host)}
              >
                <span className="set-host-name">
                  <span className="set-host-top">
                    <span className="set-host-id">{host.host}</span>
                    {host.is_ip && (
                      // The row stays pickable — the caller decides what an
                      // address becomes — but it must never pass for a domain,
                      // on screen or read aloud.
                      <span className="set-host-ip" title={strings.ipHint}>
                        <span aria-hidden="true">{strings.ipBadge}</span>
                        <span className="visually-hidden">{strings.ipHint}</span>
                      </span>
                    )}
                  </span>
                  <span className="set-host-proc">
                    {host.process || strings.unknownProcess}
                  </span>
                </span>
                <span className="set-host-bytes">{formatBytes(volume(host))}</span>
                <span className="set-host-route" title={strings.routeHint}>
                  {host.outbound}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}

      {loading && !hasRows && (
        <p className="set-empty" role="status">
          {strings.loading}
        </p>
      )}

      {/* "Nothing is connected" and "we could not look" are different claims, so
          a failed read never borrows the empty-snapshot copy. */}
      {!loading && !error && !hasRows && (
        <div className="set-hosts-empty">
          <p className="set-empty">{strings.empty}</p>
          <p className="set-hosts-note">{strings.emptyHint}</p>
        </div>
      )}
    </div>
  );
}
