import React from "react";
import ReactDOM from "react-dom/client";

import { App } from "./App";
import { I18nProvider } from "./i18n/I18nContext";
import { ThemeProvider } from "./theme/ThemeContext";

import "./styles/global.css";
import "./styles/app.css";

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
