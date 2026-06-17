import { afterEach, vi } from "vitest";
import { cleanup } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";

// Unmount React trees and reset the document between tests so component suites
// don't leak DOM or listeners into each other.
afterEach(() => {
  cleanup();
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
