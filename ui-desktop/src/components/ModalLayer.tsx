import { useLayoutEffect, useRef, type HTMLAttributes } from "react";
import { createPortal } from "react-dom";

const layers: HTMLElement[] = [];
const locks = new Map<Element, { count: number; inert: boolean }>();
const focusable = 'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), summary, [tabindex]:not([tabindex="-1"])';

/** One focus/keyboard boundary for every modal, including stacked import flows. */
export function ModalLayer({ onClose, children, ...props }: HTMLAttributes<HTMLDivElement> & { onClose: () => void }) {
  const root = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useLayoutEffect(() => {
    const element = root.current!;
    const previous = document.activeElement as HTMLElement | null;
    layers.push(element);
    const background = [...document.body.children].filter((el) => el !== element);
    for (const el of background) {
      const lock = locks.get(el) ?? { count: 0, inert: el.hasAttribute("inert") };
      lock.count += 1;
      locks.set(el, lock);
      el.setAttribute("inert", "");
    }
    const controls = () => [...element.querySelectorAll<HTMLElement>(focusable)]
      .filter((el) => {
        if (el.closest('[inert], [hidden], [aria-hidden="true"]')) return false;
        // Responsive close buttons and whole panels can be hidden by CSS,
        // without a hidden attribute. Focusing one leaves the browser on BODY.
        for (let node: HTMLElement | null = el; node; node = node.parentElement) {
          const style = getComputedStyle(node);
          if (style.display === "none" || style.visibility === "hidden" || style.visibility === "collapse") return false;
          if (node === element) break;
        }
        return true;
      });
    const focusFirst = () => (controls()[0] ?? element).focus();
    focusFirst();
    const isTop = () => layers[layers.length - 1] === element;
    const onFocus = (event: FocusEvent) => {
      if (isTop() && !element.contains(event.target as Node)) focusFirst();
    };
    const onKey = (event: KeyboardEvent) => {
      if (!isTop()) return;
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopImmediatePropagation();
        close.current();
      } else if (event.key === "Tab") {
        const list = controls();
        const first = list[0];
        const last = list[list.length - 1];
        if (!first || !element.contains(document.activeElement) ||
          (event.shiftKey && document.activeElement === first) ||
          (!event.shiftKey && document.activeElement === last)) {
          event.preventDefault();
          (event.shiftKey ? last ?? element : first ?? element).focus();
        }
      }
    };
    document.addEventListener("keydown", onKey, true);
    document.addEventListener("focusin", onFocus);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      document.removeEventListener("focusin", onFocus);
      layers.splice(layers.indexOf(element), 1);
      for (const el of background) {
        const lock = locks.get(el)!;
        if (--lock.count === 0) {
          if (!lock.inert) el.removeAttribute("inert");
          locks.delete(el);
        }
      }
      if (previous?.isConnected && !previous.closest("[inert]")) previous.focus();
    };
  }, []);
  return createPortal(<div {...props} ref={root} tabIndex={-1}>{children}</div>, document.body);
}
