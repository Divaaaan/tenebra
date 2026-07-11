// A tiny module-level pub/sub for one-line action confirmations ("toasts").
//
// It lives outside React on purpose: any module — a global key handler, an async
// action deep in a screen, a state-transition effect — can raise a toast by
// importing pushToast, without threading a callback through props or reaching for
// a context. ToastHost is the sole subscriber in the running app; it renders the
// messages and owns all policy (ordering, lifetime, queueing). Keeping the channel
// this small — a Set of listeners, no timers, no queue — is the whole point.

type ToastListener = (message: string) => void;

const listeners = new Set<ToastListener>();

/**
 * Raise a one-line confirmation. Delivered synchronously to every current
 * subscriber; if none are mounted the message is simply dropped — a toast is
 * transient feedback, not state to persist.
 */
export function pushToast(message: string): void {
  // Iterate a copy so a listener that (un)subscribes during delivery can't
  // disturb the walk.
  for (const listener of [...listeners]) {
    listener(message);
  }
}

/**
 * Subscribe to raised toasts. Returns an unsubscribe function; call it on
 * teardown so an unmounted host stops receiving.
 */
export function subscribeToast(listener: ToastListener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
