import { useReducedMotion } from "../lib/motion";
import { useCountUp } from "../lib/useCountUp";

interface RollingNumberProps {
  /** Raw numeric source value. */
  value: number;
  /** Formatter applied to the interpolated value, e.g. (n) => formatBytes(n). */
  format: (n: number) => string;
  className?: string;
}

/**
 * Displays a value that rolls smoothly toward changes instead of snapping.
 * Honours reduced-motion by disabling the tween. The visible text shows the
 * in-flight value, but aria-label carries the real target so assistive tech
 * announces the settled number, not intermediate frames.
 */
export function RollingNumber({ value, format, className }: RollingNumberProps) {
  const reducedMotion = useReducedMotion();
  const display = useCountUp(value, { enabled: !reducedMotion });

  return (
    <span className={className} aria-label={format(value)}>
      {format(display)}
    </span>
  );
}
