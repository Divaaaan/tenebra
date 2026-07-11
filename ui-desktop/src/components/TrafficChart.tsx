import { useId } from "react";

interface TrafficChartProps {
  /** Download rate samples (Mbps), oldest → newest. */
  down: number[];
  /** Upload rate samples (Mbps). */
  up: number[];
  /** Whether the session is live; idle draws faint, flat lines. */
  active: boolean;
  ariaLabel?: string;
}

// Fixed viewBox, stretched to the container with preserveAspectRatio="none";
// non-scaling strokes keep weights crisp at any width. Straight segments only —
// no smoothing, no splines — per the spec-sheet aesthetic.
const W = 600;
const H = 84;
const PAD_X = 2;
const PAD_Y = 8;

/**
 * The traffic sparkline: download in --signal (2px, over a vertical gradient area
 * fill) above upload in --text-dim (1px). While live, the latest download sample
 * carries a --signal head dot with a slow pulse ring. A single midline, no other
 * gridlines. Pure presentation — the numeric readouts beside it carry the values
 * for assistive tech, so this is aria-hidden unless given a label.
 */
export function TrafficChart({ down, up, active, ariaLabel }: TrafficChartProps) {
  // A per-instance id keeps the gradient <defs> from colliding if the chart is
  // ever mounted more than once. Strip the colons React 18 emits so the id is a
  // clean url(#…) reference.
  const gradientId = `tnb-area-${useId().replace(/:/g, "")}`;
  const n = down.length;
  if (n < 2) {
    return <svg className="traffic-svg" viewBox={`0 0 ${W} ${H}`} aria-hidden />;
  }

  const peak = Math.max(20, ...down, ...up) * 1.15;
  const xFor = (i: number) => PAD_X + (i * (W - 2 * PAD_X)) / (n - 1);
  const yFor = (v: number) => H - PAD_Y - (v / peak) * (H - 2 * PAD_Y);
  const points = (arr: number[]) =>
    arr.map((v, i) => `${xFor(i).toFixed(1)},${yFor(v).toFixed(1)}`).join(" ");

  const downLine = points(down);
  const upLine = points(up);
  const fillPoints =
    `${xFor(0).toFixed(1)},${H - PAD_Y} ` +
    downLine +
    ` ${xFor(n - 1).toFixed(1)},${H - PAD_Y}`;
  const midY = (H - PAD_Y - 0.5 * (H - 2 * PAD_Y)).toFixed(1);
  const headX = xFor(n - 1).toFixed(1);
  const headY = yFor(down[n - 1]).toFixed(1);

  return (
    <svg
      className="traffic-svg"
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="none"
      role={ariaLabel ? "img" : undefined}
      aria-label={ariaLabel}
      aria-hidden={ariaLabel ? undefined : true}
    >
      <defs>
        <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="var(--signal)" stopOpacity="0.22" />
          <stop offset="1" stopColor="var(--signal)" stopOpacity="0.02" />
        </linearGradient>
      </defs>
      <line
        x1="0"
        x2={W}
        y1={midY}
        y2={midY}
        stroke="var(--line)"
        strokeWidth="1"
        vectorEffect="non-scaling-stroke"
      />
      {active && <polygon points={fillPoints} fill={`url(#${gradientId})`} />}
      <polyline
        points={upLine}
        fill="none"
        stroke="var(--text-dim)"
        strokeWidth="1"
        vectorEffect="non-scaling-stroke"
        opacity={active ? 1 : 0.4}
      />
      <polyline
        points={downLine}
        fill="none"
        stroke="var(--signal)"
        strokeWidth="2"
        vectorEffect="non-scaling-stroke"
        opacity={active ? 1 : 0.3}
      />
      {active && (
        <>
          <circle className="chart-pulse" cx={headX} cy={headY} r="7" />
          <circle className="chart-dot" cx={headX} cy={headY} r="2.5" />
        </>
      )}
    </svg>
  );
}
