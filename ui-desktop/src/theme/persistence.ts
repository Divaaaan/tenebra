// Theme persistence, split out of the React context so the entry point can
// apply the saved choice during module evaluation — before React mounts and
// runs its effects. Without this a light-theme user gets a dark first frame on
// every launch, because index.html ships data-theme="dark" (dark is the
// canonical theme and must render even with JS broken) and the provider only
// corrects it from an effect.

export type Theme = "dark" | "light";

const STORAGE_KEY = "tenebra.theme";

/** The saved theme. Dark is the product default; only an explicit "light" opts out. */
export function loadTheme(): Theme {
  return localStorage.getItem(STORAGE_KEY) === "light" ? "light" : "dark";
}

export function saveTheme(theme: Theme): void {
  localStorage.setItem(STORAGE_KEY, theme);
}

/** Points the token layer (tokens.css keys off [data-theme]) at a theme. */
export function applyTheme(theme: Theme): void {
  document.documentElement.dataset.theme = theme;
}

/** Applies the saved theme to the document. main.tsx calls this before the
 * first render so the choice is visible from the first painted frame. */
export function applySavedTheme(): void {
  applyTheme(loadTheme());
}
