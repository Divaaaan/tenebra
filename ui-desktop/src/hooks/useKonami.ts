import { useEffect, useRef } from "react";

// The canonical Konami code: ↑ ↑ ↓ ↓ ← → ← → B A. Arrow keys arrive as their
// multi-character `key` names; the two letters are matched case-insensitively.
const KONAMI_SEQUENCE = [
  "ArrowUp",
  "ArrowUp",
  "ArrowDown",
  "ArrowDown",
  "ArrowLeft",
  "ArrowRight",
  "ArrowLeft",
  "ArrowRight",
  "b",
  "a",
] as const;

/**
 * Fire `onUnlock` when the Konami code is typed anywhere in the window. A hidden
 * affordance, so it stands down while a text field owns the keyboard (typing "ba"
 * into a search box should never spend the sequence). Progress is forgiving: a
 * wrong key resets the run, but if that key is itself the first of the sequence
 * the run restarts from one, so a fumbled start can recover.
 *
 * The callback rides a ref refreshed every render, so the listener subscribes
 * once and always calls the latest handler — no re-subscribe churn, and no stale
 * closure if the caller's callback identity changes.
 */
export function useKonami(onUnlock: () => void): void {
  const onUnlockRef = useRef(onUnlock);
  onUnlockRef.current = onUnlock;
  const progress = useRef(0);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      const tag = el?.tagName;
      if (
        tag === "INPUT" ||
        tag === "TEXTAREA" ||
        tag === "SELECT" ||
        el?.isContentEditable === true
      ) {
        progress.current = 0;
        return;
      }

      // Letters come through as single characters (case varies with Shift/Caps);
      // normalise those. The arrow names are already stable multi-char strings.
      const key = e.key.length === 1 ? e.key.toLowerCase() : e.key;

      if (key === KONAMI_SEQUENCE[progress.current]) {
        progress.current += 1;
        if (progress.current === KONAMI_SEQUENCE.length) {
          progress.current = 0;
          onUnlockRef.current();
        }
        return;
      }

      // Miss: restart. A key that is itself the opening key seeds a fresh run at
      // one rather than dropping to zero.
      progress.current = key === KONAMI_SEQUENCE[0] ? 1 : 0;
    };

    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}
