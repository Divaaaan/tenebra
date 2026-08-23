import { useEffect, useRef, useState } from "react";

import { useReducedMotion } from "./useReducedMotion";

/**
 * How long a surface stays mounted after it has been dismissed, in ms. Matches
 * `--d-base` in tokens.css — the exit step of the motion scale. Exported so the
 * tests can drive the window without guessing at it.
 */
export const EXIT_MS = 190;

export interface Presence<T> {
  /**
   * What to render: the live value while open, and the *last* live value for as
   * long as the exit runs. Null once the surface is really gone.
   */
  value: T | null;
  /** True while the exit animation is running — drive an `is-leaving` class off it. */
  leaving: boolean;
}

/**
 * Keeps a conditionally-rendered surface (an overlay, a modal, a panel) mounted
 * long enough to animate out.
 *
 * React unmounts the moment its condition goes false, which is why every dialog
 * in this app used to animate in and then simply blink out of existence — half a
 * transition, and the half the user is least forgiving of, because a surface that
 * vanishes gives no clue whether it was dismissed or crashed.
 *
 * The value is latched rather than merely flagged: a modal's content usually goes
 * away in the same tick as the flag that shows it (`setConnectRequest(null)`),
 * and rendering the exit against an empty value would flash a blank card. Holding
 * the last non-null value means the surface leaves showing exactly what it showed
 * while it was open.
 *
 * Under `prefers-reduced-motion` there is no exit to wait for, so the value drops
 * immediately — the same discipline the stylesheets keep.
 */
export function usePresence<T>(value: T | null, exitMs = EXIT_MS): Presence<T> {
  const reduced = useReducedMotion();
  const [shown, setShown] = useState<T | null>(value);
  const [leaving, setLeaving] = useState(false);
  // The latch. A ref, not state: it must be readable in the same render that
  // sees `value` go null, before any effect has had a chance to run.
  const last = useRef<T | null>(value);
  if (value !== null) {
    last.current = value;
  }

  useEffect(() => {
    if (value !== null) {
      setShown(value);
      setLeaving(false);
      return;
    }
    // Nothing was on screen — nothing to play out.
    if (shown === null) {
      return;
    }
    if (reduced || exitMs <= 0) {
      setShown(null);
      setLeaving(false);
      return;
    }
    setLeaving(true);
    const id = setTimeout(() => {
      setShown(null);
      setLeaving(false);
    }, exitMs);
    return () => clearTimeout(id);
  }, [value, shown, reduced, exitMs]);

  if (shown === null) {
    return { value: null, leaving: false };
  }
  return { value: value ?? last.current, leaving };
}
