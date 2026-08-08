import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";

/**
 * One installed or running application, as the GUI-side scanner reports it.
 *
 * Declared here rather than imported so the picker stays a leaf: it is the only
 * consumer of the shape, and the api-layer type the settings screen passes down
 * satisfies it structurally.
 */
export interface AppEntry {
  /** Human-facing name, e.g. "Ledger Desk". */
  name: string;
  /** The executable a rule matches on, lower-cased by the scanner. */
  exe: string;
  /** Full path, when the source knew one. */
  path?: string | null;
  /** `data:image/png;base64,…`, or absent when no icon could be extracted. */
  icon?: string | null;
  /** The process is alive right now. */
  running: boolean;
  /** Which scanner source produced the entry (registry, startmenu, process, …). */
  source?: string;
}

/**
 * Every string the picker paints. Nothing is hard-coded: the settings screen owns
 * the copy and the language, and this component only lays it out.
 */
export interface AppPickerStrings {
  /** Dialog heading. */
  title: string;
  /** One line under it: rules match the executable, not the pretty name. */
  hint: string;
  searchPlaceholder: string;
  /** Accessible name for the search field. */
  searchLabel: string;
  /** Heading over processes that are alive right now. */
  running: string;
  /** Heading over everything else the scan found. */
  installed: string;
  /** Shown while the first scan is in flight. */
  loading: string;
  /** The scan came back with nothing at all. */
  empty: string;
  /** The scan found apps, the query matched none of them. */
  noMatches: string;
  /** Run the scan again; doubles as the retry after a failure. */
  rescan: string;
  /** Label the rescan button wears while a scan runs. */
  scanning: string;
  close: string;
  /** "{n} selected" — `{n}` is replaced with the count. */
  selectedCount: string;
}

interface AppPickerProps {
  apps: AppEntry[];
  /** Executables already in the rule. Matched case-insensitively, as the core does. */
  selected: string[];
  loading: boolean;
  /** Why the scan failed, already localized. */
  error?: string;
  onToggle: (exe: string) => void;
  onClose: () => void;
  onRescan: () => void;
  strings: AppPickerStrings;
}

const TITLE_ID = "app-picker-title";
const HINT_ID = "app-picker-hint";
const RUNNING_ID = "app-picker-group-running";
const INSTALLED_ID = "app-picker-group-installed";

// What the focus trap counts as a stop. Rows are excluded by the tabIndex filter
// below, not by this selector — the list is one stop (roving tabindex), so the
// row that currently holds it must still be reachable.
const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]';

/** First character of a name, for the icon-less placeholder. */
function initial(entry: AppEntry): string {
  const source = entry.name.trim() || entry.exe;
  return (Array.from(source)[0] ?? "·").toUpperCase();
}

interface AppRowProps {
  app: AppEntry;
  checked: boolean;
  /** This row holds the list's single tab stop. */
  roving: boolean;
  onToggle: (exe: string) => void;
  register: (exe: string, el: HTMLButtonElement | null) => void;
}

/**
 * A single row. Memoized because the list is as long as the machine's app
 * inventory: without it, ticking one box would re-render every other row, and a
 * scan of several hundred apps turns each keystroke in the search field into a
 * visible stall.
 */
const AppRow = memo(function AppRow({
  app,
  checked,
  roving,
  onToggle,
  register,
}: AppRowProps) {
  return (
    <li className="app-picker-row">
      <button
        ref={(el) => register(app.exe, el)}
        type="button"
        role="checkbox"
        aria-checked={checked}
        tabIndex={roving ? 0 : -1}
        data-exe={app.exe}
        className={`app-picker-item${checked ? " is-on" : ""}`}
        onClick={() => onToggle(app.exe)}
      >
        <span className="app-picker-mark" aria-hidden="true">
          {checked ? "▣" : "▢"}
        </span>
        {app.icon ? (
          <img
            className="app-picker-icon"
            src={app.icon}
            alt=""
            width={20}
            height={20}
            draggable={false}
          />
        ) : (
          // A missing icon is the normal case on several sources, so it gets a
          // deliberate stand-in rather than a broken image or a collapsed row.
          <span className="app-picker-icon app-picker-icon--blank" aria-hidden="true">
            {initial(app)}
          </span>
        )}
        <span className="app-picker-text">
          <span className="app-picker-name">{app.name}</span>
          {/* The rule is written against this, so it is never hidden behind the
              display name — including when the two disagree (launchers, helpers). */}
          <span className="app-picker-exe">{app.exe}</span>
        </span>
      </button>
    </li>
  );
});

/**
 * The app chooser for split tunnelling: pick the executables that bypass (or
 * alone enter) the tunnel, instead of typing their file names from memory.
 *
 * Purely presentational — it never scans, invokes, or persists anything. The
 * scan lives in the Tauri layer (only the GUI process sees the user's own
 * registry and Start menu), and the rule itself is the core's; this renders what
 * it is handed and reports back the executable that was ticked.
 *
 * Modal behaviour follows CrashReportModal: role="dialog", Escape and a scrim
 * click close, focus lands inside on open and returns to the opener on close.
 * The list adds a focus trap and a roving tab stop of its own — an inventory of
 * several hundred rows must not become several hundred tab stops between the
 * search field and the close button.
 */
export function AppPicker({
  apps,
  selected,
  loading,
  error,
  onToggle,
  onClose,
  onRescan,
  strings,
}: AppPickerProps) {
  const [query, setQuery] = useState("");
  const [activeExe, setActiveExe] = useState<string | null>(null);

  const dialogRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  // Keyed by executable rather than by index: the visible rows change with every
  // keystroke in the search field, and index-keyed refs would point at rows that
  // moved out from under them.
  const rowRefs = useRef(new Map<string, HTMLButtonElement>());

  // Focus starts in the search field (the picker exists to be searched) and goes
  // back to whatever opened the dialog when it closes.
  useEffect(() => {
    const opener = document.activeElement;
    searchRef.current?.focus();
    return () => {
      if (opener instanceof HTMLElement && opener.isConnected) {
        opener.focus();
      }
    };
  }, []);

  useEffect(() => {
    function onKey(e: globalThis.KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  // The core compares executable names case-insensitively; the ticks have to
  // agree with it, or a rule added from a hand-typed "Ledger.exe" would read as
  // unticked here and get added a second time.
  const selectedSet = useMemo(
    () => new Set(selected.map((exe) => exe.toLowerCase())),
    [selected],
  );

  const { runningRows, installedRows, flat, total } = useMemo(() => {
    // The scanner deduplicates, but a merged or stale list must not render the
    // same executable twice: two rows with one name is a rule the user cannot
    // reason about. A live process wins the grouping.
    const byExe = new Map<string, AppEntry>();
    for (const app of apps) {
      const key = app.exe.toLowerCase();
      const seen = byExe.get(key);
      if (!seen) {
        byExe.set(key, app);
      } else if (app.running && !seen.running) {
        byExe.set(key, { ...seen, running: true });
      }
    }

    const q = query.trim().toLowerCase();
    const matches = [...byExe.values()].filter(
      (app) =>
        q === "" ||
        app.name.toLowerCase().includes(q) ||
        app.exe.toLowerCase().includes(q),
    );
    // Sorted by name only — never by selection, so ticking a box cannot move the
    // row out from under the pointer.
    matches.sort((a, b) => a.name.localeCompare(b.name));

    const running = matches.filter((app) => app.running);
    const installed = matches.filter((app) => !app.running);
    return {
      runningRows: running,
      installedRows: installed,
      // DOM order, which is also the order the arrow keys walk.
      flat: [...running, ...installed],
      total: byExe.size,
    };
  }, [apps, query]);

  // The tab stop sits on the last row the user touched, falling back to the first
  // visible row whenever that one is filtered away.
  const rovingExe =
    activeExe !== null && flat.some((app) => app.exe === activeExe)
      ? activeExe
      : (flat[0]?.exe ?? null);

  const register = useCallback((exe: string, el: HTMLButtonElement | null) => {
    if (el) {
      rowRefs.current.set(exe, el);
    } else {
      rowRefs.current.delete(exe);
    }
  }, []);

  // Kept stable through a ref so a parent that rebuilds its handler every render
  // does not defeat the row memoization.
  const toggleRef = useRef(onToggle);
  toggleRef.current = onToggle;
  const toggle = useCallback((exe: string) => {
    setActiveExe(exe);
    toggleRef.current(exe);
  }, []);

  function onListKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (
      e.key !== "ArrowDown" &&
      e.key !== "ArrowUp" &&
      e.key !== "Home" &&
      e.key !== "End"
    ) {
      return;
    }
    if (flat.length === 0) {
      return;
    }
    e.preventDefault();
    const current = Math.max(
      0,
      flat.findIndex((app) => app.exe === rovingExe),
    );
    let next: number;
    if (e.key === "ArrowDown") {
      // Stops at the ends rather than wrapping: on a list this long, jumping
      // from the last row back to the first reads as a scroll glitch.
      next = Math.min(flat.length - 1, current + 1);
    } else if (e.key === "ArrowUp") {
      next = Math.max(0, current - 1);
    } else if (e.key === "Home") {
      next = 0;
    } else {
      next = flat.length - 1;
    }
    const target = flat[next];
    setActiveExe(target.exe);
    rowRefs.current.get(target.exe)?.focus();
  }

  function onDialogKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key !== "Tab") {
      return;
    }
    const root = dialogRef.current;
    if (!root) {
      return;
    }
    const stops = [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
      (el) => el.tabIndex >= 0,
    );
    if (stops.length === 0) {
      return;
    }
    const first = stops[0];
    const last = stops[stops.length - 1];
    const active = document.activeElement;
    if (e.shiftKey && active === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }

  // A rescan keeps the previous rows on screen: blanking the list would throw
  // away the row under the pointer and the user's place in it. Only a first scan
  // with nothing to show reads as "loading".
  const showLoading = loading && flat.length === 0 && total === 0;
  const showEmpty = !loading && !error && total === 0;
  const showNoMatches = total > 0 && flat.length === 0;

  function renderGroup(rows: AppEntry[], id: string, label: string) {
    if (rows.length === 0) {
      return null;
    }
    return (
      <div className="app-picker-group" role="group" aria-labelledby={id}>
        <h3 id={id} className="app-picker-group-head">
          {label} <span className="app-picker-group-n">{rows.length}</span>
        </h3>
        <ul className="app-picker-list">
          {rows.map((app) => (
            <AppRow
              key={app.exe.toLowerCase()}
              app={app}
              checked={selectedSet.has(app.exe.toLowerCase())}
              roving={app.exe === rovingExe}
              onToggle={toggle}
              register={register}
            />
          ))}
        </ul>
      </div>
    );
  }

  return (
    <div
      className="prof-modal-scrim"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={dialogRef}
        className="prof-modal app-picker"
        role="dialog"
        aria-modal="true"
        aria-labelledby={TITLE_ID}
        aria-describedby={HINT_ID}
        onKeyDown={onDialogKeyDown}
      >
        <div className="app-picker-head">
          <h2 id={TITLE_ID} className="prof-modal-title">
            {strings.title}
          </h2>
          <p id={HINT_ID} className="app-picker-hint">
            {strings.hint}
          </p>
        </div>

        <div className="set-field app-picker-search">
          <span className="set-prompt" aria-hidden="true">
            /
          </span>
          <input
            ref={searchRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={strings.searchPlaceholder}
            aria-label={strings.searchLabel}
            autoComplete="off"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
          />
        </div>

        <div className="app-picker-body" aria-busy={loading} onKeyDown={onListKeyDown}>
          {error && (
            <p className="app-picker-error" role="alert">
              {error}
            </p>
          )}
          {showLoading && (
            <p className="app-picker-note" role="status">
              {strings.loading}
            </p>
          )}
          {showEmpty && <p className="app-picker-note">{strings.empty}</p>}
          {showNoMatches && <p className="app-picker-note">{strings.noMatches}</p>}
          {renderGroup(runningRows, RUNNING_ID, strings.running)}
          {renderGroup(installedRows, INSTALLED_ID, strings.installed)}
        </div>

        <div className="prof-modal-actions app-picker-actions">
          <span className="app-picker-count" aria-live="polite">
            {strings.selectedCount.replace("{n}", String(selected.length))}
          </span>
          <button
            type="button"
            className="prof-ghost"
            onClick={onRescan}
            disabled={loading}
          >
            {loading ? strings.scanning : strings.rescan}
          </button>
          <button type="button" className="prof-ghost" onClick={onClose}>
            {strings.close}
          </button>
        </div>
      </div>
    </div>
  );
}
