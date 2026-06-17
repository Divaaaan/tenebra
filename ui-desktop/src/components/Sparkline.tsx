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
const PAD_Y = 2;

/** Two decimals is plenty for screen coordinates and keeps the DOM string small. */
function round(n: number): number {
  return Math.round(n * 100) / 100;
}

/**
 * Cheap, dependency-free sparkline for a rolling window of rate samples. It's a
 * pure function of its props — the parent owns the data window and decides when
 * to feed new points, so this never re-renders on its own. The baseline is fixed
 * at zero (rates are never negative) so the height honestly reflects magnitude;
 * a flat-zero series renders as a flat line along the bottom rather than NaN.
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

  const svgProps = {
    width,
    height,
    viewBox: `0 0 ${width} ${height}`,
    preserveAspectRatio: "none" as const,
    ...label,
  };

  // Fewer than two points can't describe a line; show an empty frame rather
  // than risk a divide-by-zero or a single dangling vertex.
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

  const line = coords.map((p) => `${p.x},${p.y}`).join(" ");
  // Close the line down to the baseline at both ends for a soft filled area.
  const first = coords[0];
  const last = coords[coords.length - 1];
  const area = `M${first.x},${baseline} L${line.replace(/ /g, " L")} L${last.x},${baseline} Z`;

  return (
    <svg {...svgProps}>
      <path d={area} fill={color} fillOpacity={0.12} stroke="none" />
      <polyline
        points={line}
        fill="none"
        stroke={color}
        strokeWidth={1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}
