import { useEffect, useState } from "react";

// Single JS-side source of truth for whether motion is allowed. CSS owns the
// preference via the --motion-ok custom property (global.css flips it to "0"
// under prefers-reduced-motion: reduce); reading it here keeps JS-driven
// animation in lockstep with the stylesheet instead of duplicating the query.

const QUERY = "(prefers-reduced-motion: reduce)";

/**
 * One-shot read of the motion preference, sourced from the --motion-ok CSS
 * variable so it tracks whatever global.css decides. Safe to call outside React
 * (event handlers, rAF loops). Returns false when there is no DOM.
 */
export function prefersReducedMotion(): boolean {
  if (typeof window === "undefined") {
    return false;
  }
  const flag = getComputedStyle(document.documentElement)
    .getPropertyValue("--motion-ok")
    .trim();
  return flag === "0";
}

/**
 * Live motion preference for components. Subscribes to the media query so a
 * change to the OS setting re-renders consumers. Initialised from matchMedia
 * rather than the CSS var to avoid a layout read during render.
 */
export function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(
    () => typeof window !== "undefined" && window.matchMedia(QUERY).matches,
  );

  useEffect(() => {
    if (typeof window === "undefined") {
      return;
    }
    const mql = window.matchMedia(QUERY);
    const onChange = () => setReduced(mql.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);

  return reduced;
}
