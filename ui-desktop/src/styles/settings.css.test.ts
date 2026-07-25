import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

/**
 * Whether the settings overlay's controls can actually be clicked comes down to a
 * handful of CSS declarations that no component test can see: jsdom runs no
 * layout, so a sticky bar swallowing the clicks beneath it and a nav rail
 * clipping its own items both render "correctly" everywhere else in this suite.
 * These assertions read the stylesheet instead and pin the invariants 0.4.4
 * shipped broken — an opaque full-width sticky strip eating the ON/OFF switches
 * under it, a rail jump parking a section behind that strip, and 13 rail items
 * clipped out of a fixed-height column with no scrollbar to say so.
 *
 * The sheet comes off disk rather than through an import: Vitest stubs CSS
 * modules out, so even `?raw` hands back an empty string. The base URL goes
 * through a variable on purpose — Vite rewrites a literal
 * `new URL("./x", import.meta.url)` into a dev-server asset URL, which is no
 * longer a path anything can read.
 */
const here = import.meta.url;
const settingsCss = readFileSync(new URL("./settings.css", here), "utf8");

interface Rule {
  selector: string;
  declarations: Map<string, string>;
}

// A flat-sheet reader: settings.css has no at-rules and no nesting, so once the
// comments are gone (they carry braces of their own) every rule is
// `selector { prop: value; … }`. Deliberately dumb — a guard rail, not a CSS
// engine.
function parse(source: string): Rule[] {
  const withoutComments = source.replace(/\/\*[\s\S]*?\*\//g, "");
  return [...withoutComments.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map(
    ([, selector, body]) => ({
      selector: selector.trim(),
      declarations: new Map(
        body
          .split(";")
          .map((line) => line.trim())
          .filter((line) => line.includes(":"))
          .map((line): [string, string] => {
            const colon = line.indexOf(":");
            return [line.slice(0, colon).trim(), line.slice(colon + 1).trim()];
          }),
      ),
    }),
  );
}

const RULES = parse(settingsCss);

function declarations(selector: string): Map<string, string> {
  const found = RULES.find((r) => r.selector === selector);
  if (!found) {
    throw new Error(`settings.css has no rule for "${selector}"`);
  }
  return found.declarations;
}

describe("settings.css", () => {
  describe("the sticky close bar", () => {
    it("lets clicks through the stretch where it is empty", () => {
      expect(declarations(".set-close-bar").get("pointer-events")).toBe("none");
    });

    it("paints nothing of its own, so nothing hides under a click-through layer", () => {
      // Click-through plus an opaque background would only trade one bug for its
      // mirror image: controls that can be clicked but not seen.
      expect(declarations(".set-close-bar").has("background")).toBe(false);
    });

    it("keeps the button itself clickable, and opaque enough to mask what passes behind it", () => {
      const close = declarations(".set-close");
      expect(close.get("pointer-events")).toBe("auto");
      expect(close.get("background")).toBe("var(--bg)");
    });

    it("holds for every sticky layer in the sheet", () => {
      // The house rule for this overlay: a sticky layer is a frame, not a lid —
      // it stays click-through and the controls inside it opt back in. A future
      // sticky bar that genuinely needs to catch clicks across its full width has
      // to paint across its full width too, and change this test on purpose.
      const sticky = RULES.filter((r) => {
        const position = r.declarations.get("position");
        return position === "sticky" || position === "fixed";
      });
      expect(sticky.length).toBeGreaterThan(0);
      const catching = sticky
        .filter((r) => r.declarations.get("pointer-events") !== "none")
        .map((r) => r.selector);
      expect(catching).toEqual([]);
    });
  });

  describe("a rail jump", () => {
    it("clears the close bar without out-running the rail's own threshold", () => {
      const value = declarations(".set-section").get("scroll-margin-top") ?? "";
      expect(value).toMatch(/^\d+px$/);
      const px = Number.parseInt(value, 10);
      // Floor: the bar stands 12px + a ~27px button + 8px ≈ 47px tall, and
      // scrollIntoView({block: "start"}) would otherwise park the section top
      // right behind it.
      expect(px).toBeGreaterThanOrEqual(48);
      // Ceiling: pickActiveSection (SettingsScreen.tsx) counts a section as
      // reached within 60px of the scroll position. A larger offset would land
      // the jump short of that line and leave the rail lighting the section
      // before the one that was clicked.
      expect(px).toBeLessThanOrEqual(60);
    });
  });

  describe("the nav rail", () => {
    it("scrolls its items rather than clipping them out of reach", () => {
      const list = declarations(".set-nav-list");
      expect(list.get("overflow-y")).toMatch(/^(auto|scroll)$/);
      // Without this the list refuses to shrink below its content (a flex item's
      // automatic minimum), and the overflow lands outside the rail — under
      // .set-nav's clip — instead of inside the scroller.
      expect(list.get("min-height")).toBe("0");
    });

    it("sizes the shell to the viewport rather than to a hard constant", () => {
      // A zoomed WebView reports fewer CSS pixels than the 720px design window; a
      // fixed height would push the rail's tail out of the clipped overlay body,
      // where the scroller above can no longer help.
      expect(declarations(".set-shell").get("height")).toContain("100vh");
    });
  });
});
