import { invoke } from "@tauri-apps/api/core";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import { dictionaries, type Language, type Strings } from "./strings";

const STORAGE_KEY = "tenebra.lang";

interface I18nValue {
  lang: Language;
  setLang: (lang: Language) => void;
  t: Strings;
}

const I18nContext = createContext<I18nValue | null>(null);

function initialLanguage(): Language {
  const saved = localStorage.getItem(STORAGE_KEY);
  if (saved === "en" || saved === "ru") {
    return saved;
  }
  // Fall back to the OS UI language, defaulting to English.
  return navigator.language.toLowerCase().startsWith("ru") ? "ru" : "en";
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Language>(initialLanguage);

  const setLang = useCallback((next: Language) => {
    setLangState(next);
    localStorage.setItem(STORAGE_KEY, next);
  }, []);

  useEffect(() => {
    document.documentElement.lang = lang;
  }, [lang]);

  useEffect(() => {
    // Mirror the active language into the native shell so the tray menu, its
    // tooltip and the desktop notifications match the app. Runs on mount (the
    // initial language) and on every change. Fire-and-forget: outside a Tauri
    // window (browser preview, tests) the command isn't there, which is fine.
    void invoke("set_language", { lang }).catch(() => {});
  }, [lang]);

  const value = useMemo<I18nValue>(
    () => ({ lang, setLang, t: dictionaries[lang] }),
    [lang, setLang],
  );

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nValue {
  const ctx = useContext(I18nContext);
  if (!ctx) {
    throw new Error("useI18n must be used inside an I18nProvider");
  }
  return ctx;
}
