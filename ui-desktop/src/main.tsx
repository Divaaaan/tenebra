import React from "react";
import ReactDOM from "react-dom/client";

import { App } from "./App";
import { I18nProvider } from "./i18n/I18nContext";
import { ThemeProvider } from "./theme/ThemeContext";
import { applySavedTheme } from "./theme/persistence";

// Self-hosted display + mono faces (bundled by Vite, never fetched at runtime —
// a remote font CDN is both a privacy leak and, from Russia, a startup-blocking
// timeout). These register the @font-face rules the tokens reference.
import "@fontsource-variable/space-grotesk";
import "@fontsource-variable/jetbrains-mono";

import "./styles/global.css";
import "./styles/app.css";
import "./styles/shell.css";
import "./styles/connection.css";
import "./styles/servers.css";
import "./styles/settings.css";
import "./styles/profiles.css";
import "./styles/logs.css";

// Apply the persisted theme before anything renders. index.html hardcodes
// data-theme="dark" (the canonical default must survive without JS), so a
// light-theme user would otherwise watch a dark frame until React's effects
// run. Doing it here, synchronously, keeps the first paint on the right side.
applySavedTheme();

const root = document.getElementById("root");
if (!root) {
  throw new Error("root element missing");
}

ReactDOM.createRoot(root).render(
  <React.StrictMode>
    <ThemeProvider>
      <I18nProvider>
        <App />
      </I18nProvider>
    </ThemeProvider>
  </React.StrictMode>,
);
