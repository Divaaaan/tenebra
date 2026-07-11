import { useEffect, useRef, useState } from "react";

import { subscribeToast } from "../lib/toast";

/**
 * How long one toast occupies the slot before the next is shown, in ms. The CSS
 * `toast-life` animation runs for 2600ms (in → hold → fade); the extra ~100ms
 * lets the fade finish before the node is unmounted and gives a clean gap between
 * queued toasts. Exported for the tests that drive the queue on fake timers.
 */
export const TOAST_LIFE_MS = 2700;

interface QueuedToast {
  id: number;
  message: string;
}

/**
 * Renders one action-confirmation toast at a time, bottom-left, above every
 * overlay. Messages arrive on the toast bus (see lib/toast) and are queued: a new
 * one shows immediately when the slot is free, otherwise it waits its turn. The
 * bar is inert (pointer-events: none in CSS) — it confirms, it never blocks.
 *
 * The persistent host element carries the live region so a screen reader
 * announces each message as it appears. Motion is CSS-only (`toast-life`), so the
 * repo's global reduced-motion rule neutralises it automatically; the toast then
 * simply appears for its lifetime and is removed, with no movement.
 */
export function ToastHost() {
  const [current, setCurrent] = useState<QueuedToast | null>(null);
  const queueRef = useRef<QueuedToast[]>([]);
  const activeRef = useRef(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const idRef = useRef(0);

  useEffect(() => {
    // Show the next queued toast, or clear the slot when the queue drains. The
    // timer re-invokes advance, so the queue empties one toast at a time.
    const advance = () => {
      const next = queueRef.current.shift();
      if (!next) {
        activeRef.current = false;
        timerRef.current = null;
        setCurrent(null);
        return;
      }
      activeRef.current = true;
      setCurrent(next);
      timerRef.current = setTimeout(advance, TOAST_LIFE_MS);
    };

    const unsubscribe = subscribeToast((message) => {
      queueRef.current.push({ id: idRef.current++, message });
      // Kick the queue only when the slot is idle; a busy slot picks the new one
      // up when its timer fires.
      if (!activeRef.current) {
        advance();
      }
    });

    return () => {
      unsubscribe();
      if (timerRef.current) {
        clearTimeout(timerRef.current);
        timerRef.current = null;
      }
      activeRef.current = false;
    };
  }, []);

  return (
    <div className="toast-host" role="status" aria-live="polite">
      {current && (
        // key remounts the bar per message so the CSS animation replays, even for
        // two identical messages in a row.
        <div className="toast" key={current.id}>
          <span className="toast-mark" aria-hidden="true">
            ▶
          </span>
          <span className="toast-msg">{current.message}</span>
        </div>
      )}
    </div>
  );
}
