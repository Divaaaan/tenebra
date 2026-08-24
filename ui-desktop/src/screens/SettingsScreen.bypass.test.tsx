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
  return renderWithProviders(<SettingsScreen tenebra={tenebra} />);
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
});
