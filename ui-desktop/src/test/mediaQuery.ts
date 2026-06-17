import { vi } from "vitest";

type ChangeListener = (e: MediaQueryListEvent) => void;

/**
 * Install a controllable `window.matchMedia`. Returns a handle whose
 * `setMatches` flips the result of the reduced-motion query and notifies any
 * listeners registered via `addEventListener("change", …)`, so a hook that
 * subscribes (useReducedMotion) re-renders on change. `restore` puts the
 * previous implementation back.
 */
export function mockMatchMedia(initial = false) {
  const previous = window.matchMedia;
  const listeners = new Set<ChangeListener>();
  let matches = initial;

  const add = (listener: unknown) => listeners.add(listener as ChangeListener);
  const remove = (listener: unknown) =>
    listeners.delete(listener as ChangeListener);

  // Cast through unknown: we implement only the slice of MediaQueryList the code
  // under test touches (matches + the change-event listener pair), not the full
  // EventTarget overload surface.
  const mql = {
    get matches() {
      return matches;
    },
    media: "(prefers-reduced-motion: reduce)",
    onchange: null,
    addEventListener: (_type: string, listener: unknown) => add(listener),
    removeEventListener: (_type: string, listener: unknown) => remove(listener),
    addListener: (listener: unknown) => add(listener),
    removeListener: (listener: unknown) => remove(listener),
    dispatchEvent: () => true,
  } as unknown as MediaQueryList;

  window.matchMedia = vi.fn().mockReturnValue(mql);

  return {
    setMatches(next: boolean) {
      matches = next;
      const event = { matches: next } as MediaQueryListEvent;
      listeners.forEach((l) => l(event));
    },
    restore() {
      window.matchMedia = previous;
    },
  };
}
