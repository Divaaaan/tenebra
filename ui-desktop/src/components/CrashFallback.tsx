import { useI18n } from "../i18n/I18nContext";

/**
 * Full-screen fallback the {@link ErrorBoundary} shows when the UI itself throws.
 * Deliberately minimal and dependency-light: a brutalist dark/mono panel with a
 * single Reload action that reloads the webview (`location.reload()`) to recover.
 */
export function CrashFallback() {
  const { t } = useI18n();
  return (
    <div className="crash-fallback" role="alert">
      <p className="crash-fallback-title">{t.crash.fallbackTitle}</p>
      <button
        type="button"
        className="crash-fallback-reload"
        onClick={() => window.location.reload()}
      >
        {t.crash.fallbackReload}
      </button>
    </div>
  );
}
