import { useEffect, useRef, useState } from "react";

import { SCRAMBLE_POOL, scrambleFrame } from "./scramble";
import { useReducedMotion } from "./useReducedMotion";

interface ScrambleOptions {
  /** Number of frames from full scramble to the clean word. */
  frames?: number;
  /** Milliseconds between frames. */
  intervalMs?: number;
  /** Glyph pool to scramble through. */
  pool?: string;
}

/**
 * Returns `text`, but every time it *changes* runs a brief left-to-right decrypt
 * (random glyphs settling into the real characters). The first value shows
 * instantly, and so does every value under `prefers-reduced-motion` — the effect
 * is decorative, never the only way the word appears. Timing defaults to the
 * design's 12 frames × 34 ms; callers can override it (tests do).
 */
export function useScrambledText(
  text: string,
  { frames = 12, intervalMs = 34, pool = SCRAMBLE_POOL }: ScrambleOptions = {},
): string {
  const reduced = useReducedMotion();
  const [display, setDisplay] = useState(text);
  const previous = useRef(text);

  useEffect(() => {
    if (text === previous.current) {
      return;
    }
    previous.current = text;

    if (reduced) {
      setDisplay(text);
      return;
    }

    let frame = 0;
    setDisplay(scrambleFrame(text, frame, frames, pool));
    const id = setInterval(() => {
      frame += 1;
      if (frame >= frames) {
        setDisplay(text);
        clearInterval(id);
        return;
      }
      setDisplay(scrambleFrame(text, frame, frames, pool));
    }, intervalMs);
    return () => clearInterval(id);
  }, [text, reduced, frames, intervalMs, pool]);

  return display;
}
