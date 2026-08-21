import type { CheckStage, CheckTarget, NodeCheckResult } from "../api";

/**
 * Presentation helpers for node-check results.
 *
 * Selection itself stays in the core (`core/nodecheck`) — it owns the ordering
 * and hands back results already ranked. What lives here is only what the UI
 * needs to *render* a row: whether to draw it as working, what number to show,
 * and which failure to name. Keeping the verdict in one place matters because a
 * UI that disagreed with the core about "usable" would show a green row the
 * core refuses to connect to.
 */

/** True when this target completed end to end. */
export function targetOk(t: CheckTarget): boolean {
  return t.stage === "ok";
}

/**
 * Whether a node is worth connecting to: a strict majority of its probed
 * targets succeeded. Mirrors `NodeResult.Usable` in the core.
 *
 * Majority rather than "any" because a single incidental success is how a
 * black-holed node passes — it can serve one destination while timing out on
 * everything the user actually opens. Majority rather than "all" because one
 * blocked or down destination should not condemn a healthy exit. With nothing
 * probed the node is not usable: unmeasured must never outrank measured.
 */
export function isUsable(r: NodeCheckResult): boolean {
  if (r.targets.length === 0) return false;
  return okCount(r) * 2 > r.targets.length;
}

/** How many targets completed end to end. */
export function okCount(r: NodeCheckResult): number {
  return r.targets.reduce((n, t) => (targetOk(t) ? n + 1 : n), 0);
}

/**
 * The node's latency: the median round-trip across successful targets, or null
 * when none succeeded.
 *
 * Median, not mean — one target behind a congested link would drag an average
 * far enough to reorder otherwise equal nodes, while the median tracks what the
 * connection usually feels like. Even counts take the lower middle sample, so
 * the number shown is one that was actually measured.
 */
export function medianRtt(r: NodeCheckResult): number | null {
  const rtts = r.targets.filter((t) => targetOk(t) && t.rttMs > 0).map((t) => t.rttMs);
  if (rtts.length === 0) return null;
  rtts.sort((a, b) => a - b);
  return rtts[Math.floor((rtts.length - 1) / 2)];
}

/**
 * The stage to show for a node: "ok" when usable, otherwise the failure that hit
 * the most targets. Reporting the dominant failure rather than the first one
 * keeps the badge honest when a node fails different ways on different
 * destinations — the common cause is the useful one.
 *
 * Ties break toward the earlier stage in dial → handshake → probe order, since
 * the earliest failure is the most fundamental thing to fix.
 */
export function dominantStage(r: NodeCheckResult): CheckStage {
  if (isUsable(r)) return "ok";

  const order: CheckStage[] = ["dial", "handshake", "probe"];
  const counts = new Map<CheckStage, number>();
  for (const t of r.targets) {
    if (t.stage === "ok") continue;
    counts.set(t.stage, (counts.get(t.stage) ?? 0) + 1);
  }

  let best: CheckStage = "probe";
  let bestCount = -1;
  for (const stage of order) {
    const c = counts.get(stage) ?? 0;
    if (c > bestCount) {
      best = stage;
      bestCount = c;
    }
  }
  return best;
}
