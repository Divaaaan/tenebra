import { api } from "../api";

/**
 * One-shot guard. The first uncaught front-end fault of a session — a window
 * error, an unhandled rejection, or a React render error caught by the
 * ErrorBoundary — is recorded; later ones are ignored so a repeating fault can't
 * spam the crash file or the command channel.
 */
let reported = false;

/** Cap the stack excerpt handed to the core, keeping the crash file bounded. */
const MAX_STACK = 4000;

/**
 * Record an uncaught front-end error: drop a local breadcrumb in localStorage and
 * hand the message plus a short stack excerpt to the core, which appends them to
 * the crash file (the webview can't write files itself). Debounced to the first
 * error per session. Best-effort — it never throws and never awaits.
 */
export function reportWebCrash(message: string, stack: string): void {
  if (reported) return;
  reported = true;
  try {
    localStorage.setItem(
      "tenebra.webCrash",
      JSON.stringify({ ts: Date.now(), message }),
    );
  } catch {
    // Storage blocked or full — the breadcrumb is a nicety, not a requirement.
  }
  void api.recordWebCrash(message, stack.slice(0, MAX_STACK)).catch(() => {});
}

/**
 * Register global handlers for uncaught errors and unhandled promise rejections.
 * Both route through the shared debounce, so at most one report is filed per
 * session however the fault surfaces. Call once at startup.
 */
export function installGlobalCrashHandlers(): void {
  window.addEventListener("error", (e) => {
    reportWebCrash(e.message || "uncaught error", e.error?.stack ?? "");
  });
  window.addEventListener("unhandledrejection", (e) => {
    const reason: unknown = e.reason;
    const message =
      reason instanceof Error
        ? reason.message
        : String(reason ?? "unhandled rejection");
    const stack = reason instanceof Error ? (reason.stack ?? "") : "";
    reportWebCrash(message, stack);
  });
}

/** Test-only: reset the once-per-session guard between cases. */
export function __resetWebCrashGuard(): void {
  reported = false;
}
