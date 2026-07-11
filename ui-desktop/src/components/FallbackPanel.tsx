import type { Attempt, AttemptsEvent, NodeProtocol } from "../api";
import { useI18n } from "../i18n/I18nContext";

// The fallback walk narrates transport protocols, not raw enum names: the first
// tier is REALITY-flavoured VLESS, so it shows as REALITY. Purely presentational.
const PROTOCOL_LABELS: Record<NodeProtocol, string> = {
  vless: "REALITY",
  hysteria2: "HYSTERIA2",
  amneziawg: "AMNEZIAWG",
  shadowsocks: "SHADOWSOCKS",
  trojan: "TROJAN",
  vmess: "VMESS",
};

export function protocolLabel(protocol: NodeProtocol): string {
  return PROTOCOL_LABELS[protocol] ?? protocol.toUpperCase();
}

export type AttemptDisplayStatus =
  | "waiting"
  | "trying"
  | "blocked"
  | "ok"
  | "reserve";

/**
 * A row's display status. It mirrors the snapshot status, except a still-`waiting`
 * candidate that sits after one which already came up (`ok`) reads as "reserve" —
 * the walk has settled, so it will never be tried. Derived purely from the
 * snapshot (the core reports "waiting" for both cases).
 */
export function attemptDisplayStatus(
  item: Attempt,
  items: Attempt[],
): AttemptDisplayStatus {
  if (item.status === "waiting") {
    const settledBefore = items.some(
      (other) => other.seq < item.seq && other.status === "ok",
    );
    return settledBefore ? "reserve" : "waiting";
  }
  return item.status;
}

export type FallbackHead =
  | { kind: "attempt"; tried: number; total: number }
  | { kind: "success"; protocol: NodeProtocol }
  | { kind: "blocked" };

/**
 * The header status: the successful protocol once one comes up, "all blocked" on
 * an exhausted walk, otherwise the live attempt count. `tried` counts candidates
 * the walk has reached (anything past `waiting`), floored at 1 so the very first
 * frame reads "1/M" rather than "0/M".
 */
export function fallbackHead(attempts: AttemptsEvent): FallbackHead {
  const succeeded = attempts.items.find((item) => item.status === "ok");
  if (succeeded) {
    return { kind: "success", protocol: succeeded.protocol };
  }
  if (attempts.outcome === "exhausted") {
    return { kind: "blocked" };
  }
  const tried = attempts.items.filter(
    (item) => item.status !== "waiting",
  ).length;
  return {
    kind: "attempt",
    tried: Math.max(1, tried),
    total: attempts.items.length,
  };
}

const STATUS_STRING: Record<
  AttemptDisplayStatus,
  "statusWaiting" | "statusTrying" | "statusBlocked" | "statusOk" | "statusReserve"
> = {
  waiting: "statusWaiting",
  trying: "statusTrying",
  blocked: "statusBlocked",
  ok: "statusOk",
  reserve: "statusReserve",
};

interface FallbackPanelProps {
  /** The latest fallback-walk snapshot to visualise. */
  attempts: AttemptsEvent;
  /** Resolve a candidate's node id to a display name; defaults to the id. */
  resolveNodeName?: (nodeId: string) => string;
}

/**
 * Visualises the anti-DPI fallback walk in place of the current-node card: one
 * row per candidate in the plan, each showing its protocol, target node, and live
 * status as the core steps through them. Every value comes straight from the
 * attempts snapshot — no synthetic timing or demo data.
 */
export function FallbackPanel({
  attempts,
  resolveNodeName = (id) => id,
}: FallbackPanelProps) {
  const { t } = useI18n();
  const head = fallbackHead(attempts);

  const headText =
    head.kind === "success"
      ? t.fallback.success.replace(
          "{proto}",
          protocolLabel(head.protocol).toLowerCase(),
        )
      : head.kind === "blocked"
        ? t.fallback.blockedAll
        : t.fallback.attempt
            .replace("{n}", String(head.tried))
            .replace("{m}", String(head.total));
  const headTone =
    head.kind === "success" ? "ok" : head.kind === "blocked" ? "bad" : "trying";

  return (
    <div className="fallback">
      <div className="fallback-head">
        <span className="fallback-title">{t.fallback.title}</span>
        <span className={`fallback-status ${headTone}`}>{headText}</span>
      </div>
      <ol className="fallback-rows">
        {attempts.items.map((item) => {
          const status = attemptDisplayStatus(item, attempts.items);
          return (
            <li key={item.seq} className={`fallback-row status-${status}`}>
              <span className="fb-idx">
                {String(item.seq).padStart(2, "0")}
              </span>
              <span className="fb-proto">{protocolLabel(item.protocol)}</span>
              <span className="fb-node">{resolveNodeName(item.node)}</span>
              {item.last_good && (
                <span className="fb-tag">{t.fallback.lastGood}</span>
              )}
              <span className="fb-state">
                {t.fallback[STATUS_STRING[status]]}
              </span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
