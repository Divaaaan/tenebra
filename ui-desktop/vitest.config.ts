import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Test config kept separate from vite.config.ts so the build never carries the
// jsdom/test toolchain. The front end has no real backend in tests: every test
// mocks @tauri-apps/api (invoke/listen) or the api module, so nothing here
// touches Tauri, a socket, or the network.
export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    // Pin the timezone so date/expiry math is identical on every host and in CI.
    // The formatters compare local calendar days, so an unpinned TZ would make
    // "today"/"tomorrow" assertions flap.
    env: { TZ: "UTC" },
    include: ["src/**/*.{test,spec}.{ts,tsx}"],
    // Deterministic by construction: no global fake timers here. Tests that need
    // them opt in per-suite with vi.useFakeTimers so the rest stay simple.
    clearMocks: true,
    restoreMocks: true,
    coverage: {
      provider: "v8",
      include: ["src/**/*.{ts,tsx}"],
      exclude: [
        "src/**/*.{test,spec}.{ts,tsx}",
        "src/test/**",
        "src/main.tsx",
        "src/vite-env.d.ts",
      ],
    },
  },
});
