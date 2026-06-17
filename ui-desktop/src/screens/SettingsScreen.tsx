import { useState, type KeyboardEvent } from "react";

import type { RoutingMode } from "../api";
import type { Tenebra } from "../state/useTenebra";
import { useI18n } from "../i18n/I18nContext";
import { useTheme } from "../theme/ThemeContext";
import type { Language } from "../i18n/strings";

interface SettingsScreenProps {
  tenebra: Tenebra;
}

export function SettingsScreen({ tenebra }: SettingsScreenProps) {
  const { t, lang, setLang } = useI18n();
  const { theme, setTheme } = useTheme();
  const [launchAtLogin, setLaunchAtLogin] = useState(false);

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

  const themeOptions: { value: "dark" | "light"; label: string }[] = [
    { value: "dark", label: t.settings.themeDark },
    { value: "light", label: t.settings.themeLight },
  ];

  const langOptions: { value: Language; label: string }[] = [
    { value: "en", label: "EN" },
    { value: "ru", label: "RU" },
  ];

  return (
    <section className="screen settings">
      <header className="screen-head">
        <h1>{t.settings.title}</h1>
      </header>

      <section className="settings-section card">
        <h2>{t.settings.routing}</h2>
        <div className="routing-group" role="radiogroup" aria-label={t.settings.routing}>
          {routingOptions.map((opt, index) => {
            const checked = routing === opt.mode;
            return (
              <button
                key={opt.mode}
                type="button"
                role="radio"
                aria-checked={checked}
                tabIndex={checked ? 0 : -1}
                className={`routing-option${checked ? " is-checked" : ""}`}
                onClick={() => void tenebra.setRouting(opt.mode)}
                onKeyDown={(e) => onRoutingKey(e, index)}
              >
                <span className="routing-radio" aria-hidden="true" />
                <span className="routing-text">
                  <span className="routing-label">{opt.label}</span>
                  <span className="routing-hint muted">{opt.hint}</span>
                </span>
              </button>
            );
          })}
        </div>
      </section>

      <section className="settings-section card">
        <h2>{t.settings.appearance}</h2>

        <div className="setting-row">
          <span className="setting-name">{t.settings.theme}</span>
          <div className="segmented" role="group" aria-label={t.settings.theme}>
            {themeOptions.map((opt) => (
              <button
                key={opt.value}
                type="button"
                aria-pressed={theme === opt.value}
                className={`segmented-item${theme === opt.value ? " is-active" : ""}`}
                onClick={() => setTheme(opt.value)}
              >
                {opt.label}
              </button>
            ))}
          </div>
        </div>

        <div className="setting-row">
          <span className="setting-name">{t.settings.language}</span>
          <div className="segmented" role="group" aria-label={t.settings.language}>
            {langOptions.map((opt) => (
              <button
                key={opt.value}
                type="button"
                aria-pressed={lang === opt.value}
                className={`segmented-item${lang === opt.value ? " is-active" : ""}`}
                onClick={() => setLang(opt.value)}
              >
                {opt.label}
              </button>
            ))}
          </div>
        </div>
      </section>

      <section className="settings-section card">
        <h2>{t.settings.startup}</h2>
        <div className="setting-row">
          <span className="setting-text">
            <span className="setting-name">{t.settings.launchAtLogin}</span>
            <span className="setting-hint muted">{t.settings.launchAtLoginHint}</span>
          </span>
          <button
            type="button"
            role="switch"
            aria-checked={launchAtLogin}
            className={`switch${launchAtLogin ? " is-on" : ""}`}
            onClick={() => setLaunchAtLogin((v) => !v)}
          >
            <span className="switch-thumb" aria-hidden="true" />
          </button>
        </div>
      </section>
    </section>
  );
}
