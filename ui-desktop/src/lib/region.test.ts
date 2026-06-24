import { describe, expect, it } from "vitest";

import { locate, REGION_CHIPS } from "./region";

describe("locate", () => {
  it("reads a flag emoji first", () => {
    const loc = locate("🇩🇪 DE-FRA-01");
    expect(loc.country).toBe("DE");
    expect(loc.region).toBe("EU");
  });

  it("decodes a flag even with no other hint", () => {
    expect(locate("🇯🇵 fast node").region).toBe("AP");
    expect(locate("🇧🇷").region).toBe("AM");
  });

  it("resolves an airport code to a city label", () => {
    const loc = locate("NL-AMS-03");
    expect(loc.country).toBe("NL");
    expect(loc.region).toBe("EU");
    expect(loc.label).toBe("amsterdam");
  });

  it("falls back to a bare ISO country code", () => {
    const loc = locate("US 7");
    expect(loc.country).toBe("US");
    expect(loc.region).toBe("AM");
    expect(loc.label).toBe("united states");
  });

  it("matches a city keyword anywhere in the name", () => {
    expect(locate("Tokyo Premium").region).toBe("AP");
    expect(locate("Frankfurt #2").country).toBe("DE");
  });

  it("matches Russian-language country names", () => {
    expect(locate("Сервер Нидерланды").country).toBe("NL");
    expect(locate("США быстрый").country).toBe("US");
  });

  it("prefers a multi-word city keyword as the label", () => {
    expect(locate("vless new york reality").label).toBe("new york");
  });

  it("returns OTHER with no label when nothing matches", () => {
    const loc = locate("my-private-relay");
    expect(loc.region).toBe("OTHER");
    expect(loc.label).toBe("");
    expect(loc.country).toBeUndefined();
  });

  it("does not mistake a random two-letter token for a country", () => {
    // "ZZ" is not a known country; the name has no other signal.
    expect(locate("zz-test-node").region).toBe("OTHER");
  });

  it("exposes the four region chips in order", () => {
    expect(REGION_CHIPS.map((c) => c.key)).toEqual([null, "EU", "AM", "AP"]);
  });
});
