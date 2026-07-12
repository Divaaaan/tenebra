import { useEffect } from "react";

/**
 * How long the eclipse holds before it unmounts, in ms. Matches the CSS
 * `eclipse-veil` / `eclipse-corona` animations (in → totality → fade) so the node
 * is removed exactly as the fade lands. Under reduced motion the animations are
 * neutralised (see styles/eclipse.css) and this is simply how long the static
 * frame lingers.
 */
export const ECLIPSE_MS = 2600;

interface EclipseOverlayProps {
  /** When true, the eclipse plays; when false the overlay renders nothing. */
  active: boolean;
  /** Called once the hold elapses, so the host can drop back to `active=false`. */
  onDone: () => void;
}

/**
 * A brief full-window eclipse: darkness sweeps the canvas, an orange corona ring
 * flares to totality, holds, then everything fades. Purely decorative and inert
 * (`pointer-events: none`, `aria-hidden`) — it never intercepts input and is gone
 * on its own after {@link ECLIPSE_MS}. Triggered by three taps on the wordmark; a
 * quiet reward for the curious, nothing the UI depends on. The Latin motto — "in
 * the dark, light" — surfaces inside the totality: the light the eclipse is named
 * for.
 */
export function EclipseOverlay({ active, onDone }: EclipseOverlayProps) {
  useEffect(() => {
    if (!active) {
      return;
    }
    const id = setTimeout(onDone, ECLIPSE_MS);
    return () => clearTimeout(id);
  }, [active, onDone]);

  if (!active) {
    return null;
  }

  return (
    <div className="eclipse" aria-hidden="true">
      <div className="eclipse-corona" />
      <p className="eclipse-motto">In tenebris lux</p>
    </div>
  );
}
