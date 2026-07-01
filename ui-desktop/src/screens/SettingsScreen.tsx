import { useEffect, useState, type KeyboardEvent } from "react";
import { disable, enable, isEnabled } from "@tauri-apps/plugin-autostart";
import { getVersion } from "@tauri-apps/api/app";
import type { Update } from "@tauri-apps/plugin-updater";

import type { RoutingMode, SplitMode, TunStack } from "../api";
import type { Tenebra } from "../state/useTenebra";
import { useI18n } from "../i18n/I18nContext";
import { useTheme } from "../theme/ThemeContext";
import type { Language } from "../i18n/strings";
import {
  getAutoconnect,
  getAutoFastest,
  setAutoconnect,
  setAutoFastest,
} from "../lib/settings";
import { checkForUpdate, installUpdate, type UpdateStatus } from "../lib/updates";

interface SettingsScreenProps {
  tenebra: Tenebra;
}

export function SettingsScreen({ tenebra }: SettingsScreenProps) {
  const { t, lang, setLang } = useI18n();
  const { theme, setTheme } = useTheme();

  // Launch-at-login mirrors the OS autostart registration; we read the real
  // state on mount so the toggle reflects what the system actually has set.
  const [launchAtLogin, setLaunchAtLogin] = useState(false);
  const [launchBusy, setLaunchBusy] = useState(false);
  const [autoconnect, setAutoconnectState] = useState(getAutoconnect);
  const [autoFastest, setAutoFastestState] = useState(getAutoFastest);
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus>({ kind: "idle" });
  const [pendingUpdate, setPendingUpdate] = useState<Update | null>(null);
  const [appVersion, setAppVersion] = useState("");

  useEffect(() => {
    getVersion()
      .then(setAppVersion)
      .catch(() => {
        // The version line is cosmetic; leave it blank if the host call fails.
      });
  }, []);

  useEffect(() => {
    let active = true;
    isEnabled()
      .then((on) => {
        if (active) {
          setLaunchAtLogin(on);
        }
      })
      .catch(() => {
        // If the query fails, leave the toggle off rather than guessing on.
      });
    return () => {
      active = false;
    };
  }, []);

  async function toggleLaunchAtLogin() {
    if (launchBusy) {
      return;
    }
    setLaunchBusy(true);
    const next = !launchAtLogin;
    try {
      if (next) {
        await enable();
      } else {
        await disable();
      }
      setLaunchAtLogin(next);
    } catch {
      // Keep the toggle where it was if the OS rejected the change.
    } finally {
      setLaunchBusy(false);
    }
  }

  function toggleAutoconnect() {
    setAutoconnectState((prev) => {
      const next = !prev;
      setAutoconnect(next);
      return next;
    });
  }

  function toggleAutoFastest() {
    setAutoFastestState((prev) => {
      const next = !prev;
      setAutoFastest(next);
      return next;
    });
  }

  async function checkUpdates() {
    setUpdateStatus({ kind: "checking" });
    try {
      const update = await checkForUpdate();
      if (!update) {
        setPendingUpdate(null);
        setUpdateStatus({ kind: "uptodate" });
        return;
      }
      setPendingUpdate(update);
      setUpdateStatus({ kind: "available", version: update.version });
    } catch (e) {
      setUpdateStatus({ kind: "error", message: String(e) });
    }
  }

  async function applyUpdate() {
    if (!pendingUpdate) {
      return;
    }
    setUpdateStatus({ kind: "downloading", percent: null });
    try {
      await installUpdate(pendingUpdate, (percent) =>
        setUpdateStatus({ kind: "downloading", percent }),
      );
      // relaunch() normally ends the process; reaching here means the install
      // completed and the restart is imminent.
      setUpdateStatus({ kind: "installing" });
    } catch (e) {
      setUpdateStatus({ kind: "error", message: String(e) });
    }
  }

  const updateBusy =
    updateStatus.kind === "checking" || updateStatus.kind === "downloading";
  const updateStatusText = ((): string => {
    switch (updateStatus.kind) {
      case "checking":
        return t.settings.updatesChecking;
      case "uptodate":
        return t.settings.updatesUpToDate;
      case "available":
        return t.settings.updatesAvailable.replace("{version}", updateStatus.version);
      case "downloading":
        return updateStatus.percent == null
          ? t.settings.updatesDownloading
          : `${t.settings.updatesDownloading} ${updateStatus.percent}%`;
      case "installing":
        return t.settings.updatesInstalling;
      case "error":
        return t.settings.updatesError;
      default:
        return appVersion
          ? t.settings.updatesCurrent.replace("{version}", appVersion)
          : "";
    }
  })();

  const routing = tenebra.state.routing ?? "smart";

  const routingOptions: { mode: RoutingMode; label: string; hint: string }[] = [
    { mode: "smart", label: t.settings.routingSmart, hint: t.settings.routingSmartHint },
    { mode: "global", label: t.settings.routingGlobal, hint: t.settings.routingGlobalHint },
    { mode: "direct", label: t.settings.routingDirect, hint: t.settings.routingDirectHint },
  ];

  function onRoutingKey(e: KeyboardEvent, index: number) {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") {
      return;
    }
    e.preventDefault();
    const delta = e.key === "ArrowDown" ? 1 : -1;
    const next = routingOptions[(index + delta + routingOptions.length) % routingOptions.length];
    void tenebra.setRouting(next.mode);
  }

  // Split tunnelling. The core owns the canonical list (it normalizes names), so
  // the rendered list always reflects tenebra.state rather than a local copy; the
  // text field is the only local state.
  const splitMode = tenebra.state.split ?? "off";
  const splitApps = tenebra.state.split_apps ?? [];
  const [appDraft, setAppDraft] = useState("");

  const splitOptions: { mode: SplitMode; label: string; hint: string }[] = [
    { mode: "off", label: t.settings.splitOff, hint: t.settings.splitOffHint },
    { mode: "exclude", label: t.settings.splitExclude, hint: t.settings.splitExcludeHint },
    { mode: "include", label: t.settings.splitInclude, hint: t.settings.splitIncludeHint },
  ];

  function chooseSplitMode(mode: SplitMode) {
    if (mode === splitMode) {
      return;
    }
    void tenebra.setSplit(mode, splitApps);
  }

  function onSplitKey(e: KeyboardEvent, index: number) {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") {
      return;
    }
    e.preventDefault();
    const delta = e.key === "ArrowDown" ? 1 : -1;
    const next = splitOptions[(index + delta + splitOptions.length) % splitOptions.length];
    chooseSplitMode(next.mode);
  }

  // Normalize the draft the same way the core does for the dedupe check, so the
  // UI doesn't offer to add a name that will collapse server-side anyway.
  const normalizedDraft = appDraft.trim().toLowerCase();
  const canAddApp =
    splitMode !== "off" &&
    normalizedDraft.length > 0 &&
    !splitApps.includes(normalizedDraft);

  function addApp() {
    if (!canAddApp) {
      return;
    }
    void tenebra.setSplit(splitMode, [...splitApps, normalizedDraft]);
    setAppDraft("");
  }

  function removeApp(name: string) {
    void tenebra.setSplit(
      splitMode,
      splitApps.filter((a) => a !== name),
    );
  }

  function onAppInputKey(e: KeyboardEvent) {
    if (e.key === "Enter") {
      e.preventDefault();
      addApp();
    }
  }

  // The tun network stack. Core-owned like routing: the daemon validates,
  // persists, and — when a tunnel is live — re-applies it in place, so the UI
  // just reflects tenebra.state and requests changes.
  const tunStack = tenebra.state.tun_stack ?? "system";

  const stackOptions: { stack: TunStack; label: string; hint: string }[] = [
    { stack: "system", label: t.settings.stackSystem, hint: t.settings.stackSystemHint },
    { stack: "gvisor", label: t.settings.stackGvisor, hint: t.settings.stackGvisorHint },
    { stack: "mixed", label: t.settings.stackMixed, hint: t.settings.stackMixedHint },
  ];

  function chooseStack(stack: TunStack) {
    if (stack === tunStack) {
      return;
    }
    void tenebra.setTun(stack);
  }

  function onStackKey(e: KeyboardEvent, index: number) {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") {
      return;
    }
    e.preventDefault();
    const delta = e.key === "ArrowDown" ? 1 : -1;
    const next = stackOptions[(index + delta + stackOptions.length) % stackOptions.length];
    chooseStack(next.stack);
  }

  const themeOptions: { value: "dark" | "light"; label: string }[] = [
    { value: "dark", label: t.settings.themeDark },
    { value: "light", label: t.settings.themeLight },
  ];

  const langOptions: { value: Language; label: string }[] = [
    { value: "en", label: "EN" },
    { value: "ru", label: "RU" },
  ];

  return (
    <section className="set-doc">
      <h1 className="set-title">{t.settings.title}</h1>

      <section className="set-section">
        <div className="set-section-head">
          <h2 className="set-eyebrow">{t.settings.routing}</h2>
        </div>
        <div className="set-options" role="radiogroup" aria-label={t.settings.routing}>
          {routingOptions.map((opt, index) => {
            const checked = routing === opt.mode;
            return (
              <button
                key={opt.mode}
                type="button"
                role="radio"
                aria-checked={checked}
                tabIndex={checked ? 0 : -1}
                className={`set-option${checked ? " is-checked" : ""}`}
                onClick={() => void tenebra.setRouting(opt.mode)}
                onKeyDown={(e) => onRoutingKey(e, index)}
              >
                <span className="set-mark" aria-hidden="true">{checked ? "▣" : "▢"}</span>
                <span className="set-option-text">
                  <span className="set-option-label">{opt.label}</span>
                  <span className="set-option-hint">{opt.hint}</span>
                </span>
              </button>
            );
          })}
        </div>
      </section>

      <section className="set-section">
        <div className="set-section-head">
          <h2 className="set-eyebrow">{t.settings.split}</h2>
          <p className="set-sub">{t.settings.splitHint}</p>
        </div>

        <div className="set-options" role="radiogroup" aria-label={t.settings.split}>
          {splitOptions.map((opt, index) => {
            const checked = splitMode === opt.mode;
            return (
              <button
                key={opt.mode}
                type="button"
                role="radio"
                aria-checked={checked}
                tabIndex={checked ? 0 : -1}
                className={`set-option${checked ? " is-checked" : ""}`}
                onClick={() => chooseSplitMode(opt.mode)}
                onKeyDown={(e) => onSplitKey(e, index)}
              >
                <span className="set-mark" aria-hidden="true">{checked ? "▣" : "▢"}</span>
                <span className="set-option-text">
                  <span className="set-option-label">{opt.label}</span>
                  <span className="set-option-hint">{opt.hint}</span>
                </span>
              </button>
            );
          })}
        </div>

        {splitMode !== "off" && (
          <div className="set-apps">
            <span className="set-eyebrow">{t.settings.splitApps}</span>
            <div className="set-add">
              <span className="set-field">
                <span className="set-prompt" aria-hidden="true">$</span>
                <input
                  type="text"
                  value={appDraft}
                  onChange={(e) => setAppDraft(e.target.value)}
                  onKeyDown={onAppInputKey}
                  placeholder={t.settings.splitAddPlaceholder}
                  aria-label={t.settings.splitApps}
                  spellCheck={false}
                  autoCapitalize="off"
                  autoCorrect="off"
                />
              </span>
              <button
                type="button"
                className="set-btn"
                onClick={addApp}
                disabled={!canAddApp}
              >
                {t.settings.splitAdd}
              </button>
            </div>

            {splitApps.length === 0 ? (
              <p className="set-empty">{t.settings.splitAppsEmpty}</p>
            ) : (
              <ul className="set-app-list">
                {splitApps.map((name) => (
                  <li key={name} className="set-app-row">
                    <span className="set-app-name">{name}</span>
                    <button
                      type="button"
                      className="set-remove"
                      onClick={() => removeApp(name)}
                      aria-label={`${t.settings.splitRemove} ${name}`}
                    >
                      <span aria-hidden="true">✕</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </section>

      <section className="set-section">
        <div className="set-section-head">
          <h2 className="set-eyebrow">{t.settings.tunnel}</h2>
          <p className="set-sub">{t.settings.tunnelHint}</p>
        </div>
        <div className="set-options" role="radiogroup" aria-label={t.settings.tunnel}>
          {stackOptions.map((opt, index) => {
            const checked = tunStack === opt.stack;
            return (
              <button
                key={opt.stack}
                type="button"
                role="radio"
                aria-checked={checked}
                tabIndex={checked ? 0 : -1}
                className={`set-option${checked ? " is-checked" : ""}`}
                onClick={() => chooseStack(opt.stack)}
                onKeyDown={(e) => onStackKey(e, index)}
              >
                <span className="set-mark" aria-hidden="true">{checked ? "▣" : "▢"}</span>
                <span className="set-option-text">
                  <span className="set-option-label">{opt.label}</span>
                  <span className="set-option-hint">{opt.hint}</span>
                </span>
              </button>
            );
          })}
        </div>
      </section>

      <section className="set-section">
        <div className="set-section-head">
          <h2 className="set-eyebrow">{t.settings.appearance}</h2>
        </div>

        <div className="set-row">
          <span className="set-row-text">
            <span className="set-row-label">{t.settings.theme}</span>
          </span>
          <div className="set-seg" role="group" aria-label={t.settings.theme}>
            {themeOptions.map((opt) => (
              <button
                key={opt.value}
                type="button"
                aria-pressed={theme === opt.value}
                className={`${theme === opt.value ? "is-active" : ""}`}
                onClick={() => setTheme(opt.value)}
              >
                {opt.label}
              </button>
            ))}
          </div>
        </div>

        <div className="set-row">
          <span className="set-row-text">
            <span className="set-row-label">{t.settings.language}</span>
          </span>
          <div className="set-seg" role="group" aria-label={t.settings.language}>
            {langOptions.map((opt) => (
              <button
                key={opt.value}
                type="button"
                aria-pressed={lang === opt.value}
                className={`${lang === opt.value ? "is-active" : ""}`}
                onClick={() => setLang(opt.value)}
              >
                {opt.label}
              </button>
            ))}
          </div>
        </div>
      </section>

      <section className="set-section">
        <div className="set-section-head">
          <h2 className="set-eyebrow">{t.settings.startup}</h2>
        </div>
        <div className="set-row">
          <span className="set-row-text">
            <span className="set-row-label">{t.settings.launchAtLogin}</span>
            <span className="set-row-hint">{t.settings.launchAtLoginHint}</span>
          </span>
          <button
            type="button"
            role="switch"
            aria-checked={launchAtLogin}
            disabled={launchBusy}
            className={`set-switch${launchAtLogin ? " is-on" : ""}`}
            onClick={() => void toggleLaunchAtLogin()}
          >
            <span className="set-switch-box" aria-hidden="true">
              {launchAtLogin ? "▣" : "▢"}
            </span>
            {launchAtLogin ? "ON" : "OFF"}
          </button>
        </div>

        <div className="set-row">
          <span className="set-row-text">
            <span className="set-row-label">{t.settings.autoconnect}</span>
            <span className="set-row-hint">{t.settings.autoconnectHint}</span>
          </span>
          <button
            type="button"
            role="switch"
            aria-checked={autoconnect}
            className={`set-switch${autoconnect ? " is-on" : ""}`}
            onClick={toggleAutoconnect}
          >
            <span className="set-switch-box" aria-hidden="true">
              {autoconnect ? "▣" : "▢"}
            </span>
            {autoconnect ? "ON" : "OFF"}
          </button>
        </div>

        <div className="set-row">
          <span className="set-row-text">
            <span className="set-row-label">{t.settings.autoFastest}</span>
            <span className="set-row-hint">{t.settings.autoFastestHint}</span>
          </span>
          <button
            type="button"
            role="switch"
            aria-checked={autoFastest}
            className={`set-switch${autoFastest ? " is-on" : ""}`}
            onClick={toggleAutoFastest}
          >
            <span className="set-switch-box" aria-hidden="true">
              {autoFastest ? "▣" : "▢"}
            </span>
            {autoFastest ? "ON" : "OFF"}
          </button>
        </div>
      </section>

      <section className="set-section">
        <div className="set-section-head">
          <h2 className="set-eyebrow">{t.settings.updates}</h2>
        </div>
        <div className="set-row">
          <span className="set-row-text">
            <span className="set-row-label">{t.settings.updatesCheck}</span>
            <span className="set-row-hint">{updateStatusText}</span>
          </span>
          {pendingUpdate &&
          (updateStatus.kind === "available" || updateStatus.kind === "error") ? (
            <button
              type="button"
              className="set-btn"
              onClick={() => void applyUpdate()}
            >
              {t.settings.updatesInstall}
            </button>
          ) : (
            <button
              type="button"
              className="set-btn"
              onClick={() => void checkUpdates()}
              disabled={updateBusy}
            >
              {updateStatus.kind === "checking"
                ? t.settings.updatesChecking
                : t.settings.updatesCheck}
            </button>
          )}
        </div>
      </section>
    </section>
  );
}
