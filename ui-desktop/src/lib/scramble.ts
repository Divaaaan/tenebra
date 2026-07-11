/** Glyph pool the status-word decrypt animation scrambles through. */
export const SCRAMBLE_POOL = "▓▒░#/+*<>";

/**
 * One frame of the status-word "decrypt": characters left of the reveal point —
 * which advances with `frame` — show the target, the rest are random glyphs from
 * `pool`. Pure and deterministic given `rand`, so the animation's shape is
 * unit-testable without timers. The terminal frame (`frame >= frames`) is the
 * clean target, so the settled word is always exact.
 */
export function scrambleFrame(
  target: string,
  frame: number,
  frames: number,
  pool: string = SCRAMBLE_POOL,
  rand: () => number = Math.random,
): string {
  if (frame >= frames) {
    return target;
  }
  const reveal = Math.floor((frame / frames) * target.length);
  let out = "";
  for (let i = 0; i < target.length; i++) {
    out += i < reveal ? target[i] : pool[Math.floor(rand() * pool.length)];
  }
  return out;
}
