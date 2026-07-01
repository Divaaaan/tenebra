import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { SettingsScreen } from "./SettingsScreen";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeTenebra } from "../test/fixtures";
import type { State } from "../api";
import { disable, enable, isEnabled } from "@tauri-apps/plugin-autostart";

// The screen reads the OS autostart registration on mount; stub it so no real
// platform call happens and the toggle starts off.
vi.mock("@tauri-apps/plugin-autostart", () => ({
  enable: vi.fn(),
  disable: vi.fn(),
  isEnabled: vi.fn(),
}));

describe("SettingsScreen", () => {
  beforeEach(() => {
    // Renderer-owned settings live in localStorage; start each test clean.
    localStorage.clear();
    // restoreMocks wipes implementations between tests, so re-arm the autostart
    // stubs: the mount-time isEnabled() must resolve, the toggles must no-op.
    vi.mocked(isEnabled).mockResolvedValue(false);
    vi.mocked(enable).mockResolvedValue();
    vi.mocked(disable).mockResolvedValue();
  });

  describe("routing", () => {
    it("marks the active routing mode as checked", () => {
      const tenebra = makeTenebra({
        state: { state: "idle", routing: "smart" } as State,
      });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(screen.getByRole("radio", { name: /Smart/ })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      expect(screen.getByRole("radio", { name: /Global/ })).toHaveAttribute(
        "aria-checked",
        "false",
      );
    });

    it("sets routing on click", async () => {
      const tenebra = makeTenebra({
        state: { state: "idle", routing: "smart" } as State,
      });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(screen.getByRole("radio", { name: /Global/ }));
      expect(tenebra.setRouting).toHaveBeenCalledWith("global");
    });

    it("moves to the next mode on ArrowDown", () => {
      const tenebra = makeTenebra({
        state: { state: "idle", routing: "smart" } as State,
      });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      fireEvent.keyDown(screen.getByRole("radio", { name: /Smart/ }), {
        key: "ArrowDown",
      });
      // smart is index 0; ArrowDown advances to global.
      expect(tenebra.setRouting).toHaveBeenCalledWith("global");
    });
  });

  describe("split tunnelling", () => {
    it("hides the apps editor while off", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      // The "Apps" list/input only exists once a split mode is on.
      expect(screen.queryByLabelText("Apps")).not.toBeInTheDocument();
    });

    it("switches mode on click", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(screen.getByRole("radio", { name: /Exclude apps/ }));
      // Switching mode keeps the (empty) app list.
      expect(tenebra.setSplit).toHaveBeenCalledWith("exclude", []);
    });

    it("lists configured apps and removes one", async () => {
      const tenebra = makeTenebra({
        state: {
          state: "idle",
          split: "exclude",
          split_apps: ["chrome.exe"],
        } as State,
      });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(screen.getByText("chrome.exe")).toBeInTheDocument();
      await user.click(
        screen.getByRole("button", { name: "Remove chrome.exe" }),
      );
      // Removing filters the one entry out, leaving an empty list.
      expect(tenebra.setSplit).toHaveBeenCalledWith("exclude", []);
    });

    it("normalizes and appends an added app", async () => {
      const tenebra = makeTenebra({
        state: {
          state: "idle",
          split: "exclude",
          split_apps: ["chrome.exe"],
        } as State,
      });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const addButton = screen.getByRole("button", { name: "Add" });
      // Nothing typed yet, so adding is disabled.
      expect(addButton).toBeDisabled();

      await user.type(screen.getByLabelText("Apps"), "Firefox.exe");
      await user.click(addButton);
      // The draft is trimmed and lowercased before being appended.
      expect(tenebra.setSplit).toHaveBeenCalledWith("exclude", [
        "chrome.exe",
        "firefox.exe",
      ]);
    });
  });

  describe("startup", () => {
    // The switch's accessible name is its own text ("ON"/"OFF"), not the sibling
    // label, so locate the row by its label text and grab the switch within it.
    function fastestToggle(): HTMLElement {
      const label = screen.getByText("Auto-select fastest node");
      const row = label.closest(".set-row");
      if (!row) {
        throw new Error("auto-fastest row not found");
      }
      const toggle = row.querySelector('[role="switch"]');
      if (!toggle) {
        throw new Error("auto-fastest switch not found");
      }
      return toggle as HTMLElement;
    }

    it("auto-select-fastest reflects and persists the stored preference", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const toggle = fastestToggle();
      // Starts off (localStorage cleared in beforeEach).
      expect(toggle).toHaveAttribute("aria-checked", "false");

      await user.click(toggle);
      expect(toggle).toHaveAttribute("aria-checked", "true");
      // The choice is written to localStorage so the connect flow can read it.
      expect(localStorage.getItem("tenebra.autoFastest")).toBe("1");

      await user.click(toggle);
      expect(toggle).toHaveAttribute("aria-checked", "false");
      expect(localStorage.getItem("tenebra.autoFastest")).toBe("0");
    });

    it("auto-select-fastest starts on when previously enabled", () => {
      localStorage.setItem("tenebra.autoFastest", "1");
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(fastestToggle()).toHaveAttribute("aria-checked", "true");
    });
  });

  describe("appearance", () => {
    it("applies the chosen theme to the document", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(screen.getByRole("button", { name: "Light" }));
      expect(document.documentElement.dataset.theme).toBe("light");
    });

    it("switches the UI language", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      // Starts in English (renderWithProviders pins "en").
      expect(
        screen.getByRole("heading", { name: "Settings" }),
      ).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "RU" }));
      expect(
        screen.getByRole("heading", { name: "Настройки" }),
      ).toBeInTheDocument();
    });
  });
});
