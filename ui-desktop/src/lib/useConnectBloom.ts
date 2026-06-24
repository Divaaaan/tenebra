import { useEffect, useRef } from "react";

import { prefersReducedMotion } from "./motion";
import type { ConnectionState } from "../api";

// How long the flood lasts. Long enough to feel like a force field switching on,
// short enough that it never gets in the way of using the app.
const BLOOM_MS = 900;

/**
 * Fires a one-shot full-window flash of light the moment the tunnel goes live —
 * the signature "bloom" of the connect experience. It listens to the connection
 * phase and, on the edge into "connected", paints a brief radial wash of the
 * aurora accent over the whole window via the Web Animations API, then removes
 * it. The element is injected on demand (not in the React tree) so it can sit
 * above everything and clean itself up without a re-render.
 *
 * It is purely decorative and non-interactive (pointer-events: none), animates
 * opacity + transform only, and stands down entirely under reduced motion. The
 * very first observed phase is treated as a baseline, so launching already
 * connected (autoconnect) doesn't flash on open.
 */
export function useConnectBloom(phase: ConnectionState): void {
  const prev = useRef<ConnectionState | null>(null);

  useEffect(() => {
    const previous = prev.current;
    prev.current = phase;

    // Baseline the first phase we ever see; only animate real transitions.
    if (previous === null) {
      return;
    }
    // Only the moment of becoming connected blooms.
    if (phase !== "connected" || previous === "connected") {
      return;
    }
    if (prefersReducedMotion() || typeof document === "undefined") {
      return;
    }
    // WAAPI may be absent in an old WebView; bail rather than throw.
    if (typeof Element === "undefined" || !("animate" in Element.prototype)) {
      return;
    }

    const el = document.createElement("div");
    el.setAttribute("aria-hidden", "true");
    Object.assign(el.style, {
      position: "fixed",
      inset: "0",
      zIndex: "60",
      pointerEvents: "none",
      // Aurora light blooming from behind the dial (centre-ish), fading out.
      background:
        "radial-gradient(60rem 60rem at 50% 46%, " +
        "color-mix(in srgb, var(--aurora) 36%, transparent), transparent 60%)",
      mixBlendMode: "screen",
      opacity: "0",
    } satisfies Partial<CSSStyleDeclaration>);

    document.body.appendChild(el);

    const anim = el.animate(
      [
        { opacity: 0, transform: "scale(0.6)" },
        { opacity: 1, transform: "scale(1)", offset: 0.25 },
        { opacity: 0, transform: "scale(1.25)" },
      ],
      { duration: BLOOM_MS, easing: "cubic-bezier(0.16, 1, 0.3, 1)" },
    );

    const cleanup = () => el.remove();
    anim.addEventListener("finish", cleanup);
    anim.addEventListener("cancel", cleanup);

    return () => {
      anim.cancel(); // fires "cancel" → cleanup; guards against unmount mid-bloom
    };
  }, [phase]);
}
