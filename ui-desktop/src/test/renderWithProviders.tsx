import type { ReactElement, ReactNode } from "react";
import { render, type RenderOptions } from "@testing-library/react";

import { I18nProvider } from "../i18n/I18nContext";
import { ThemeProvider } from "../theme/ThemeContext";
import type { Language } from "../i18n/strings";

interface Options extends Omit<RenderOptions, "wrapper"> {
  /** Force the UI language so string assertions are stable. Defaults to "en". */
  lang?: Language;
}

/**
 * Render a component inside the same context providers App mounts it under, so
 * screens that call useI18n/useTheme work without a custom wrapper per test. The
 * language is pinned (via the provider's localStorage key) so tests can assert
 * on English copy regardless of the host's locale.
 */
export function renderWithProviders(ui: ReactElement, options: Options = {}) {
  const { lang = "en", ...rest } = options;
  localStorage.setItem("tenebra.lang", lang);

  function Wrapper({ children }: { children: ReactNode }) {
    return (
      <ThemeProvider>
        <I18nProvider>{children}</I18nProvider>
      </ThemeProvider>
    );
  }

  return render(ui, { wrapper: Wrapper, ...rest });
}
