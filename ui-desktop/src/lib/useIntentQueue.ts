import { useRef, useState } from "react";

/** Serialize full-payload commands while retaining each individual edit.
 * A failed command is removed before the next intent is derived, so it cannot
 * roll back a later, unrelated edit or silently retry the rejected field.
 */
export function useIntentQueue<T>(authoritative: T, onError: (e: unknown) => void) {
  type Intent = { change: (value: T) => T; save: (value: T) => Promise<void> };
  const base = useRef(authoritative);
  const source = useRef(authoritative);
  const pending = useRef<Intent[]>([]);
  const running = useRef(false);
  const report = useRef(onError);
  report.current = onError;
  const [, redraw] = useState(0);
  if (!running.current && source.current !== authoritative) {
    source.current = authoritative;
    base.current = authoritative;
  }
  const value = pending.current.reduce((v, intent) => intent.change(v), base.current);

  function enqueue(change: Intent["change"], save: Intent["save"]) {
    pending.current.push({ change, save });
    redraw((n) => n + 1);
    if (running.current) return;
    running.current = true;
    void (async () => {
      while (pending.current.length > 0) {
        const intent = pending.current[0];
        const next = intent.change(base.current);
        let failed = false;
        try {
          await intent.save(next);
          base.current = next;
        } catch (e) {
          failed = true;
          report.current(e);
        }
        pending.current.shift();
        if (failed) redraw((n) => n + 1);
      }
      running.current = false;
    })();
  }
  return { value, enqueue };
}
