import { describe, expect, it } from "vitest";

import { isValidDomainSuffix } from "./rules";

describe("isValidDomainSuffix", () => {
  it("accepts bare domain suffixes, tolerating case and surrounding space", () => {
    for (const s of [
      "example.com",
      "sberbank.ru",
      "a.b.c.example.co.uk",
      "my-host.example",
      "host1.example2.com",
      "localhost",
      "Sberbank.RU", // normalizable to lowercase
      "  vtb.ru  ", // trimmed
    ]) {
      expect(isValidDomainSuffix(s), s).toBe(true);
    }
  });

  it("rejects empty, malformed, and non-domain input", () => {
    for (const s of [
      "",
      "   ",
      ".com", // leading dot
      "com.", // trailing dot
      "-lead.example", // leading hyphen
      "example.com-", // trailing hyphen
      "https://x.com", // scheme
      "x.com/path", // slash
      "x.com:443", // port
      "user@x.com", // at-sign
      "has space.com", // whitespace
      "123.456", // no letter
      "пример.рф", // non-ASCII
    ]) {
      expect(isValidDomainSuffix(s), s).toBe(false);
    }
  });
});
