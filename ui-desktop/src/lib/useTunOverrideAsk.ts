import { useCallback, useEffect, useRef, useState } from "react";

export interface TunOverrideAsk {
  /** True while a question is on screen waiting for an answer. */
  pending: boolean;
  /**
   * Ask the user whether to override the tun-conflict guard. Resolves true to
   * connect anyway, false to leave the refusal standing.
   */
  ask: () => Promise<boolean>;
  /** Answer the question on screen. A no-op when nothing is being asked. */
  answer: (ok: boolean) => void;
}

/**
 * The tun-conflict override prompt, as something a connect can await.
 *
 * `window.confirm` is not available here (Tauri brokers it through the dialog
 * plugin, which this app grants only for the file picker), so the question is a
 * React modal and the promise's `resolve` is parked until it is answered.
 *
 * That parking is the whole risk: an unanswered promise never returns to the
 * caller, so its `finally` never runs and the connect stays "in flight" forever —
 * which in v0.5.0 meant a permanently disabled connect button, because simple
 * mode returned its own tree without the modal in it. Hence the two rules here,
 * both of which live in this hook rather than in whoever renders the prompt:
 *
 *   - unmounting answers the question (declining, the safe side), so a closing
 *     window or a swapped-out tree can never strand the asker;
 *   - a second question supersedes the first by answering it, never by dropping
 *     its resolve on the floor.
 */
export function useTunOverrideAsk(): TunOverrideAsk {
  const [pending, setPending] = useState(false);
  // Held in a ref, not in state: the cleanup below has to reach the *current*
  // resolve on unmount, and it runs after the last render.
  const resolveRef = useRef<((ok: boolean) => void) | null>(null);

  const settle = useCallback((ok: boolean) => {
    const resolve = resolveRef.current;
    resolveRef.current = null;
    resolve?.(ok);
  }, []);

  const answer = useCallback(
    (ok: boolean) => {
      setPending(false);
      settle(ok);
    },
    [settle],
  );

  const ask = useCallback(
    () =>
      new Promise<boolean>((resolve) => {
        // Nothing should be pending here (the caller is gated on one connect at
        // a time), but if it is, decline it rather than lose its caller.
        settle(false);
        resolveRef.current = resolve;
        setPending(true);
      }),
    [settle],
  );

  useEffect(
    () => () => {
      settle(false);
    },
    [settle],
  );

  return { pending, ask, answer };
}
