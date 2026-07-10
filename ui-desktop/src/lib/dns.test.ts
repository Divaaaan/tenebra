import { describe, expect, it } from "vitest";

import { isValidDnsServer } from "./dns";

describe("isValidDnsServer", () => {
  it("accepts empty (falls back to the default) and the parsed schemes", () => {
    for (const addr of [
      "",
      "tls://1.1.1.1",
      "tls://1.1.1.1:853",
      "https://77.88.8.8/dns-query",
      "h3://1.1.1.1/dns-query",
      "quic://dns.adguard.com",
      "tcp://8.8.8.8:53",
      "udp://8.8.8.8",
      "1.1.1.1",
      "8.8.8.8:53",
      "one.one.one.one",
      "dns.google",
      "tls://2606:4700:4700::1111",
      "tls://[2606:4700:4700::1111]:853",
    ]) {
      expect(isValidDnsServer(addr), addr).toBe(true);
    }
  });

  it("rejects unknown schemes, malformed hosts, and bad ports", () => {
    for (const addr of [
      "http://1.1.1.1", // not a DNS scheme
      "ftp://example", // not a DNS scheme
      "tls://", // empty host
      "https://", // empty host
      "not a url", // whitespace in host
      "udp://8.8.8.8:99999", // port out of range
      "udp://8.8.8.8:0", // port out of range
      "udp://8.8.8.8:abc", // non-numeric port
      "tls://1.1.1.1/path", // path on a non-DoH scheme
      "-lead.example", // label starts with hyphen
      "trail-.example", // label ends with hyphen
    ]) {
      expect(isValidDnsServer(addr), addr).toBe(false);
    }
  });
});
