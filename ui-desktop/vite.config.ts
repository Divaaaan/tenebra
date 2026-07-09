import { readFileSync } from "node:fs";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The Tauri shell loads the front end from a fixed dev server port and expects
// the production bundle in `dist`. Vite's own env vars are exposed under
// VITE_*; Tauri injects TAURI_* which we leave untouched.
const host = process.env.TAURI_DEV_HOST;

// The version the UI shows comes from package.json, which scripts/set-version.mjs
// keeps in lockstep with tauri.conf.json and Cargo.toml — no per-release edits
// in component code.
const { version } = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8"));

export default defineConfig({
  plugins: [react()],
  define: {
    __APP_VERSION__: JSON.stringify(version),
  },

  // Tauri serves the dev build over a fixed port so the Rust side can point at
  // it. Failing instead of falling back to a random port keeps the two in sync.
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
    host: host ?? false,
    hmr: host
      ? {
          protocol: "ws",
          host,
          port: 1421,
        }
      : undefined,
    watch: {
      // The Rust side is rebuilt by cargo, not Vite.
      ignored: ["**/src-tauri/**"],
    },
  },

  build: {
    // WebView2 on Windows tracks evergreen Chromium; a modern target keeps the
    // bundle small without shipping legacy polyfills.
    target: "es2021",
    sourcemap: false,
  },
});
