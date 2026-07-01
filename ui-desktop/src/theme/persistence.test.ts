import { beforeEach, describe, expect, it } from "vitest";

import { applySavedTheme, applyTheme, loadTheme, saveTheme } from "./persistence";

describe("theme persistence", () => {
  beforeEach(() => {
    localStorage.clear();
    delete document.documentElement.dataset.theme;
  });

  it("defaults to dark with nothing saved", () => {
    expect(loadTheme()).toBe("dark");
  });

  it("restores a saved light choice", () => {
    saveTheme("light");
    expect(loadTheme()).toBe("light");
  });

  it("treats junk in storage as dark", () => {
    // Only an explicit "light" opts out of the default; a stale or corrupted
    // value must not leave the UI half-themed.
    localStorage.setItem("tenebra.theme", "solarized");
    expect(loadTheme()).toBe("dark");
  });

  it("applyTheme points the token layer at the theme", () => {
    applyTheme("light");
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("applySavedTheme stamps the saved theme onto the document", () => {
    // This is the pre-render call in main.tsx: with light saved, the document
    // must flip before React ever mounts.
    saveTheme("light");
    applySavedTheme();
    expect(document.documentElement.dataset.theme).toBe("light");
  });
});
