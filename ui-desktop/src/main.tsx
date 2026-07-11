import React from "react";
import ReactDOM from "react-dom/client";

import { App } from "./App";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { CrashFallback } from "./components/CrashFallback";
import { I18nProvider } from "./i18n/I18nContext";
import { ThemeProvider } from "./theme/ThemeContext";
import { applySavedTheme } from "./theme/persistence";
import { installGlobalCrashHandlers, reportWebCrash } from "./lib/webCrash";

// Self-hosted display + mono faces (bundled by Vite, never fetched at runtime —
// a remote font CDN is both a privacy leak and, from Russia, a startup-blocking
// timeout). These register the @font-face rules the tokens reference.
import "@fontsource-variable/space-grotesk";
import "@fontsource-variable/jetbrains-mono";

import "./styles/global.css";
import "./styles/ping-badge.css";
import "./styles/ping-scale.css";
import "./styles/shell.css";
import "./styles/toast.css";
import "./styles/connection.css";
import "./styles/fallback.css";
import "./styles/servers.css";
import "./styles/settings.css";
import "./styles/profiles.css";
import "./styles/logs.css";
import "./styles/crash.css";
import "./styles/simple.css";
import "./styles/eclipse.css";
import "./styles/credits.css";

// Apply the persisted theme before anything renders. index.html hardcodes
// data-theme="dark" (the canonical default must survive without JS), so a
// light-theme user would otherwise watch a dark frame until React's effects
// run. Doing it here, synchronously, keeps the first paint on the right side.
applySavedTheme();

// Catch uncaught webview errors and unhandled rejections into the local crash
// file, alongside any native panic — the same opt-in, never-networked channel the
// ErrorBoundary below feeds. Debounced to one report per session.
installGlobalCrashHandlers();

const root = document.getElementById("root");
if (!root) {
  throw new Error("root element missing");
}

ReactDOM.createRoot(root).render(
  <React.StrictMode>
    <ThemeProvider>
      <I18nProvider>
        <ErrorBoundary fallback={<CrashFallback />} onError={reportWebCrash}>
          <App />
        </ErrorBoundary>
      </I18nProvider>
    </ThemeProvider>
  </React.StrictMode>,
);
