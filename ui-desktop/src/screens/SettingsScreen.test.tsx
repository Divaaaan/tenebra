import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { SettingsScreen } from "./SettingsScreen";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeTenebra } from "../test/fixtures";
import type { State } from "../api";
import { disable, enable, isEnabled } from "@tauri-apps/plugin-autostart";
import { getVersion } from "@tauri-apps/api/app";

// The screen reads the OS autostart registration on mount; stub it so no real
// platform call happens and the toggle starts off.
vi.mock("@tauri-apps/plugin-autostart", () => ({
  enable: vi.fn(),
  disable: vi.fn(),
  isEnabled: vi.fn(),
}));

// The updates row reads the app version on mount and calls the updater/process
// plugins on click; stub all three so the screen mounts without a Tauri host.
vi.mock("@tauri-apps/api/app", () => ({
  getVersion: vi.fn().mockResolvedValue("0.1.0"),
}));
vi.mock("@tauri-apps/plugin-updater", () => ({
  check: vi.fn(),
}));
vi.mock("@tauri-apps/plugin-process", () => ({
  relaunch: vi.fn(),
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
    // The updates row reads the app version on mount; restoreMocks wipes the
    // factory impl between tests, so re-arm it here like the autostart stubs.
    vi.mocked(getVersion).mockResolvedValue("0.1.0");
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

  describe("tunnel stack", () => {
    it("marks the reported stack as checked, defaulting to system", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(screen.getByRole("radio", { name: /^System/ })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      expect(screen.getByRole("radio", { name: /^gVisor/ })).toHaveAttribute(
        "aria-checked",
        "false",
      );
    });

    it("reflects a non-default stack from the core state", () => {
      const tenebra = makeTenebra({
        state: { state: "idle", tun_stack: "mixed" } as State,
      });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(screen.getByRole("radio", { name: /^Mixed/ })).toHaveAttribute(
        "aria-checked",
        "true",
      );
    });

    it("requests the new stack on click and skips a no-op", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(screen.getByRole("radio", { name: /^gVisor/ }));
      expect(tenebra.setTun).toHaveBeenCalledWith("gvisor");

      // Clicking the already-active stack must not re-apply (a live tunnel
      // would be pointlessly hot-swapped).
      await user.click(screen.getByRole("radio", { name: /^System/ }));
      expect(tenebra.setTun).toHaveBeenCalledTimes(1);
    });

    it("moves to the next stack on ArrowDown", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      fireEvent.keyDown(screen.getByRole("radio", { name: /^System/ }), {
        key: "ArrowDown",
      });
      expect(tenebra.setTun).toHaveBeenCalledWith("gvisor");
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

    it("persists the theme and reflects it in the segment control", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const light = screen.getByRole("button", { name: "Light" });
      const dark = screen.getByRole("button", { name: "Dark" });
      expect(dark).toHaveAttribute("aria-pressed", "true");

      await user.click(light);
      expect(light).toHaveAttribute("aria-pressed", "true");
      expect(dark).toHaveAttribute("aria-pressed", "false");
      // Written through to localStorage so the next launch (which reads it
      // before first paint) comes up light.
      expect(localStorage.getItem("tenebra.theme")).toBe("light");
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
