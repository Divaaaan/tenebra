import { afterEach, beforeEach, vi } from "vitest";
import { cleanup } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";

// Unmount React trees and reset the document between tests so component suites
// don't leak DOM or listeners into each other.
afterEach(() => {
  cleanup();
});

// The app ships simple mode as its default — a link, an archive and one button.
// Suites that exercise the full shell (keyboard shortcuts, overlays, banners)
// need the shell, so they get it here rather than each opting in: a test about
// the kill-switch toggle should not have to know how the app decides which view
// to render. Tests that care about the mode itself set the flag explicitly.
beforeEach(() => {
  localStorage.setItem("tenebra.simpleMode", "0");
});

// jsdom ships no matchMedia. Default to "motion allowed, no match" so anything
// that reads the media query (useReducedMotion, etc.) gets a sane value without
// every test having to stub it. Tests that care about reduced motion install
// their own implementation (see test/mediaQuery.ts).
if (!window.matchMedia) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }));
}
