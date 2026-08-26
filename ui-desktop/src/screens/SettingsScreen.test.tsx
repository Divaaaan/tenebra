import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { SettingsScreen, pickActiveSection } from "./SettingsScreen";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeProfile, makeTenebra } from "../test/fixtures";
import { dictionaries } from "../i18n/strings";
import { subscribeToast } from "../lib/toast";
import type { State } from "../api";
import { disable, enable, isEnabled } from "@tauri-apps/plugin-autostart";
import { getVersion } from "@tauri-apps/api/app";
import { check, type Update } from "@tauri-apps/plugin-updater";
import { relaunch } from "@tauri-apps/plugin-process";
import { invoke } from "@tauri-apps/api/core";

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
// The updates row also asks the backend whether this install can replace itself
// (only a package manager's copy cannot); stub the command channel so the screen
// mounts without a Tauri host.
vi.mock("@tauri-apps/api/core", () => ({
  invoke: vi.fn(),
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
    // Same for the "can this install replace itself?" probe: everything but the
    // packaged-install case runs as a self-updating build.
    vi.mocked(invoke).mockResolvedValue(true);
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

  describe("connection mode", () => {
    const modeGroup = () =>
      within(screen.getByRole("radiogroup", { name: "Connection mode" }));

    it("marks the reported mode as checked, defaulting to tun", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(modeGroup().getByRole("radio", { name: /^TUN/ })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      expect(
        modeGroup().getByRole("radio", { name: /^System proxy/ }),
      ).toHaveAttribute("aria-checked", "false");
    });

    it("reflects system-proxy from the core state", () => {
      const tenebra = makeTenebra({
        state: { state: "idle", proxy_mode: "system-proxy" } as State,
      });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(
        modeGroup().getByRole("radio", { name: /^System proxy/ }),
      ).toHaveAttribute("aria-checked", "true");
    });

    it("requests the new mode on click and skips a no-op", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(modeGroup().getByRole("radio", { name: /^System proxy/ }));
      expect(tenebra.setProxyMode).toHaveBeenCalledWith("system-proxy");

      // Clicking the already-active mode must not re-apply (a live tunnel would be
      // pointlessly hot-swapped).
      await user.click(modeGroup().getByRole("radio", { name: /^TUN/ }));
      expect(tenebra.setProxyMode).toHaveBeenCalledTimes(1);
    });

    it("moves to the next mode on ArrowDown", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      fireEvent.keyDown(modeGroup().getByRole("radio", { name: /^TUN/ }), {
        key: "ArrowDown",
      });
      expect(tenebra.setProxyMode).toHaveBeenCalledWith("system-proxy");
    });
  });

  describe("tunnel stack", () => {
    // The "System proxy" connection-mode radio shares the "System" prefix with the
    // "System" stack radio, so scope stack lookups to the Tunnel group to keep the
    // matcher unambiguous.
    const stackGroup = () => within(screen.getByRole("radiogroup", { name: "Tunnel" }));

    it("marks the reported stack as checked, defaulting to system", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(stackGroup().getByRole("radio", { name: /^System/ })).toHaveAttribute(
        "aria-checked",
        "true",
      );
      expect(stackGroup().getByRole("radio", { name: /^gVisor/ })).toHaveAttribute(
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

      await user.click(stackGroup().getByRole("radio", { name: /^gVisor/ }));
      expect(tenebra.setTun).toHaveBeenCalledWith("gvisor");

      // Clicking the already-active stack must not re-apply (a live tunnel
      // would be pointlessly hot-swapped).
      await user.click(stackGroup().getByRole("radio", { name: /^System/ }));
      expect(tenebra.setTun).toHaveBeenCalledTimes(1);
    });

    it("moves to the next stack on ArrowDown", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      fireEvent.keyDown(stackGroup().getByRole("radio", { name: /^System/ }), {
        key: "ArrowDown",
      });
      expect(tenebra.setTun).toHaveBeenCalledWith("gvisor");
    });

    it("carries focus with selection so repeated ArrowDown reaches every stack", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const system = stackGroup().getByRole("radio", { name: /^System/ });
      const gvisor = stackGroup().getByRole("radio", { name: /^gVisor/ });
      const mixed = stackGroup().getByRole("radio", { name: /^Mixed/ });

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

  describe("routing presets", () => {
    // Same shape as the RU preset rows: the switch's accessible name is its own
    // ON/OFF text, so find the row by its label and take the switch inside it.
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

    const presetState = {
      state: "idle",
      preset_voice_direct: true,
      preset_unblock_services: true,
    } as State;

    // These shipped on and invisible: the command existed only in the core, so
    // the two presets that route traffic around the tunnel could not be seen or
    // switched off from the app at all.
    it("reflects the presets from core state", () => {
      const tenebra = makeTenebra({ state: presetState });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(presetToggle("Real-time UDP skips the tunnel")).toHaveAttribute(
        "aria-checked",
        "true",
      );
      expect(presetToggle("Games skip the tunnel")).toHaveAttribute(
        "aria-checked",
        "false",
      );
      expect(presetToggle("Unblock censored services")).toHaveAttribute(
        "aria-checked",
        "true",
      );
    });

    it("toggling one preset names only that one", async () => {
      const tenebra = makeTenebra({ state: presetState });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(presetToggle("Games skip the tunnel"));
      expect(tenebra.setPresets).toHaveBeenCalledWith({ games: true });

      await user.click(presetToggle("Real-time UDP skips the tunnel"));
      expect(tenebra.setPresets).toHaveBeenCalledWith({ voice: false });
    });

    // A switch that sends the user's real address to whoever is on the call has
    // to say so where the user decides, not in a changelog.
    it("says what the traffic-leaking presets cost", () => {
      const tenebra = makeTenebra({ state: presetState });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const voiceRow = screen
        .getByText("Real-time UDP skips the tunnel")
        .closest(".set-row");
      expect(voiceRow).toHaveTextContent(/real IP address/i);

      const gamesRow = screen
        .getByText("Games skip the tunnel")
        .closest(".set-row");
      expect(gamesRow).toHaveTextContent(/real IP address/i);
    });

    it("keeps the russian strings for the same rows", () => {
      const tenebra = makeTenebra({ state: presetState });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />, { lang: "ru" });

      expect(
        presetToggle(dictionaries.ru.settings.presetVoiceDirect),
      ).toHaveAttribute("aria-checked", "true");
      const voiceRow = screen
        .getByText(dictionaries.ru.settings.presetVoiceDirect)
        .closest(".set-row");
      expect(voiceRow).toHaveTextContent(/реальный IP/i);
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

  describe("censorship bypass", () => {
    function tlsToggle(): HTMLElement {
      const row = screen
        .getByText("DPI bypass — TLS fragmentation")
        .closest(".set-row");
      if (!row) {
        throw new Error("tls-fragment row not found");
      }
      const toggle = row.querySelector('[role="switch"]');
      if (!toggle) {
        throw new Error("tls-fragment switch not found");
      }
      return toggle as HTMLElement;
    }

    it("reflects the armed TLS-fragmentation state from the core", () => {
      const tenebra = makeTenebra({
        state: { state: "idle", tls_fragment: true } as State,
      });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(tlsToggle()).toHaveAttribute("aria-checked", "true");
    });

    it("starts off when the core omits the field", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(tlsToggle()).toHaveAttribute("aria-checked", "false");
    });

    it("arms fragmentation through the core", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(tlsToggle());
      expect(tenebra.setTlsFragment).toHaveBeenCalledWith(true);
    });

    it("disarms from the reported on state", async () => {
      const tenebra = makeTenebra({
        state: { state: "idle", tls_fragment: true } as State,
      });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(tlsToggle());
      expect(tenebra.setTlsFragment).toHaveBeenCalledWith(false);
    });
  });

  describe("reliability", () => {
    function failoverToggle(): HTMLElement {
      const row = screen
        .getByText("Auto-switch on node failure")
        .closest(".set-row");
      if (!row) {
        throw new Error("auto-failover row not found");
      }
      const toggle = row.querySelector('[role="switch"]');
      if (!toggle) {
        throw new Error("auto-failover switch not found");
      }
      return toggle as HTMLElement;
    }

    // The core defaults the watchdog on and projects that into State as a concrete
    // `auto_failover: true` (present), so an armed default arrives set; the field
    // is only ever absent after the user disarms it. Reading a missing field as
    // off is therefore correct and matches every other core-owned toggle here.
    it("reflects the armed default the core reports (present true)", () => {
      const tenebra = makeTenebra({
        state: { state: "idle", auto_failover: true } as State,
      });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(failoverToggle()).toHaveAttribute("aria-checked", "true");
    });

    it("reads as off once disarmed (the core omits the field)", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(failoverToggle()).toHaveAttribute("aria-checked", "false");
    });

    it("disarms through the core from the armed state", async () => {
      const tenebra = makeTenebra({
        state: { state: "idle", auto_failover: true } as State,
      });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(failoverToggle());
      expect(tenebra.setAutoFailover).toHaveBeenCalledWith(false);
    });

    it("re-arms through the core from the disarmed state", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(failoverToggle());
      expect(tenebra.setAutoFailover).toHaveBeenCalledWith(true);
    });
  });

  describe("multihop", () => {
    function multihopToggle(): HTMLElement {
      const row = screen.getByText("Route through two nodes").closest(".set-row");
      if (!row) {
        throw new Error("multihop row not found");
      }
      const toggle = row.querySelector('[role="switch"]');
      if (!toggle) {
        throw new Error("multihop switch not found");
      }
      return toggle as HTMLElement;
    }

    it("lists the active profile's nodes in both selectors", () => {
      const tenebra = makeTenebra({
        state: { state: "idle" } as State,
        profiles: [makeProfile()],
      });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const entry = screen.getByLabelText("Entry node");
      expect(
        within(entry).getByRole("option", { name: "Amsterdam" }),
      ).toBeInTheDocument();
      expect(
        within(entry).getByRole("option", { name: "Frankfurt" }),
      ).toBeInTheDocument();
    });

    it("reflects the armed chain the core reports", () => {
      const tenebra = makeTenebra({
        state: {
          state: "connected",
          profile: "profile-1",
          multihop: { enabled: true, entry_id: "node-1", exit_id: "node-2" },
        } as State,
        profiles: [makeProfile()],
      });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(multihopToggle()).toHaveAttribute("aria-checked", "true");
      expect(screen.getByLabelText("Entry node")).toHaveValue("node-1");
      expect(screen.getByLabelText("Exit node")).toHaveValue("node-2");
    });

    it("records an entry pick through the core, kept off until armed", async () => {
      const tenebra = makeTenebra({
        state: { state: "idle" } as State,
        profiles: [makeProfile()],
      });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.selectOptions(screen.getByLabelText("Entry node"), "node-1");
      expect(tenebra.setMultihop).toHaveBeenCalledWith(
        "profile-1",
        false,
        "node-1",
        "",
      );
    });

    it("arms the chain through the core from a chosen pair", async () => {
      const tenebra = makeTenebra({
        state: {
          state: "idle",
          profile: "profile-1",
          multihop: { enabled: false, entry_id: "node-1", exit_id: "node-2" },
        } as State,
        profiles: [makeProfile()],
      });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(multihopToggle());
      expect(tenebra.setMultihop).toHaveBeenCalledWith(
        "profile-1",
        true,
        "node-1",
        "node-2",
      );
    });

    it("keeps the toggle inert until both ends are chosen", async () => {
      const tenebra = makeTenebra({
        state: { state: "idle" } as State,
        profiles: [makeProfile()],
      });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(multihopToggle()).toBeDisabled();
      await user.click(multihopToggle());
      expect(tenebra.setMultihop).not.toHaveBeenCalled();
    });
  });

  describe("diagnostics", () => {
    it("gates the speed test on a live connection", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(screen.getByRole("button", { name: "Speed test" })).toBeDisabled();
    });

    it("enables the speed test once connected", () => {
      const tenebra = makeTenebra({ state: { state: "connected" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(screen.getByRole("button", { name: "Speed test" })).toBeEnabled();
    });

    it("always offers the STUN probe, connected or not", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(
        screen.getByRole("button", { name: "Check UDP / NAT" }),
      ).toBeEnabled();
    });
  });

  describe("simple mode", () => {
    function simpleToggle(): HTMLElement {
      const row = screen.getByText("Simple mode").closest(".set-row");
      if (!row) {
        throw new Error("simple-mode row not found");
      }
      const toggle = row.querySelector('[role="switch"]');
      if (!toggle) {
        throw new Error("simple-mode switch not found");
      }
      return toggle as HTMLElement;
    }

    it("starts off when the flag is unset", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(simpleToggle()).toHaveAttribute("aria-checked", "false");
    });

    it("reads the persisted flag on mount", () => {
      localStorage.setItem("tenebra.simpleMode", "true");
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);
      expect(simpleToggle()).toHaveAttribute("aria-checked", "true");
    });

    it("writes the exact key/value and raises a storage event for the shell", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      const seen: StorageEvent[] = [];
      const onStorage = (e: StorageEvent) => seen.push(e);
      window.addEventListener("storage", onStorage);
      try {
        renderWithProviders(<SettingsScreen tenebra={tenebra} />);

        await user.click(simpleToggle());
        // The flag is written verbatim ('true'/'false'), the shell's contract.
        expect(localStorage.getItem("tenebra.simpleMode")).toBe("true");
        expect(simpleToggle()).toHaveAttribute("aria-checked", "true");
        // A same-document storage event, keyed on the flag, reaches the shell.
        const hit = seen.find((e) => e.key === "tenebra.simpleMode");
        expect(hit).toBeDefined();
        expect(hit?.newValue).toBe("true");

        await user.click(simpleToggle());
        expect(localStorage.getItem("tenebra.simpleMode")).toBe("false");
      } finally {
        window.removeEventListener("storage", onStorage);
      }
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

    // The crash-reports switch is core-owned like autoconnect: found by its row
    // label, and a click toggles through the daemon rather than localStorage.
    function crashReportsToggle(): HTMLElement {
      const row = screen.getByText("Crash reports").closest(".set-row");
      if (!row) {
        throw new Error("crash-reports row not found");
      }
      const toggle = row.querySelector('[role="switch"]');
      if (!toggle) {
        throw new Error("crash-reports switch not found");
      }
      return toggle as HTMLElement;
    }

    it("crash reports start off when the core omits the field", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(crashReportsToggle()).toHaveAttribute("aria-checked", "false");
    });

    it("crash reports reflect the reported on state", () => {
      const tenebra = makeTenebra({
        state: {
          state: "idle",
          crash_reports: true,
          crash_reports_asked: true,
        } as State,
      });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(crashReportsToggle()).toHaveAttribute("aria-checked", "true");
    });

    it("crash reports toggle through the core, not localStorage", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(crashReportsToggle());
      expect(tenebra.setCrashReports).toHaveBeenCalledWith(true);
    });

    it("stands the updater down when a package manager owns this install", async () => {
      // A Linux copy from apt/pacman: the app cannot replace files the package
      // manager owns, so the row has to say where updates come from rather than
      // leave a Check button that leads to an install that would fail.
      vi.mocked(invoke).mockResolvedValue(false);
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(
        await screen.findByText(
          "This copy was installed by your package manager — update Tenebra through it.",
        ),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Check for updates" }),
      ).toBeDisabled();
      // And the auto-install preference has nothing to arm: the launch check
      // never runs on such an install.
      expect(autoInstallToggle()).toBeDisabled();
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

    // The check runs on a schedule of its own now (lib/updateSchedule), so this
    // row is the only place the schedule is visible. A client that has quietly
    // stopped reaching the release host looks exactly like one with nothing to
    // install unless the row says when it last managed to ask.
    it("says when the client last checked", async () => {
      localStorage.setItem(
        "tenebra.updateLastCheck",
        String(Date.parse("2026-08-24T14:03:00Z")),
      );

      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(
        await screen.findByText("Last checked Aug 24, 14:03"),
      ).toBeInTheDocument();
    });

    it("says so when the last check did not get an answer", async () => {
      localStorage.setItem(
        "tenebra.updateLastCheck",
        String(Date.parse("2026-08-24T14:03:00Z")),
      );
      localStorage.setItem("tenebra.updateFailures", "2");

      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(
        await screen.findByText("Last check failed Aug 24, 14:03"),
      ).toBeInTheDocument();
    });

    it("says nothing has been checked yet on a fresh install", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      expect(await screen.findByText("Not checked yet")).toBeInTheDocument();
    });

    it("records a manual check against the same schedule", async () => {
      // Whoever asked for it, a check that answered is a check: it clears the
      // failure run behind the "couldn't check" banner and pushes the next
      // scheduled one out by a full interval.
      localStorage.setItem("tenebra.updateFailures", "4");
      vi.mocked(check).mockResolvedValue(null);

      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(
        screen.getByRole("button", { name: "Check for updates" }),
      );
      await screen.findByText("You're on the latest version.");

      expect(localStorage.getItem("tenebra.updateFailures")).toBe("0");
      expect(localStorage.getItem("tenebra.updateLastCheck")).not.toBeNull();
    });

    it("counts a failed manual check toward the same run", async () => {
      localStorage.setItem("tenebra.updateFailures", "1");
      vi.mocked(check).mockRejectedValue(new Error("offline"));

      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(
        screen.getByRole("button", { name: "Check for updates" }),
      );
      await screen.findByText("Couldn't check for updates. Try again later.");

      expect(localStorage.getItem("tenebra.updateFailures")).toBe("2");
    });

    it("keeps the updater's own words out of the hint but within reach", async () => {
      vi.mocked(check).mockRejectedValue(new Error("dns lookup failed"));

      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(
        screen.getByRole("button", { name: "Check for updates" }),
      );

      const hint = await screen.findByText(
        "Couldn't check for updates. Try again later.",
      );
      // The sentence the user reads is localized; the raw rejection is the
      // tooltip, for whoever has to work out why.
      expect(hint).toHaveAttribute("title", "Error: dns lookup failed");
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

  describe("section navigation", () => {
    it("picks the active section from the scroll position (last past the threshold)", () => {
      // Eight section tops; the picker returns the last whose top − 60 has been
      // scrolled past, and never goes below the first.
      const tops = [0, 300, 620, 900, 1250, 1600, 1850, 2200];
      expect(pickActiveSection(0, tops)).toBe(0);
      expect(pickActiveSection(50, tops)).toBe(0); // 50 < 300−60, still first
      expect(pickActiveSection(240, tops)).toBe(1); // 240 ≥ 300−60
      expect(pickActiveSection(560, tops)).toBe(2); // 560 ≥ 620−60
      expect(pickActiveSection(1_000_000, tops)).toBe(7); // past the end → last
    });

    it("locks onto the last section when the container is scrolled to its bottom", () => {
      // A short final section cannot reach the container top: with the scroll
      // pinned at the bottom (scrollTop 2300 + viewport 700 = scrollHeight 3000)
      // the raw threshold pick would stay on the previous section and a click on
      // the last rail item would visibly snap back. The viewport-aware pick must
      // return the last section instead.
      const tops = [0, 300, 620, 900, 1250, 1600, 1850, 2680];
      const viewport = { clientHeight: 700, scrollHeight: 3000 };
      expect(pickActiveSection(2300, tops, undefined, viewport)).toBe(7);
      // Away from the bottom the threshold logic is untouched.
      expect(pickActiveSection(2300, tops)).toBe(6);
      expect(pickActiveSection(560, tops, undefined, viewport)).toBe(2);
    });

    it("lists one rail link per section in order", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const rail = screen.getByRole("navigation", { name: "Settings sections" });
      const links = within(rail).getAllByRole("button");
      // routing, split, presets, rules, mode, tunnel, dns, bypass, multihop,
      // reliability, diagnostics, appearance, startup, updates.
      expect(links).toHaveLength(14);
      expect(links[0]).toHaveTextContent("Routing");
      expect(links[2]).toHaveTextContent("Routing presets");
      expect(links[3]).toHaveTextContent("Custom rules");
      expect(links[4]).toHaveTextContent("Connection mode");
      expect(links[7]).toHaveTextContent("Censorship bypass");
      expect(links[8]).toHaveTextContent("Multihop");
      expect(links[10]).toHaveTextContent("Diagnostics");
      expect(links[13]).toHaveTextContent("Updates");
    });

    it("scrolls to a section and highlights its link on click", async () => {
      // jsdom doesn't implement scrollIntoView; install a spy so the jump is
      // observable and the optional call actually fires.
      const scrollIntoView = vi.fn();
      Element.prototype.scrollIntoView = scrollIntoView;

      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      const rail = screen.getByRole("navigation", { name: "Settings sections" });
      const dnsLink = within(rail).getByRole("button", { name: "DNS" });
      await user.click(dnsLink);

      expect(scrollIntoView).toHaveBeenCalledTimes(1);
      // The clicked link takes the active mark immediately.
      expect(dnsLink).toHaveAttribute("aria-current", "true");
    });

    it("closes via the sticky esc button by re-raising Escape for the app handler", async () => {
      // The screen has no close callback; the button re-raises the Escape the app
      // already listens for. Prove the keydown reaches a window listener.
      let escaped = false;
      const onKey = (e: KeyboardEvent) => {
        if (e.key === "Escape") {
          escaped = true;
        }
      };
      window.addEventListener("keydown", onKey);

      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      try {
        await user.click(screen.getByRole("button", { name: "Close settings" }));
        expect(escaped).toBe(true);
      } finally {
        window.removeEventListener("keydown", onKey);
      }
    });

    it("shows the app version and licence at the foot of the rail", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      // getVersion() resolves 0.1.0 (mocked); the rail reads "v{version} · GPLv3".
      expect(await screen.findByText("v0.1.0 · GPLv3")).toBeInTheDocument();
    });

    it("still renders every section heading (controls preserved under the new shell)", () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      for (const name of [
        "Routing",
        "Split tunneling",
        "Custom rules",
        "Connection mode",
        "Tunnel",
        "DNS",
        "Censorship bypass",
        "Multihop",
        "Reliability",
        "Diagnostics",
        "Appearance",
        "Startup",
        "Updates",
      ]) {
        expect(
          screen.getByRole("heading", { level: 2, name }),
        ).toBeInTheDocument();
      }
    });
  });

  // Every core-owned control here used to send its command with either a bare
  // `void` (an unhandled rejection) or `.catch(() => {})`. Since the visible
  // position of a switch is read back from the core's echo, a refused command
  // meant the switch simply did not move and nothing said why — which is exactly
  // the "half the toggles are dead" report from a machine whose background
  // service is older than the app.
  describe("refused commands", () => {
    const en = dictionaries.en;

    // Toast subscriptions live in a module-level bus, so each test detaches its
    // own listener again or later tests would keep feeding it.
    const detach: (() => void)[] = [];
    afterEach(() => {
      detach.splice(0).forEach((fn) => fn());
    });

    /** Collect everything raised on the toast bus during a test. */
    function captureToasts(): string[] {
      const seen: string[] = [];
      detach.push(subscribeToast((m) => seen.push(m)));
      return seen;
    }

    /** The switch inside the row carrying `label` (its own name is "ON"/"OFF"). */
    function switchIn(label: string): HTMLElement {
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

    it("blames the out-of-date service when a toggle is refused as unknown", async () => {
      const tenebra = makeTenebra({
        state: { state: "idle" } as State,
        setTlsFragment: vi
          .fn()
          .mockRejectedValue('unknown command "set_tls_fragment"'),
      });
      const toasts = captureToasts();
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(switchIn("DPI bypass — TLS fragmentation"));

      await waitFor(() => expect(toasts).toContain(en.daemon.commandUnknown));
    });

    it("reports a refused setting instead of swallowing it", async () => {
      const tenebra = makeTenebra({
        state: { state: "idle" } as State,
        setAutoconnect: vi.fn().mockRejectedValue("the service is still down"),
      });
      const toasts = captureToasts();
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(switchIn("Connect on launch"));

      await waitFor(() => expect(toasts).toContain(en.daemon.commandFailed));
    });

    it("reports a refused radio choice too, not only the switches", async () => {
      // setRouting was sent with a bare `void` — no catch at all, so a refusal
      // was an unhandled rejection the user never saw.
      const tenebra = makeTenebra({
        state: { state: "idle", routing: "smart" } as State,
        setRouting: vi.fn().mockRejectedValue('unknown command "set_routing"'),
      });
      const toasts = captureToasts();
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(screen.getByRole("radio", { name: /Global/ }));

      await waitFor(() => expect(toasts).toContain(en.daemon.commandUnknown));
    });

    it("stays quiet when the command is accepted", async () => {
      const tenebra = makeTenebra({ state: { state: "idle" } as State });
      const toasts = captureToasts();
      const user = userEvent.setup();
      renderWithProviders(<SettingsScreen tenebra={tenebra} />);

      await user.click(switchIn("Connect on launch"));

      await waitFor(() => expect(tenebra.setAutoconnect).toHaveBeenCalled());
      expect(toasts).toEqual([]);
    });
  });
});
