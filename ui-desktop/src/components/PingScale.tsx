/** Round-trip time (ms) → number of lit bars (5 = fastest, 1 = slowest). */
export function pingScaleLevel(rttMs: number): number {
  if (rttMs < 40) return 5;
  if (rttMs < 70) return 4;
  if (rttMs < 110) return 3;
  if (rttMs < 160) return 2;
  return 1;
}

/** Fill level → the accent the lit bars take. */
export function pingScaleTone(level: number): "good" | "warn" | "signal" {
  if (level >= 4) return "good";
  if (level === 3) return "warn";
  return "signal";
}

const BARS = [0, 1, 2, 3, 4];

interface PingScaleProps {
  /** Round-trip time in milliseconds. */
  rttMs: number;
  /** Accessible label; the meter is decorative (aria-hidden) without one. */
  ariaLabel?: string;
}

/**
 * A five-bar signal-strength meter for a node's latency: lower ping lights more
 * bars, and the fill steps good → warn → signal as latency climbs. Bar heights
 * live in CSS (nth-child); this only decides how many bars are lit and in which
 * tone. Shared by the connection card and the server list so the two never drift.
 */
export function PingScale({ rttMs, ariaLabel }: PingScaleProps) {
  const level = pingScaleLevel(rttMs);
  const tone = pingScaleTone(level);
  return (
    <span
      className="ping-scale"
      role={ariaLabel ? "img" : undefined}
      aria-label={ariaLabel}
      aria-hidden={ariaLabel ? undefined : true}
    >
      {BARS.map((i) => (
        <span
          key={i}
          className={`ping-scale-bar${i < level ? ` on ${tone}` : ""}`}
        />
      ))}
    </span>
  );
}
