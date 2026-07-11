import { forwardRef, useCallback, useEffect, useMemo, useRef } from "react";

import type { NodeProtocol, Profile } from "../api";
import { useI18n } from "../i18n/I18nContext";
import type { Strings } from "../i18n/strings";
import { REGION_CHIPS, type Region } from "../lib/region";
import { PingScale } from "./PingScale";

/** A node enriched with derived location and a latency probe, ready to render. */
export interface ServerRow {
  id: string;
  /** Display code — the node's own name. */
  name: string;
  /** Derived location subtitle; "" hides it. */
  city: string;
  region: Region;
  protocol: NodeProtocol;
  /** Round-trip ms, or null if not yet measured. */
  rttMs: number | null;
  /** Probe came back failed. */
  dead: boolean;
  /** TLS certificate verification is off (skip-cert-verify) on this node. */
  insecure: boolean;
}

interface ServerListProps {
  profiles: Profile[];
  selectedProfileId: string | null;
  onSelectProfile: (id: string) => void;
  /** Enriched nodes of the selected profile (unfiltered). */
  rows: ServerRow[];
  /** Connected node id, for the active marker. */
  activeNodeId: string | null;
  region: Region | null;
  onRegion: (r: Region | null) => void;
  query: string;
  onQuery: (q: string) => void;
  onSelectNode: (id: string) => void;
  onAddSubscription: () => void;
  pinging: boolean;
  /**
   * OPTIONAL — for the orchestrator to wire from App. True when the exit is
   * auto-picked (no node pinned by hand): the AUTO row then carries the active
   * state and no node row is marked active. Dormant until wired — it falls back
   * to "no active node" (`activeNodeId === null`), which is already correct while
   * idle; the connected-auto case needs the real prop to be exact.
   */
  auto?: boolean;
  /**
   * OPTIONAL — for the orchestrator to wire from App. Invoked when the AUTO row
   * is clicked (hand control back to auto-select). Dormant until wired: the row
   * renders and reads its state, but the click is a no-op. Wire this to an App
   * handler that clears the pinned node (e.g. selectedNodeId → "") and, when
   * connected, re-handshakes onto the fastest node.
   */
  onSelectAuto?: () => void;
}

const PROTO_LABEL: Record<NodeProtocol, string> = {
  vless: "VLESS",
  hysteria2: "HY2",
  amneziawg: "AWG",
  shadowsocks: "SS",
  trojan: "TROJAN",
  vmess: "VMESS",
};

const REGION_LABELS: Record<string, keyof Strings["servers"]> = {
  all: "regionAll",
  europe: "regionEurope",
  americas: "regionAmericas",
  asiaPac: "regionAsiaPac",
};

// A placeholder five-bar meter for rows with no usable latency (dead, or not yet
// probed): all bars sit unlit, holding the grid column so rows stay aligned. The
// live meter is the shared PingScale; this only mirrors its slot, never its logic.
const BLANK_BARS = [0, 1, 2, 3, 4];

export const ServerList = forwardRef<HTMLInputElement, ServerListProps>(
  function ServerList(
    {
      profiles,
      selectedProfileId,
      onSelectProfile,
      rows,
      activeNodeId,
      region,
      onRegion,
      query,
      onQuery,
      onSelectNode,
      onAddSubscription,
      pinging,
      auto,
      onSelectAuto,
    },
    forwardedRef,
  ) {
    const { t } = useI18n();

    // Merge the forwarded search ref with a local one: App still holds the input
    // ref (its "change" affordance focuses it directly), and the pane also focuses
    // the input itself when the shell ("/" hotkey) or the connection card ("change"
    // button) broadcasts "tenebra:focus-search" — keeping the zones decoupled.
    const searchRef = useRef<HTMLInputElement | null>(null);
    const setSearchRef = useCallback(
      (node: HTMLInputElement | null) => {
        searchRef.current = node;
        if (typeof forwardedRef === "function") {
          forwardedRef(node);
        } else if (forwardedRef) {
          (forwardedRef as { current: HTMLInputElement | null }).current = node;
        }
      },
      [forwardedRef],
    );

    useEffect(() => {
      const focus = () => searchRef.current?.focus();
      window.addEventListener("tenebra:focus-search", focus);
      return () => window.removeEventListener("tenebra:focus-search", focus);
    }, []);

    const q = query.trim().toLowerCase();
    const filtered = region !== null || q !== "";

    let visible = rows;
    if (region) {
      visible = visible.filter((r) => r.region === region);
    }
    if (q) {
      visible = visible.filter(
        (r) =>
          r.name.toLowerCase().includes(q) || r.city.toLowerCase().includes(q),
      );
    }

    // The lowest-ping live node — the target the AUTO row names and times. Read
    // from the unfiltered rows, since auto ignores the region/search filter just
    // as the core does when it picks. Null until something has a ping: shown as a
    // neutral row rather than a guessed name.
    const bestRow = useMemo(() => {
      let best: ServerRow | null = null;
      let bestRtt = Infinity;
      for (const r of rows) {
        if (!r.dead && r.rttMs !== null && r.rttMs < bestRtt) {
          best = r;
          bestRtt = r.rttMs;
        }
      }
      return best;
    }, [rows]);

    // AUTO is active whenever no node is pinned by hand. The `auto` prop is
    // authoritative once App wires it; until then "no active node" is the honest
    // stand-in (exact while idle; connected-auto needs the prop).
    const isAuto = auto ?? activeNodeId === null;

    const online = rows.filter((r) => !r.dead).length;
    const insecureCount = rows.filter((r) => r.insecure).length;
    const insecureSummary = t.servers.insecureSummary
      .replace("{n}", String(insecureCount))
      .replace("{m}", String(rows.length));

    const hasNodes = rows.length > 0;
    const noneVisible = visible.length === 0;

    // Which of the three real emptinesses is on screen — named honestly, each
    // with the CTA that actually resolves it.
    let emptyMessage: string;
    let showReset = false;
    let showImport = false;
    if (profiles.length === 0) {
      emptyMessage = t.topbar.noSubscription;
      showImport = true;
    } else if (!hasNodes) {
      emptyMessage = t.servers.noNodes;
      showImport = true;
    } else {
      emptyMessage = t.servers.emptyFilter;
      showReset = filtered;
    }

    const handleReset = () => {
      onRegion(null);
      onQuery("");
    };

    const autoSub = bestRow
      ? t.servers.autoSub.replace("{node}", bestRow.name.toLowerCase())
      : t.servers.autoSubIdle;

    return (
      <div className="pane srv">
        <div className="srv-head">
          <div className="srv-subs">
            {profiles.length > 1 ? (
              <div className="srv-sub-chips" role="tablist">
                {profiles.map((p) => (
                  <button
                    type="button"
                    key={p.id}
                    role="tab"
                    aria-selected={p.id === selectedProfileId}
                    className={`chip${p.id === selectedProfileId ? " on" : ""}`}
                    onClick={() => onSelectProfile(p.id)}
                  >
                    {p.name}
                  </button>
                ))}
              </div>
            ) : (
              <span className="srv-sub-name">
                {profiles[0]?.name ?? t.topbar.noSubscription}
              </span>
            )}
            <button type="button" className="srv-add" onClick={onAddSubscription}>
              {t.servers.addSub}
            </button>
          </div>

          <div className="srv-title">
            <h2>
              {t.servers.title} · {online} {t.servers.online}
              {pinging && <span className="srv-pinging"> · …</span>}
            </h2>
            <div className="count">
              {t.servers.showing} <b>{visible.length}</b>
            </div>
          </div>

          {insecureCount > 0 && (
            <div className="srv-insecure-summary" role="alert">
              <span className="srv-insecure-mark" aria-hidden="true">
                !
              </span>
              {insecureSummary}
            </div>
          )}

          <div className="srv-filters" role="group">
            {REGION_CHIPS.map(({ key, labelKey }) => (
              <button
                type="button"
                key={labelKey}
                className={`chip${region === key ? " on" : ""}`}
                onClick={() => onRegion(key)}
              >
                {t.servers[REGION_LABELS[labelKey]]}
              </button>
            ))}
          </div>

          <div className="srv-search">
            <span className="prompt" aria-hidden="true">
              &gt;
            </span>
            <input
              ref={setSearchRef}
              value={query}
              onChange={(e) => onQuery(e.target.value)}
              placeholder={t.servers.searchPlaceholder}
              autoComplete="off"
              spellCheck={false}
              aria-label={t.servers.searchPlaceholder}
            />
            <span className="srv-search-hint" aria-hidden="true">
              /
            </span>
          </div>
        </div>

        <div className="srv-list">
          {hasNodes && (
            <div
              className={`srv-auto${isAuto ? " on" : ""}`}
              role="button"
              tabIndex={0}
              aria-pressed={isAuto}
              onClick={() => onSelectAuto?.()}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  onSelectAuto?.();
                }
              }}
            >
              <div className="srv-auto-name">
                <span className="srv-auto-code">
                  <span>{t.servers.auto}</span>
                  {isAuto && (
                    <span className="srv-auto-dot" aria-hidden="true" />
                  )}
                </span>
                <span className="srv-auto-sub">{autoSub}</span>
              </div>
              <div className="srv-auto-rtt">
                {bestRow && bestRow.rttMs !== null
                  ? `${bestRow.rttMs} ${t.units.ms}`
                  : "·"}
              </div>
              <div className="srv-auto-tag">{t.servers.autoTag}</div>
            </div>
          )}

          {noneVisible ? (
            <div className="srv-empty">
              <span>{emptyMessage}</span>
              {showReset && (
                <button
                  type="button"
                  className="srv-empty-cta"
                  onClick={handleReset}
                >
                  {t.servers.resetFilter}
                </button>
              )}
              {showImport && (
                <button
                  type="button"
                  className="srv-empty-cta"
                  onClick={onAddSubscription}
                >
                  {t.servers.importSub}
                </button>
              )}
            </div>
          ) : (
            visible.map((s, i) => {
              const active = !isAuto && s.id === activeNodeId;
              const pingCls = s.dead
                ? " dead"
                : s.rttMs !== null && s.rttMs >= 120
                  ? " hi"
                  : "";
              return (
                <div
                  key={s.id}
                  className={`srv-row${active ? " active" : ""}${s.dead ? " is-dead" : ""}`}
                  style={{ animationDelay: `${i * 26}ms` }}
                  role="button"
                  tabIndex={s.dead ? -1 : 0}
                  aria-disabled={s.dead}
                  onClick={() => !s.dead && onSelectNode(s.id)}
                  onKeyDown={(e) => {
                    if (!s.dead && (e.key === "Enter" || e.key === " ")) {
                      e.preventDefault();
                      onSelectNode(s.id);
                    }
                  }}
                >
                  <div className="srv-name">
                    <span className="srv-node">
                      <span className="srv-node-code">{s.name}</span>
                      {active && (
                        <span className="srv-active-dot" aria-hidden="true" />
                      )}
                      {s.insecure && (
                        <span
                          className="srv-insecure"
                          title={t.servers.insecureTitle}
                          aria-label={t.servers.insecureTitle}
                        >
                          {t.servers.insecureBadge}
                        </span>
                      )}
                    </span>
                    {s.city && <span className="srv-city">{s.city}</span>}
                  </div>
                  {!s.dead && s.rttMs !== null ? (
                    <PingScale rttMs={s.rttMs} />
                  ) : (
                    <span className="ping-scale" aria-hidden="true">
                      {BLANK_BARS.map((b) => (
                        <span key={b} className="ping-scale-bar" />
                      ))}
                    </span>
                  )}
                  <div className={`srv-ping${pingCls}`}>
                    {s.dead
                      ? t.servers.down
                      : s.rttMs !== null
                        ? `${s.rttMs} ${t.units.ms}`
                        : "·"}
                  </div>
                  <div className="srv-proto">{PROTO_LABEL[s.protocol]}</div>
                </div>
              );
            })
          )}
        </div>
      </div>
    );
  },
);
