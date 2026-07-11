import { useSyncExternalStore } from "react";

const QUERY = "(prefers-reduced-motion: reduce)";

function subscribe(onChange: () => void): () => void {
  // matchMedia is absent in some webviews and the test DOM; treat that as a
  // stable "motion allowed" with nothing to subscribe to.
  const mql = window.matchMedia?.(QUERY);
  if (!mql) {
    return () => {};
  }
  mql.addEventListener("change", onChange);
  return () => mql.removeEventListener("change", onChange);
}

function getSnapshot(): boolean {
  return window.matchMedia?.(QUERY)?.matches ?? false;
}

/**
 * Tracks the user's `prefers-reduced-motion` preference, re-rendering when it
 * flips. Components gate JS-driven motion (the status-word decrypt, the traffic
 * pulse) on this; CSS keyframes are already neutralised by the global media
 * query in global.css. Defaults to "motion allowed" where matchMedia is absent
 * (older webviews, the test DOM), matching the CSS default.
 */
export function useReducedMotion(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, () => false);
}
