import { useId, useState } from "react";

interface HelpHintProps {
  /** Short title of the popover. */
  title: string;
  /** Lines of the explanation, rendered in order. */
  lines: string[];
  /** Accessible label for the trigger. */
  label: string;
}

/**
 * A "?" affordance that reveals an explanation on hover or focus.
 *
 * Hover alone is not enough: the same trigger has to work for a keyboard, and a
 * hint that only exists on hover is invisible to anyone tabbing through. So the
 * popover opens on focus too, and Escape closes it — the behaviour a tooltip is
 * expected to have once it carries content worth reading.
 *
 * It is a hint, not a dialog: nothing inside is focusable, so it never traps the
 * keyboard, and the trigger keeps its own focus while the panel is shown.
 */
export function HelpHint({ title, lines, label }: HelpHintProps) {
  const [open, setOpen] = useState(false);
  const id = useId();

  return (
    <span
      className="hint"
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
    >
      <button
        type="button"
        className="hint__trigger"
        aria-label={label}
        aria-expanded={open}
        aria-describedby={open ? id : undefined}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onKeyDown={(e) => {
          if (e.key === "Escape") setOpen(false);
        }}
        // Tapping the trigger toggles, so the hint is reachable without a mouse
        // hover on touch input too.
        onClick={() => setOpen((v) => !v)}
      >
        ?
      </button>
      {open && (
        <span className="hint__pop" id={id} role="tooltip">
          <span className="hint__title">{title}</span>
          {lines.map((line, i) => (
            <span
              key={line}
              className="hint__line"
              // Lines cascade so the eye follows the order of the steps rather
              // than being handed a block of text at once. The chunk step (not
              // the row step): there are only ever a handful, and each is meant
              // to be landed on.
              style={{ animationDelay: `${i * 60}ms` }}
            >
              {line}
            </span>
          ))}
        </span>
      )}
    </span>
  );
}
