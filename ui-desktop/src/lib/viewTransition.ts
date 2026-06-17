import { prefersReducedMotion } from "./motion";

/**
 * Run a DOM update inside a view transition so the change animates per the
 * ::view-transition rules in app.css. Falls back to applying the update
 * synchronously when the API is missing (older WebView) or when the user has
 * asked for reduced motion, so the result is identical — just without the
 * cross-fade. Always call the update; never let a missing API drop it.
 */
export function withViewTransition(update: () => void): void {
  // Feature-detect rather than trust the static type: lib.dom declares this on
  // Document, but the runtime WebView may predate it.
  if (!("startViewTransition" in document) || prefersReducedMotion()) {
    update();
    return;
  }
  document.startViewTransition(update);
}
