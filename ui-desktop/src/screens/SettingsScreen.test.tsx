import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { SettingsScreen } from "./SettingsScreen";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeTenebra } from "../test/fixtures";
import type { State } from "../api";
import { disable, enable, isEnabled } from "@tauri-apps/plugin-autostart";
import { getVersion } from "@tauri-apps/api/app";
import { check, type Update } from "@tauri-apps/plugin-updater";
import { relaunch } from "@tauri-apps/plugin-process";

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

    it("carries focus with selection so repeated ArrowDown reaches every mode", async () => {
      const tenebra = makeTenebra({
        state: { state: "idle", routing: "smart" } as State,
      });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const smart = screen.getByRole("radio", { name: /Smart/ });
      const global = screen.getByRole("radio", { name: /Global/ });
      const direct = screen.getByRole("radio", { name: /Direct/ });

      // Start where the roving tabIndex parks the caret: on the checked option.
      smart.focus();
      expect(smart).toHaveFocus();

      // Each ArrowDown must move focus AND selection onward. The focus move is
      // what re-anchors the index; without it the second press would recompute
      // from smart and stall on global (the reported defect).
      await user.keyboard("{ArrowDown}");
      expect(global).toHaveFocus();
      expect(tenebra.setRouting).toHaveBeenLastCalledWith("global");

      await user.keyboard("{ArrowDown}");
      expect(direct).toHaveFocus();
      expect(tenebra.setRouting).toHaveBeenLastCalledWith("direct");

      // A third press wraps around to the first option.
      await user.keyboard("{ArrowDown}");
      expect(smart).toHaveFocus();
      expect(tenebra.setRouting).toHaveBeenLastCalledWith("smart");
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

    it("carries focus with selection so repeated ArrowDown reaches every mode", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const off = screen.getByRole("radio", { name: /^Off/ });
      const exclude = screen.getByRole("radio", { name: /Exclude apps/ });
      const include = screen.getByRole("radio", { name: /Only these apps/ });

      // off is index 0 (the default split mode).
      off.focus();
      expect(off).toHaveFocus();

      await user.keyboard("{ArrowDown}");
      expect(exclude).toHaveFocus();
      expect(tenebra.setSplit).toHaveBeenLastCalledWith("exclude", []);

      await user.keyboard("{ArrowDown}");
      expect(include).toHaveFocus();
      expect(tenebra.setSplit).toHaveBeenLastCalledWith("include", []);

      // Wraps back to off; re-selecting the already-active mode is a no-op in
      // the core, but focus must still travel so the group stays navigable.
      await user.keyboard("{ArrowDown}");
      expect(off).toHaveFocus();
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

    it("carries focus with selection so repeated ArrowDown reaches every stack", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const system = screen.getByRole("radio", { name: /^System/ });
      const gvisor = screen.getByRole("radio", { name: /^gVisor/ });
      const mixed = screen.getByRole("radio", { name: /^Mixed/ });

      // system is index 0 (the default stack).
      system.focus();
      expect(system).toHaveFocus();

      await user.keyboard("{ArrowDown}");
      expect(gvisor).toHaveFocus();
      expect(tenebra.setTun).toHaveBeenLastCalledWith("gvisor");

      await user.keyboard("{ArrowDown}");
      expect(mixed).toHaveFocus();
      expect(tenebra.setTun).toHaveBeenLastCalledWith("mixed");

      // Wraps back to system; re-selecting the active stack is a no-op in the
      // core, but focus must still travel so the group stays navigable.
      await user.keyboard("{ArrowDown}");
      expect(system).toHaveFocus();
    });
  });

  describe("dns", () => {
    const dnsState = {
      state: "idle",
      ad_block: true,
      dns_remote: "tls://1.1.1.1",
      dns_direct: "https://77.88.8.8/dns-query",
    } as State;

    // The ad-block switch's accessible name is its own ON/OFF text, so find the
    // row by its label and take the switch inside it (same shape as the startup
    // toggles).
    function adBlockToggle(): HTMLElement {
      const row = screen.getByText("Block ads and trackers").closest(".set-row");
      if (!row) {
        throw new Error("ad-block row not found");
      }
      const toggle = row.querySelector('[role="switch"]');
      if (!toggle) {
        throw new Error("ad-block switch not found");
      }
      return toggle as HTMLElement;
    }

    // The IPv4-only switch mirrors the ad-block one: found by its row label, then
    // the switch inside that row.
    function ipv4OnlyToggle(): HTMLElement {
      const row = screen.getByText("IPv4-only DNS").closest(".set-row");
      if (!row) {
        throw new Error("ipv4-only row not found");
      }
      const toggle = row.querySelector('[role="switch"]');
      if (!toggle) {
        throw new Error("ipv4-only switch not found");
      }
      return toggle as HTMLElement;
    }

    it("reflects the ad-block toggle from core state", () => {
      const tenebra = makeTenebra({ state: dnsState });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(adBlockToggle()).toHaveAttribute("aria-checked", "true");
    });

    it("ad-block starts off when the core omits the field", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(adBlockToggle()).toHaveAttribute("aria-checked", "false");
    });

    it("toggling ad-block sends the whole DNS triple, resolvers unchanged", async () => {
      const tenebra = makeTenebra({ state: dnsState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(adBlockToggle());
      // Flips ad-block off, carrying the current effective resolvers and the
      // IPv4-only toggle (off here) along.
      expect(tenebra.setDns).toHaveBeenCalledWith(
        false,
        "tls://1.1.1.1",
        "https://77.88.8.8/dns-query",
        false,
      );
    });

    it("reflects the IPv4-only toggle from core state", () => {
      const tenebra = makeTenebra({
        state: { ...dnsState, ipv4_only: true } as State,
      });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(ipv4OnlyToggle()).toHaveAttribute("aria-checked", "true");
    });

    it("IPv4-only starts off when the core omits the field", () => {
      const tenebra = makeTenebra({ state: dnsState });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(ipv4OnlyToggle()).toHaveAttribute("aria-checked", "false");
    });

    it("toggling IPv4-only sends the whole DNS set, ad-block and resolvers unchanged", async () => {
      const tenebra = makeTenebra({ state: dnsState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(ipv4OnlyToggle());
      // Flips IPv4-only on, carrying the current ad-block and resolvers along.
      expect(tenebra.setDns).toHaveBeenCalledWith(
        true,
        "tls://1.1.1.1",
        "https://77.88.8.8/dns-query",
        true,
      );
    });

    it("prefills the resolver inputs from the reported state", () => {
      const tenebra = makeTenebra({ state: dnsState });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(screen.getByLabelText("Encrypted resolver")).toHaveValue(
        "tls://1.1.1.1",
      );
      expect(screen.getByLabelText("Direct resolver")).toHaveValue(
        "https://77.88.8.8/dns-query",
      );
    });

    it("commits a valid resolver edit on blur", async () => {
      const tenebra = makeTenebra({ state: dnsState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const remote = screen.getByLabelText("Encrypted resolver");
      await user.clear(remote);
      await user.type(remote, "quic://dns.adguard.com");
      await user.tab(); // blur commits
      expect(tenebra.setDns).toHaveBeenCalledWith(
        true,
        "quic://dns.adguard.com",
        "https://77.88.8.8/dns-query",
        false,
      );
    });

    it("commits a valid resolver edit on Enter", async () => {
      const tenebra = makeTenebra({ state: dnsState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const direct = screen.getByLabelText("Direct resolver");
      await user.clear(direct);
      await user.type(direct, "tls://8.8.8.8:853{Enter}");
      expect(tenebra.setDns).toHaveBeenCalledWith(
        true,
        "tls://1.1.1.1",
        "tls://8.8.8.8:853",
        false,
      );
    });

    it("flags a malformed resolver and refuses to commit it", async () => {
      const tenebra = makeTenebra({ state: dnsState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const remote = screen.getByLabelText("Encrypted resolver");
      await user.clear(remote);
      await user.type(remote, "ftp://nope");
      expect(remote).toHaveAttribute("aria-invalid", "true");
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Enter a resolver like",
      );

      await user.tab(); // blur must not push garbage to the core
      expect(tenebra.setDns).not.toHaveBeenCalled();
    });

    it("clearing a resolver commits empty so the core restores its default", async () => {
      const tenebra = makeTenebra({ state: dnsState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const direct = screen.getByLabelText("Direct resolver");
      await user.clear(direct);
      await user.tab();
      // Empty is valid; the core substitutes the default and echoes it back.
      expect(tenebra.setDns).toHaveBeenCalledWith(
        true,
        "tls://1.1.1.1",
        "",
        false,
      );
    });
  });

  describe("custom rules", () => {
    const rulesState = {
      state: "idle",
      rules_direct: ["bank.example"],
      rules_proxy: ["work.example"],
      preset_ru_banking: true,
    } as State;

    // The preset switch's accessible name is its own ON/OFF text, so find the row
    // by its label and take the switch inside it (same shape as the ad-block row).
    function presetToggle(label: string): HTMLElement {
      const row = screen.getByText(label).closest(".set-row");
      if (!row) {
        throw new Error(`${label} row not found`);
      }
      const toggle = row.querySelector('[role="switch"]');
      if (!toggle) {
        throw new Error(`${label} switch not found`);
      }
      return toggle as HTMLElement;
    }

    it("reflects the RU presets from core state", () => {
      const tenebra = makeTenebra({ state: rulesState });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(
        presetToggle("Russian banking sites stay direct"),
      ).toHaveAttribute("aria-checked", "true");
      expect(
        presetToggle("Russian government sites stay direct"),
      ).toHaveAttribute("aria-checked", "false");
    });

    it("toggling a preset sends the whole rule set, lists and other preset unchanged", async () => {
      const tenebra = makeTenebra({ state: rulesState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(presetToggle("Russian government sites stay direct"));
      // Flips gov on, carrying the current lists and the banking preset (on).
      expect(tenebra.setRules).toHaveBeenCalledWith(
        ["bank.example"],
        ["work.example"],
        true,
        true,
      );
    });

    it("prefills and lists the configured domains", () => {
      const tenebra = makeTenebra({ state: rulesState });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(screen.getByText("bank.example")).toBeInTheDocument();
      expect(screen.getByText("work.example")).toBeInTheDocument();
    });

    it("adds a normalized domain to the direct list on Enter", async () => {
      const tenebra = makeTenebra({ state: rulesState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const input = screen.getByLabelText("Always direct");
      await user.type(input, "Sberbank.RU{Enter}");
      // Trimmed + lowercased, appended to the direct list; proxy + presets held.
      expect(tenebra.setRules).toHaveBeenCalledWith(
        ["bank.example", "sberbank.ru"],
        ["work.example"],
        true,
        false,
      );
    });

    it("adds to the proxy list independently of the direct list", async () => {
      const tenebra = makeTenebra({ state: rulesState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const input = screen.getByLabelText("Always through the tunnel");
      await user.type(input, "vpn.example{Enter}");
      expect(tenebra.setRules).toHaveBeenCalledWith(
        ["bank.example"],
        ["work.example", "vpn.example"],
        true,
        false,
      );
    });

    it("removes a domain from the direct list", async () => {
      const tenebra = makeTenebra({ state: rulesState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(
        screen.getByRole("button", { name: "Remove bank.example" }),
      );
      // Filters the one entry out, leaving an empty direct list; proxy unchanged.
      expect(tenebra.setRules).toHaveBeenCalledWith(
        [],
        ["work.example"],
        true,
        false,
      );
    });

    it("flags a malformed domain and refuses to add it", async () => {
      const tenebra = makeTenebra({ state: rulesState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const input = screen.getByLabelText("Always direct");
      await user.type(input, "https://nope");
      expect(input).toHaveAttribute("aria-invalid", "true");
      expect(screen.getByRole("alert")).toHaveTextContent("Enter a domain like");

      // Enter must not push an invalid domain to the core.
      await user.type(input, "{Enter}");
      expect(tenebra.setRules).not.toHaveBeenCalled();
    });
  });

  describe("startup", () => {
    // The switch's accessible name is its own text ("ON"/"OFF"), not the sibling
    // label, so locate the row by its label text and grab the switch within it.
    function toggleInRow(label: string): HTMLElement {
      const row = screen.getByText(label).closest(".set-row");
      if (!row) {
        throw new Error(`${label} row not found`);
      }
      const toggle = row.querySelector('[role="switch"]');
      if (!toggle) {
        throw new Error(`${label} switch not found`);
      }
      return toggle as HTMLElement;
    }

    function fastestToggle(): HTMLElement {
      return toggleInRow("Auto-select fastest node");
    }

    // Autoconnect is core-owned: the toggle mirrors State.autoconnect and asks
    // the core to change it, instead of a renderer-side localStorage flag.
    function autoconnectToggle(): HTMLElement {
      return toggleInRow("Connect on launch");
    }

    it("autoconnect reflects the core-reported state", () => {
      const tenebra = makeTenebra({
        state: { state: "idle", autoconnect: true } as State,
      });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(autoconnectToggle()).toHaveAttribute("aria-checked", "true");
    });

    it("autoconnect starts off when the core omits the field", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(autoconnectToggle()).toHaveAttribute("aria-checked", "false");
    });

    it("autoconnect toggles through the core, not localStorage", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(autoconnectToggle());
      expect(tenebra.setAutoconnect).toHaveBeenCalledWith(true);
      // The choice lives in the core's settings now; nothing is written here.
      expect(localStorage.getItem("tenebra.autoconnect")).toBeNull();
    });

    it("autoconnect disarms from the reported on state", async () => {
      const tenebra = makeTenebra({
        state: { state: "idle", autoconnect: true } as State,
      });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(autoconnectToggle());
      expect(tenebra.setAutoconnect).toHaveBeenCalledWith(false);
    });

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

  describe("updates", () => {
    // Same shape as fastestToggle above: the switch's accessible name is its
    // own ON/OFF text, so find the row by label and take the switch inside it.
    function autoInstallToggle(): HTMLElement {
      const label = screen.getByText("Install updates automatically");
      const row = label.closest(".set-row");
      if (!row) {
        throw new Error("auto-install row not found");
      }
      const toggle = row.querySelector('[role="switch"]');
      if (!toggle) {
        throw new Error("auto-install switch not found");
      }
      return toggle as HTMLElement;
    }

    it("auto-install reflects and persists the stored preference", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const toggle = autoInstallToggle();
      // Starts off (localStorage cleared in beforeEach): installing restarts
      // the app, so it must be opted into.
      expect(toggle).toHaveAttribute("aria-checked", "false");

      await user.click(toggle);
      expect(toggle).toHaveAttribute("aria-checked", "true");
      // Written through so the next launch's update check reads it.
      expect(localStorage.getItem("tenebra.autoInstallUpdates")).toBe("1");

      await user.click(toggle);
      expect(toggle).toHaveAttribute("aria-checked", "false");
      expect(localStorage.getItem("tenebra.autoInstallUpdates")).toBe("0");
    });

    it("auto-install starts on when previously enabled", async () => {
      localStorage.setItem("tenebra.autoInstallUpdates", "1");
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(autoInstallToggle()).toHaveAttribute("aria-checked", "true");
      // Let the mount-time getVersion/isEnabled promises settle inside the
      // test so their state updates land wrapped in act.
      await screen.findByText("Current version 0.1.0");
    });

    it("moves from checking to available when the updater finds a release", async () => {
      const update = { version: "9.9.9", downloadAndInstall: vi.fn() };
      // Hold the check open so the transient "checking" state is observable
      // before the result lands.
      let resolveCheck!: (value: Update | null) => void;
      vi.mocked(check).mockReturnValue(
        new Promise<Update | null>((resolve) => {
          resolveCheck = resolve;
        }),
      );

      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(
        screen.getByRole("button", { name: "Check for updates" }),
      );

      // While the check is in flight the action button is relabeled and locked.
      expect(screen.getByRole("button", { name: /Checking/ })).toBeDisabled();

      // Resolving with an update flips the row to the available affordance: the
      // version-bearing hint plus an install button.
      resolveCheck(update as unknown as Update);
      expect(
        await screen.findByText("Version 9.9.9 is available."),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Install and restart" }),
      ).toBeInTheDocument();
    });

    it("reports up to date when no newer release exists", async () => {
      vi.mocked(check).mockResolvedValue(null);

      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(
        screen.getByRole("button", { name: "Check for updates" }),
      );

      expect(
        await screen.findByText("You're on the latest version."),
      ).toBeInTheDocument();
      // No update to install, so the row keeps the check affordance.
      expect(
        screen.queryByRole("button", { name: "Install and restart" }),
      ).not.toBeInTheDocument();
    });

    it("shows the error hint when the update check fails", async () => {
      vi.mocked(check).mockRejectedValue(new Error("offline"));

      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(
        screen.getByRole("button", { name: "Check for updates" }),
      );

      expect(
        await screen.findByText("Couldn't check for updates. Try again later."),
      ).toBeInTheDocument();
    });

    it("surfaces an install failure as the error hint and never relaunches", async () => {
      const update = {
        version: "9.9.9",
        downloadAndInstall: vi.fn().mockRejectedValue(new Error("disk full")),
      };
      vi.mocked(check).mockResolvedValue(update as unknown as Update);

      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(
        screen.getByRole("button", { name: "Check for updates" }),
      );
      const install = await screen.findByRole("button", {
        name: "Install and restart",
      });
      await user.click(install);

      expect(
        await screen.findByText("Couldn't check for updates. Try again later."),
      ).toBeInTheDocument();
      // A failed download must not fall through to the restart.
      expect(relaunch).not.toHaveBeenCalled();
    });
  });

  describe("update channel", () => {
    it("defaults to stable and marks it checked", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(screen.getByRole("radio", { name: /Stable/ })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      expect(screen.getByRole("radio", { name: /Beta/ })).toHaveAttribute(
        "aria-checked",
        "false",
      );
    });

    it("exposes the options as a labelled radiogroup", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(
        screen.getByRole("radiogroup", { name: "Update channel" }),
      ).toBeInTheDocument();
    });

    it("selects beta on click and persists the choice", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(screen.getByRole("radio", { name: /Beta/ }));

      expect(screen.getByRole("radio", { name: /Beta/ })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      expect(screen.getByRole("radio", { name: /Stable/ })).toHaveAttribute(
        "aria-checked",
        "false",
      );
      // Written through so the launch check and the manual check read it.
      expect(localStorage.getItem("tenebra.updateChannel")).toBe("beta");
    });

    it("starts on beta when previously selected", () => {
      localStorage.setItem("tenebra.updateChannel", "beta");
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(screen.getByRole("radio", { name: /Beta/ })).toHaveAttribute(
        "aria-checked",
        "true",
      );
    });

    it("carries focus with selection on ArrowDown and wraps back", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const stable = screen.getByRole("radio", { name: /Stable/ });
      const beta = screen.getByRole("radio", { name: /Beta/ });

      // stable is index 0, where the roving tabIndex parks the caret.
      stable.focus();
      expect(stable).toHaveFocus();

      await user.keyboard("{ArrowDown}");
      expect(beta).toHaveFocus();
      expect(beta).toHaveAttribute("aria-checked", "true");
      expect(localStorage.getItem("tenebra.updateChannel")).toBe("beta");

      // A second press wraps around to the first option.
      await user.keyboard("{ArrowDown}");
      expect(stable).toHaveFocus();
      expect(stable).toHaveAttribute("aria-checked", "true");
      expect(localStorage.getItem("tenebra.updateChannel")).toBe("stable");
    });
  });
});
