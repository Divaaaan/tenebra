import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { getVersion } from "@tauri-apps/api/app";
import { isEnabled } from "@tauri-apps/plugin-autostart";
import { invoke } from "@tauri-apps/api/core";

import type { State } from "../api";
import { api } from "../api";
import { makeTenebra } from "../test/fixtures";
import { renderWithProviders } from "../test/renderWithProviders";
import { SettingsScreen } from "./SettingsScreen";

// The screen touches these platform plugins on mount; none of them are what this
// file is about.
vi.mock("@tauri-apps/plugin-autostart", () => ({
  enable: vi.fn(),
  disable: vi.fn(),
  isEnabled: vi.fn().mockResolvedValue(false),
}));
vi.mock("@tauri-apps/api/app", () => ({
  getVersion: vi.fn().mockResolvedValue("0.4.6"),
}));
vi.mock("@tauri-apps/plugin-updater", () => ({ check: vi.fn() }));
vi.mock("@tauri-apps/plugin-process", () => ({ relaunch: vi.fn() }));
vi.mock("@tauri-apps/api/core", () => ({ invoke: vi.fn() }));

// The app-update row carries a button with the same word, so the bypass one is
// found through its own row rather than by name alone.
function clickBypassUpdate() {
  const row = screen
    .getByText(/Сборка обхода|Bypass bundle/)
    .closest(".set-row");
  if (!row) throw new Error("bypass row not found");
  const btn = row.querySelector("button");
  if (!btn) throw new Error("bypass update button not found");
  fireEvent.click(btn);
}

function render(state: Partial<State> = {}) {
  const tenebra = makeTenebra({
    state: { state: "idle", routing: "smart", ...state } as State,
  });
  return { ...renderWithProviders(<SettingsScreen tenebra={tenebra} />), tenebra };
}

/** The row carrying the bypass on/off switch and its status line. */
function bypassRow(): HTMLElement {
  const row = screen
    .getByText(/Packet-level bypass|Обход на уровне пакетов/)
    .closest(".set-row");
  if (!row) throw new Error("bypass status row not found");
  return row as HTMLElement;
}

describe("SettingsScreen — bypass bundle", () => {
  beforeEach(() => {
    localStorage.clear();
    // Not restoreAllMocks: that would strip the module-level platform stubs this
    // screen calls on mount, and the failure surfaces as an unrelated crash in a
    // version-reading effect.
    vi.clearAllMocks();
    vi.mocked(getVersion).mockResolvedValue("0.4.6");
    vi.mocked(isEnabled).mockResolvedValue(false);
    // The i18n provider pushes the language to the shell on mount; without a
    // resolved promise here that fire-and-forget call throws inside an effect.
    vi.mocked(invoke).mockResolvedValue(undefined);
  });

  // A stale bundle fails exactly like a dead node or an expired subscription, so
  // the installed version is the one fact that tells them apart. It has to be on
  // screen, not in a log.
  it("shows the installed bundle version", () => {
    render({ zapret_version: "1.10.1" });
    expect(screen.getByText(/1\.10\.1/)).toBeInTheDocument();
  });

  it("says so when no bundle is installed yet", () => {
    render({});
    expect(
      screen.getByText(/ещё не установлена|not installed yet/),
    ).toBeInTheDocument();
  });

  // "Updated" and "already current" are both successes and mean different things:
  // the second one tells the user the bypass is not what is broken, which is the
  // reason they opened this screen.
  it("reports an update with both versions", async () => {
    vi.spyOn(api, "updateZapret").mockResolvedValue({
      installed: "1.9.9",
      latest: "1.10.1",
      updated: true,
      blocked: false,
    });
    render({ zapret_version: "1.9.9" });

    clickBypassUpdate();

    await waitFor(() =>
      expect(screen.getByText(/1\.9\.9 → 1\.10\.1/)).toBeInTheDocument(),
    );
  });

  it("distinguishes 'already current' from an update", async () => {
    vi.spyOn(api, "updateZapret").mockResolvedValue({
      installed: "1.10.1",
      latest: "1.10.1",
      updated: false,
      blocked: false,
    });
    render({ zapret_version: "1.10.1" });

    clickBypassUpdate();

    await waitFor(() =>
      expect(
        screen.getByText(/уже свежая|already current/),
      ).toBeInTheDocument(),
    );
  });

  // A newer bundle exists but its checksum is not pinned into this client, so the
  // core reports it without installing it. This must read as "update Tenebra",
  // naming the version — not as "already current" (there IS a newer one) and not
  // as a red failure (nothing is wrong, and retrying will not change it).
  it("says to update Tenebra when a newer bundle is not trusted yet", async () => {
    vi.spyOn(api, "updateZapret").mockResolvedValue({
      installed: "1.10.1",
      latest: "1.11.0",
      updated: false,
      blocked: true,
    });
    render({ zapret_version: "1.10.1" });

    clickBypassUpdate();

    await waitFor(() => {
      const row = screen
        .getByText(/Сборка обхода|Bypass bundle/)
        .closest(".set-row");
      const text = row?.textContent ?? "";
      // Names the available version and points at updating Tenebra...
      expect(text).toMatch(/1\.11\.0/);
      expect(text).toMatch(/обнови Tenebra|update Tenebra/i);
      // ...and does not mislead as "already current".
      expect(text).not.toMatch(/уже свежая|already current/);
    });
  });

  // A failed check must not read as success. The user is here because something
  // is broken; a silent no-op is the worst possible answer.
  it("surfaces a failed update", async () => {
    vi.spyOn(api, "updateZapret").mockRejectedValue(
      new Error("feed unreachable"),
    );
    render({ zapret_version: "1.10.1" });

    clickBypassUpdate();

    // The screen speaks the core's errors through describeCoreError rather than
    // pasting raw text, so the assertion is that the failure is *said* — not that
    // a particular English sentence survived translation.
    await waitFor(() => {
      const row = screen
        .getByText(/Сборка обхода|Bypass bundle/)
        .closest(".set-row");
      expect(row?.textContent ?? "").toMatch(
        /refused|didn't apply|не применилась|отказал/i,
      );
    });
  });

  it("toggles automatic updates through the core", async () => {
    const spy = vi.spyOn(api, "setZapretAutoUpdate").mockResolvedValue();
    render({ zapret_auto_update: true });

    // A switch here is labelled by its own ON/OFF text, so it is found through
    // its row — the shape the other toggle tests in this screen use.
    const row = screen
      .getByText(/Обновлять сборку|Update the bundle/)
      .closest(".set-row");
    if (!row) throw new Error("auto-update row not found");
    const toggle = row.querySelector('[role="switch"]');
    if (!toggle) throw new Error("auto-update switch not found");

    fireEvent.click(toggle as HTMLElement);

    await waitFor(() => expect(spy).toHaveBeenCalledWith(false));
  });

  // Everything below used to live in a panel on the main screen. The panel was
  // the only way to reach start_zapret / stop_zapret / pick_zapret at all, so it
  // could not simply be deleted — these pin that the commands are still reachable
  // and still driven by what the core reports rather than by a local flag.
  describe("controls", () => {
    it("reports the running strategy from the snapshot", () => {
      render({ zapret_active: true, zapret_strategy: "general (FAKE TLS AUTO)" });

      const row = bypassRow();
      expect(row.textContent).toMatch(/running|работает/);
      expect(row.textContent).toContain("general (FAKE TLS AUTO)");
      expect(row.querySelector('[role="switch"]')).toHaveAttribute(
        "aria-checked",
        "true",
      );
    });

    it("shows a bundle that is installed and idle as not running", () => {
      render({
        zapret_active: false,
        zapret_strategy: "general (FAKE TLS AUTO)",
        zapret_version: "1.10.2",
      });

      const row = bypassRow();
      expect(row.textContent).toMatch(/not running|не работает/);
      expect(row.querySelector('[role="switch"]')).toHaveAttribute(
        "aria-checked",
        "false",
      );
    });

    it("switches the bypass on and re-reads the core's state", async () => {
      const start = vi
        .spyOn(api, "startZapret")
        .mockResolvedValue({ active: "general" });
      const { tenebra } = render({ zapret_active: false });

      fireEvent.click(bypassRow().querySelector('[role="switch"]')!);

      await waitFor(() => expect(start).toHaveBeenCalledTimes(1));
      // The switch is drawn from the snapshot, so it only moves once the core has
      // been asked again — otherwise it would show where it was clicked.
      await waitFor(() => expect(tenebra.refreshStatus).toHaveBeenCalled());
    });

    it("switches it off through the core", async () => {
      const stop = vi.spyOn(api, "stopZapret").mockResolvedValue();
      const { tenebra } = render({
        zapret_active: true,
        zapret_strategy: "general",
      });

      fireEvent.click(bypassRow().querySelector('[role="switch"]')!);

      await waitFor(() => expect(stop).toHaveBeenCalledTimes(1));
      await waitFor(() => expect(tenebra.refreshStatus).toHaveBeenCalled());
    });

    it("says so when the core refuses to start the bypass", async () => {
      vi.spyOn(api, "startZapret").mockRejectedValue(
        new Error("start_zapret: winws requires administrator rights"),
      );
      const { tenebra } = render({ zapret_active: false });

      fireEvent.click(bypassRow().querySelector('[role="switch"]')!);

      // A refusal is reported, and the state is re-read rather than assumed: the
      // switch must end up where the core is, not where the click aimed.
      await waitFor(() => expect(tenebra.refreshStatus).toHaveBeenCalled());
      expect(bypassRow().querySelector('[role="switch"]')).toHaveAttribute(
        "aria-checked",
        "false",
      );
    });

    it("re-measures the strategies on request", async () => {
      const pick = vi.spyOn(api, "pickZapret").mockResolvedValue({
        baseline: 1,
        targets: 5,
        best: "general (ALT)",
        improved: true,
        results: null,
      });
      const { tenebra } = render({ zapret_active: true });

      const row = screen
        .getByText(/Strategy choice|Подбор стратегии/)
        .closest(".set-row");
      fireEvent.click(row!.querySelector("button")!);

      await waitFor(() => expect(pick).toHaveBeenCalledTimes(1));
      await waitFor(() => expect(tenebra.refreshStatus).toHaveBeenCalled());
    });

    it("does not treat 'nothing helped' as a failed probe", async () => {
      // Every strategy measured, none beat the baseline. That is an answer — the
      // block is elsewhere — and it must not read as an error, or the user goes
      // on suspecting the bypass.
      vi.spyOn(api, "pickZapret").mockResolvedValue({
        baseline: 5,
        targets: 5,
        improved: false,
        results: null,
      });
      const { tenebra } = render({ zapret_active: true });

      const row = screen
        .getByText(/Strategy choice|Подбор стратегии/)
        .closest(".set-row");
      fireEvent.click(row!.querySelector("button")!);

      await waitFor(() => expect(tenebra.refreshStatus).toHaveBeenCalled());
      expect(
        screen.queryByText(/refused|didn't apply|не применилась|отказал/i),
      ).toBeNull();
    });
  });
});
