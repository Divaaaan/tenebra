import { describe, expect, it } from "vitest";

import { parseBlocklistJson, parseBlocklistText, parseRuleLine, readBlocklistFile } from "./blocklist";

/**
 * Builds a File with working arrayBuffer()/text().
 *
 * jsdom's Blob implements neither, while the real webview does — so without this
 * the tests would fail on the environment rather than on the code, and the
 * reader's binary/zip detection (which reads bytes first) could not be covered
 * at all.
 */
function fileOf(name: string, body: string | Uint8Array): File {
  const bytes = typeof body === "string" ? new TextEncoder().encode(body) : body;
  const file = new File([bytes as BlobPart], name);
  Object.defineProperty(file, "arrayBuffer", {
    value: async () => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength),
  });
  Object.defineProperty(file, "text", {
    value: async () => new TextDecoder().decode(bytes),
  });
  return file;
}

describe("parseRuleLine", () => {
  it("reads the three syntaxes lists mix freely", () => {
    expect(parseRuleLine("ads.example")).toBe("ads.example");
    expect(parseRuleLine("0.0.0.0 ads.example")).toBe("ads.example");
    expect(parseRuleLine("127.0.0.1   ads.example")).toBe("ads.example");
    expect(parseRuleLine("||ads.example^")).toBe("ads.example");
    expect(parseRuleLine("||ads.example^$third-party")).toBe("ads.example");
  });

  it("drops comments, blanks and things that are not domains", () => {
    for (const line of ["", "   ", "# comment", "! adblock header", "; ini comment", "not_a_domain", "/regex/"]) {
      expect(parseRuleLine(line)).toBeNull();
    }
  });

  it("normalises leading wildcards and dots", () => {
    expect(parseRuleLine("*.ads.example")).toBe("ads.example");
    expect(parseRuleLine(".ads.example")).toBe("ads.example");
    expect(parseRuleLine("ADS.EXAMPLE")).toBe("ads.example");
  });
});

describe("parseBlocklistJson", () => {
  it("reads a sing-box rule-set", () => {
    const doc = JSON.stringify({
      version: 2,
      rules: [{ domain: ["ads.example"], domain_suffix: ["tracker.example"] }],
    });
    expect(parseBlocklistJson(doc).sort()).toEqual(["ads.example", "tracker.example"]);
  });

  it("reads a bare array and a domains object", () => {
    expect(parseBlocklistJson('["ads.example","tracker.example"]')).toHaveLength(2);
    expect(parseBlocklistJson('{"domains":["ads.example"]}')).toEqual(["ads.example"]);
  });

  it("does not harvest unrelated string fields", () => {
    // A description or a version must never become a rule.
    const doc = JSON.stringify({ name: "my.list.example", version: "1.2", rules: [] });
    expect(parseBlocklistJson(doc)).toEqual([]);
  });

  it("returns nothing for non-JSON instead of throwing", () => {
    expect(parseBlocklistJson("0.0.0.0 ads.example")).toEqual([]);
  });
});

describe("parseBlocklistText", () => {
  it("falls back to line parsing for a JSON body with no rules in it", () => {
    expect(parseBlocklistText('{"unrelated": true}')).toEqual([]);
  });

  it("de-duplicates across syntaxes", () => {
    const body = ["ads.example", "0.0.0.0 ads.example", "||ads.example^"].join("\n");
    expect(parseBlocklistText(body)).toEqual(["ads.example"]);
  });
});

describe("readBlocklistFile", () => {
  it("reads a plain list", async () => {
    const parsed = await readBlocklistFile(fileOf("hosts", "0.0.0.0 ads.example\n# comment\n"));
    expect(parsed.rules).toEqual(["ads.example"]);
  });

  // The failure from the field: a compiled .srs is binary, and reporting "no
  // rules found" sends the user hunting for a problem in a file that simply is
  // not text.
  it("says a binary file is binary rather than empty", async () => {
    const binary = new Uint8Array([0x53, 0x52, 0x53, 0x00, 0x01, 0x00, 0x02]);
    await expect(readBlocklistFile(fileOf("geosite.srs", binary))).rejects.toThrow(/двоичный/);
  });

  it("reports how much was read when nothing parsed", async () => {
    // "Wrong file" and "right file, unsupported syntax" have to be tellable
    // apart without guessing.
    await expect(readBlocklistFile(fileOf("notes.txt", "hello\nworld\n"))).rejects.toThrow(/2 строк/);
  });
});
