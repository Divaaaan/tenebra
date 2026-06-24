import { forwardRef } from "react";

import type { NodeProtocol, Profile } from "../api";
import { useI18n } from "../i18n/I18nContext";
import type { Strings } from "../i18n/strings";
import { REGION_CHIPS, type Region } from "../lib/region";

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
    },
    searchRef,
  ) {
    const { t } = useI18n();

    let visible = rows;
    if (region) {
      visible = visible.filter((r) => r.region === region);
    }
    const q = query.trim().toLowerCase();
    if (q) {
      visible = visible.filter(
        (r) =>
          r.name.toLowerCase().includes(q) || r.city.toLowerCase().includes(q),
      );
    }
    const online = rows.filter((r) => !r.dead).length;

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
              ref={searchRef}
              value={query}
              onChange={(e) => onQuery(e.target.value)}
              placeholder={t.servers.searchPlaceholder}
              autoComplete="off"
              spellCheck={false}
              aria-label={t.servers.searchPlaceholder}
            />
          </div>
        </div>

        <div className="srv-list">
          {rows.length === 0 ? (
            <div className="srv-empty">{t.servers.noNodes}</div>
          ) : visible.length === 0 ? (
            <div className="srv-empty">{t.servers.emptyFilter}</div>
          ) : (
            visible.map((s) => {
              const active = s.id === activeNodeId;
              const pingCls = s.dead
                ? " dead"
                : s.rttMs !== null && s.rttMs >= 120
                  ? " hi"
                  : "";
              return (
                <div
                  key={s.id}
                  className={`srv-row${active ? " active" : ""}${s.dead ? " is-dead" : ""}`}
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
                      {s.name}
                      {active && <span className="srv-active-dot" aria-hidden="true" />}
                    </span>
                    {s.city && <span className="srv-city">{s.city}</span>}
                  </div>
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
