import { useEffect, useRef, useState } from "react";

interface CountUpOptions {
  /** Tween length in milliseconds. */
  duration?: number;
  /** When false, the value snaps to the target with no animation. */
  enabled?: boolean;
}

// Below this delta a tween isn't worth a frame; just set the value. Keeps tiny
// per-tick traffic jitter from looking like a constant shimmer.
const MIN_DELTA = 0.5;

const easeOutCubic = (t: number) => 1 - Math.pow(1 - t, 3);

/**
 * Smoothly interpolates a displayed number toward `target` with rAF. When the
 * target changes mid-tween the animation restarts from the value currently on
 * screen, so live-updating counters glide rather than jump. The caller formats
 * the result; this only returns the interpolated number.
 */
export function useCountUp(target: number, options?: CountUpOptions): number {
  const duration = options?.duration ?? 600;
  const enabled = options?.enabled ?? true;
  const safeTarget = Number.isFinite(target) ? target : 0;

  const [display, setDisplay] = useState(safeTarget);

  // Refs so the rAF loop can read live values without being a render dep.
  const frame = useRef(0);
  const displayRef = useRef(safeTarget);
  displayRef.current = display;

  useEffect(() => {
    if (!enabled) {
      setDisplay(safeTarget);
      return;
    }

    const from = displayRef.current;
    const delta = safeTarget - from;
    if (Math.abs(delta) < MIN_DELTA) {
      setDisplay(safeTarget);
      return;
    }

    let start = 0;
    const step = (now: number) => {
      if (start === 0) {
        start = now;
      }
      const t = Math.min((now - start) / duration, 1);
      setDisplay(from + delta * easeOutCubic(t));
      if (t < 1) {
        frame.current = requestAnimationFrame(step);
      }
    };
    frame.current = requestAnimationFrame(step);

    return () => cancelAnimationFrame(frame.current);
  }, [safeTarget, duration, enabled]);

  return display;
}
