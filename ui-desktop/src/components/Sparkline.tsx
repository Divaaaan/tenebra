interface SparklineProps {
  /** Recent samples, oldest first; may be empty or short. */
  points: number[];
  /** Stroke colour; defaults to the current text colour. */
  color?: string;
  width?: number;
  height?: number;
  /** When set, the chart is exposed to assistive tech; otherwise it's decorative. */
  "aria-label"?: string;
}

// Keep the peak and baseline strokes off the very edge so neither clips.
const PAD_Y = 3;

/** Two decimals is plenty for screen coordinates and keeps the DOM string small. */
function round(n: number): number {
  return Math.round(n * 100) / 100;
}

/**
 * Build a smooth path through the points using a Catmull-Rom spline converted to
 * cubic béziers. A curved line reads as a living waveform rather than a jagged
 * polyline, which suits the "traffic is flowing" feeling without costing much:
 * it's still a single <path> string computed once per render.
 */
function smoothLine(pts: { x: number; y: number }[]): string {
  if (pts.length < 2) {
    return "";
  }
  let d = `M${pts[0].x},${pts[0].y}`;
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[i - 1] ?? pts[i];
    const p1 = pts[i];
    const p2 = pts[i + 1];
    const p3 = pts[i + 2] ?? p2;
    // Catmull-Rom → cubic Bézier control points (tension 1/6 = standard CR).
    const c1x = round(p1.x + (p2.x - p0.x) / 6);
    const c1y = round(p1.y + (p2.y - p0.y) / 6);
    const c2x = round(p2.x - (p3.x - p1.x) / 6);
    const c2y = round(p2.y - (p3.y - p1.y) / 6);
    d += ` C${c1x},${c1y} ${c2x},${c2y} ${p2.x},${p2.y}`;
  }
  return d;
}

/**
 * A dependency-free traffic wave for a rolling window of rate samples. It's a
 * pure function of its props — the parent owns the data window and decides when
 * to feed new points, so this never re-renders on its own. The baseline is fixed
 * at zero (rates are never negative) so the height honestly reflects magnitude;
 * a flat-zero series renders as a flat line along the bottom rather than NaN.
 *
 * Visual: a soft gradient area under a smooth curve, plus a glowing dot riding
 * the most recent sample so the "now" edge of the line feels live. Colour comes
 * from `currentColor` so the caller's per-direction accent drives everything.
 */
export function Sparkline({
  points,
  color = "currentColor",
  width = 120,
  height = 28,
  "aria-label": ariaLabel,
}: SparklineProps) {
  const label = ariaLabel
    ? { role: "img" as const, "aria-label": ariaLabel }
    : { "aria-hidden": true as const };

  // A stable-enough id so the gradient/filter defs don't collide between the two
  // stacked charts on the Home screen.
  const gradId = `spark-fill-${ariaLabel ?? "x"}`.replace(/[^a-z0-9-]/gi, "");

  const svgProps = {
    width,
    height,
    viewBox: `0 0 ${width} ${height}`,
    preserveAspectRatio: "none" as const,
    ...label,
  };

  // Fewer than two points can't describe a line; show an empty frame rather than
  // risk a divide-by-zero or a single dangling vertex.
  if (points.length < 2) {
    return <svg {...svgProps} />;
  }

  const baseline = height - PAD_Y;
  const usableH = height - PAD_Y * 2;
  // Floor the max so an all-zero window stays pinned to the baseline instead of
  // dividing by zero.
  const max = Math.max(...points, 1);
  const stepX = width / (points.length - 1);

  const coords = points.map((value, i) => {
    const safe = Number.isFinite(value) && value > 0 ? value : 0;
    const x = round(i * stepX);
    const y = round(baseline - (safe / max) * usableH);
    return { x, y };
  });

  const line = smoothLine(coords);
  const last = coords[coords.length - 1];
  const first = coords[0];
  // Close the smooth line down to the baseline at both ends for a filled area.
  const area = `${line} L${last.x},${baseline} L${first.x},${baseline} Z`;

  return (
    <svg {...svgProps}>
      <defs>
        {/* A vertical fade so the area melts into the card toward the baseline,
            with preserveAspectRatio="none" the gradient still maps top→bottom. */}
        <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity={0.34} />
          <stop offset="100%" stopColor={color} stopOpacity={0.02} />
        </linearGradient>
      </defs>

      <path d={area} fill={`url(#${gradId})`} stroke="none" />
      <path
        d={line}
        fill="none"
        stroke={color}
        strokeWidth={1.75}
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
      {/* The live edge: a dot on the newest sample, with a soft halo. Drawn last
          so it sits above the line. r is in user units; the non-scaling stroke
          keeps the line crisp regardless of the x-stretch. */}
      <circle
        className="spark-head-halo"
        cx={last.x}
        cy={last.y}
        r={3.5}
        fill={color}
        fillOpacity={0.25}
      />
      <circle cx={last.x} cy={last.y} r={1.8} fill={color} />
    </svg>
  );
}
