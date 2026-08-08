import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import {
  AppPicker,
  type AppEntry,
  type AppPickerStrings,
} from "./AppPicker";

// English copy, shaped exactly like the strings the settings screen will hand
// down. The component holds none of its own, so the fixture is the only source
// of text in these assertions.
const STRINGS: AppPickerStrings = {
  title: "Choose apps",
  hint: "Rules match the executable name shown under each app.",
  searchPlaceholder: "search apps",
  searchLabel: "Search apps",
  running: "Running now",
  installed: "Installed",
  loading: "Scanning apps…",
  empty: "No apps found on this machine.",
  noMatches: "Nothing matches that search.",
  rescan: "Rescan",
  scanning: "Scanning…",
  close: "Done",
  selectedCount: "{n} selected",
};

// Invented names, deliberately unlike each other: "studio" appears only in a
// display name and "tiles" only in an executable, so a search assertion can tell
// the two match paths apart.
function makeApps(): AppEntry[] {
  return [
    {
      name: "Nightshade Browser",
      exe: "nightshade.exe",
      path: "C:\\Program Files\\Nightshade\\nightshade.exe",
      icon: "data:image/png;base64,iVBORw0KGgo=",
      running: true,
      source: "process",
    },
    {
      name: "Ledger Desk",
      exe: "ledger.exe",
      path: "C:\\Apps\\Ledger\\ledger.exe",
      icon: null,
      running: false,
      source: "registry",
    },
    {
      name: "Tile Studio",
      exe: "tiles.exe",
      path: null,
      icon: null,
      running: false,
      source: "startmenu",
    },
  ];
}

interface Overrides {
  apps?: AppEntry[];
  selected?: string[];
  loading?: boolean;
  error?: string;
  onToggle?: (exe: string) => void;
  onClose?: () => void;
  onRescan?: () => void;
}

function renderPicker(overrides: Overrides = {}) {
  const props = {
    apps: overrides.apps ?? makeApps(),
    selected: overrides.selected ?? [],
    loading: overrides.loading ?? false,
    error: overrides.error,
    onToggle: overrides.onToggle ?? vi.fn(),
    onClose: overrides.onClose ?? vi.fn(),
    onRescan: overrides.onRescan ?? vi.fn(),
    strings: STRINGS,
  };
  return { ...render(<AppPicker {...props} />), props };
}

describe("AppPicker", () => {
  it("shows the exact executable next to every display name", () => {
    renderPicker();

    // The rule matches the executable, so the name a user recognises is never
    // allowed to stand alone — the file that goes into the rule is on screen.
    expect(screen.getByText("Nightshade Browser")).toBeInTheDocument();
    expect(screen.getByText("nightshade.exe")).toBeInTheDocument();
    expect(screen.getByText("Ledger Desk")).toBeInTheDocument();
    expect(screen.getByText("ledger.exe")).toBeInTheDocument();
    expect(screen.getByText("Tile Studio")).toBeInTheDocument();
    expect(screen.getByText("tiles.exe")).toBeInTheDocument();
  });

  it("separates what is running now from the rest of the installed apps", () => {
    renderPicker();

    const runningGroup = screen.getByRole("group", { name: /running now/i });
    expect(
      within(runningGroup).getByRole("checkbox", { name: /nightshade\.exe/ }),
    ).toBeInTheDocument();
    expect(
      within(runningGroup).queryByRole("checkbox", { name: /ledger\.exe/ }),
    ).toBeNull();

    const installedGroup = screen.getByRole("group", { name: /installed/i });
    expect(
      within(installedGroup).getByRole("checkbox", { name: /ledger\.exe/ }),
    ).toBeInTheDocument();
    expect(
      within(installedGroup).getByRole("checkbox", { name: /tiles\.exe/ }),
    ).toBeInTheDocument();
  });

  it("hides a group that has nothing in it", () => {
    renderPicker({ apps: makeApps().map((a) => ({ ...a, running: false })) });

    expect(screen.queryByRole("group", { name: /running now/i })).toBeNull();
    expect(screen.getByRole("group", { name: /installed/i })).toBeInTheDocument();
  });

  it("filters on the display name", async () => {
    const user = userEvent.setup();
    renderPicker();

    await user.type(screen.getByLabelText(STRINGS.searchLabel), "studio");

    expect(screen.getByText("tiles.exe")).toBeInTheDocument();
    expect(screen.queryByText("ledger.exe")).toBeNull();
    expect(screen.queryByText("nightshade.exe")).toBeNull();
  });

  it("filters on the executable name too", async () => {
    const user = userEvent.setup();
    renderPicker();

    // "tiles" is nowhere in "Tile Studio" — only a match against the executable
    // can keep this row on screen.
    await user.type(screen.getByLabelText(STRINGS.searchLabel), "tiles");

    expect(screen.getByText("Tile Studio")).toBeInTheDocument();
    expect(screen.queryByText("Ledger Desk")).toBeNull();
  });

  it("says so when the search matches nothing, without claiming the scan was empty", async () => {
    const user = userEvent.setup();
    renderPicker();

    await user.type(screen.getByLabelText(STRINGS.searchLabel), "zzzz");

    expect(screen.getByText(STRINGS.noMatches)).toBeInTheDocument();
    expect(screen.queryByText(STRINGS.empty)).toBeNull();
  });

  it("reports the executable, not the display name, when a row is ticked", async () => {
    const user = userEvent.setup();
    const onToggle = vi.fn();
    renderPicker({ onToggle });

    await user.click(screen.getByRole("checkbox", { name: /Tile Studio/ }));

    expect(onToggle).toHaveBeenCalledTimes(1);
    expect(onToggle).toHaveBeenCalledWith("tiles.exe");
  });

  it("marks the rows already in the rule, matching the core's case-insensitive names", () => {
    renderPicker({ selected: ["LEDGER.EXE"] });

    expect(screen.getByRole("checkbox", { name: /ledger\.exe/ })).toBeChecked();
    expect(
      screen.getByRole("checkbox", { name: /tiles\.exe/ }),
    ).not.toBeChecked();
  });

  it("counts what the rule holds, including names no scan turned up", () => {
    renderPicker({ selected: ["ledger.exe", "gone.exe"] });

    expect(screen.getByText("2 selected")).toBeInTheDocument();
  });

  it("announces the scan while it runs and holds the rescan button", () => {
    renderPicker({ apps: [], loading: true });

    expect(screen.getByText(STRINGS.loading)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: STRINGS.scanning }),
    ).toBeDisabled();
  });

  it("keeps the previous list on screen while a rescan runs", () => {
    renderPicker({ loading: true });

    // Blanking the list mid-rescan would throw the row under the cursor away.
    expect(screen.getByText("ledger.exe")).toBeInTheDocument();
    expect(screen.queryByText(STRINGS.loading)).toBeNull();
  });

  it("names an empty result rather than showing a blank panel", () => {
    renderPicker({ apps: [] });

    expect(screen.getByText(STRINGS.empty)).toBeInTheDocument();
  });

  it("surfaces a scan failure and still offers a retry", async () => {
    const user = userEvent.setup();
    const onRescan = vi.fn();
    renderPicker({ apps: [], error: "scan refused", onRescan });

    expect(screen.getByRole("alert")).toHaveTextContent("scan refused");
    // The failure replaces the empty-state copy; claiming "no apps found" after
    // a failed scan would be a lie.
    expect(screen.queryByText(STRINGS.empty)).toBeNull();

    await user.click(screen.getByRole("button", { name: STRINGS.rescan }));
    expect(onRescan).toHaveBeenCalledTimes(1);
  });

  it("closes on the close button", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderPicker({ onClose });

    await user.click(screen.getByRole("button", { name: STRINGS.close }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("closes on Escape", () => {
    const onClose = vi.fn();
    renderPicker({ onClose });

    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("closes when the scrim behind the card is clicked", () => {
    const onClose = vi.fn();
    const { container } = renderPicker({ onClose });

    const scrim = container.querySelector(".prof-modal-scrim");
    expect(scrim).not.toBeNull();
    fireEvent.mouseDown(scrim as Element);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("opens as a modal dialog labelled by its own title", () => {
    renderPicker();

    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(within(dialog).getByText(STRINGS.title)).toBeInTheDocument();
  });

  it("puts the caret in the search field on open", () => {
    renderPicker();

    expect(screen.getByLabelText(STRINGS.searchLabel)).toHaveFocus();
  });

  it("keeps Tab inside the dialog", async () => {
    const user = userEvent.setup();
    renderPicker();

    const search = screen.getByLabelText(STRINGS.searchLabel);
    const close = screen.getByRole("button", { name: STRINGS.close });

    close.focus();
    await user.tab();
    expect(search).toHaveFocus();

    await user.tab({ shift: true });
    expect(close).toHaveFocus();
  });

  it("walks the list with arrow keys from a single tab stop", async () => {
    const user = userEvent.setup();
    renderPicker();

    const first = screen.getByRole("checkbox", { name: /nightshade\.exe/ });
    const second = screen.getByRole("checkbox", { name: /ledger\.exe/ });
    const last = screen.getByRole("checkbox", { name: /tiles\.exe/ });

    // One stop for the whole list: only the row the roving focus sits on is
    // reachable by Tab, the rest by arrows.
    expect(first).toHaveAttribute("tabindex", "0");
    expect(second).toHaveAttribute("tabindex", "-1");

    first.focus();
    await user.keyboard("{ArrowDown}");
    expect(second).toHaveFocus();

    await user.keyboard("{End}");
    expect(last).toHaveFocus();

    await user.keyboard("{Home}");
    expect(first).toHaveFocus();
  });

  it("ticks the focused row from the keyboard", async () => {
    const user = userEvent.setup();
    const onToggle = vi.fn();
    renderPicker({ onToggle });

    screen.getByRole("checkbox", { name: /nightshade\.exe/ }).focus();
    await user.keyboard("{ArrowDown}");
    await user.keyboard(" ");

    expect(onToggle).toHaveBeenCalledWith("ledger.exe");
  });

  it("hands focus back to whatever opened it", () => {
    const opener = document.createElement("button");
    document.body.appendChild(opener);
    opener.focus();

    const { unmount } = renderPicker();
    expect(opener).not.toHaveFocus();

    unmount();
    expect(opener).toHaveFocus();
    opener.remove();
  });

  it("renders a row whose icon is missing without losing its text", () => {
    const { container } = renderPicker();

    const withIcon = container.querySelector("img.app-picker-icon");
    expect(withIcon).toHaveAttribute("src", "data:image/png;base64,iVBORw0KGgo=");
    // An icon-less entry is normal, not an error: the row keeps its slot so the
    // columns stay aligned.
    expect(container.querySelectorAll(".app-picker-icon--blank")).toHaveLength(2);
    expect(
      screen.getByRole("checkbox", { name: /ledger\.exe/ }),
    ).toHaveTextContent("Ledger Desk");
  });

  it("renders one row per executable when a stale list repeats one", () => {
    const apps = makeApps();
    renderPicker({
      apps: [
        ...apps,
        { ...apps[1], running: true, source: "process" },
      ],
    });

    expect(
      screen.getAllByRole("checkbox", { name: /ledger\.exe/ }),
    ).toHaveLength(1);
    // The live process wins the grouping: a running duplicate promotes the row.
    const runningGroup = screen.getByRole("group", { name: /running now/i });
    expect(
      within(runningGroup).getByRole("checkbox", { name: /ledger\.exe/ }),
    ).toBeInTheDocument();
  });

  it("keeps row order stable when a row is ticked", async () => {
    const user = userEvent.setup();
    const onToggle = vi.fn();
    const { rerender } = renderPicker({ onToggle });

    const order = () =>
      screen
        .getAllByRole("checkbox")
        .map((el) => el.getAttribute("data-exe"))
        .join(",");
    const before = order();

    await user.click(screen.getByRole("checkbox", { name: /tiles\.exe/ }));
    rerender(
      <AppPicker
        apps={makeApps()}
        selected={["tiles.exe"]}
        loading={false}
        onToggle={onToggle}
        onClose={vi.fn()}
        onRescan={vi.fn()}
        strings={STRINGS}
      />,
    );

    // Selection must not re-sort the list — the row under the cursor has to stay
    // where it was.
    expect(order()).toBe(before);
  });
});
