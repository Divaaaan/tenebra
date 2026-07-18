import { describe, expect, it } from "vitest";

import { buildDiagnostics, scrubSecrets } from "./diagnostics";
import type { LeakCheck } from "../api";
import type { LogLine } from "../state/useTenebra";

const WHEN = new Date("2026-07-18T12:00:00.000Z");

function logLine(overrides: Partial<LogLine> = {}): LogLine {
  return {
    id: 1,
    at: WHEN,
    level: "info",
    msg: "core started",
    ...overrides,
  };
}

const CLEAN_LEAK: LeakCheck = {
  connected: true,
  ip_verdict: "ok",
  ip_message: "traffic leaves through the exit",
  public_ip: "95.217.44.101",
  country: "FI",
  source: "ipify",
  exit_server: "95.217.44.101",
  dns: { status: "ok", message: "consistent", resolvers: ["tls://1.1.1.1"] },
};

describe("scrubSecrets", () => {
  it("masks the token segment of a managed subscription URL, keeping the path shape", () => {
    const out = scrubSecrets("fetching https://vpsxd.pro/sub/abc123secret now");
    expect(out).toContain("https://vpsxd.pro/sub/***");
    expect(out).not.toContain("abc123secret");
  });

  it("covers every managed host", () => {
    const out = scrubSecrets(
      "https://vpnxd.pro/s/TOK1 https://chatakfix.ru/api/TOK2",
    );
    expect(out).not.toContain("TOK1");
    expect(out).not.toContain("TOK2");
  });

  it("masks the credential in a share link but keeps scheme and host", () => {
    const out = scrubSecrets(
      "dialing vless://d1e2f3a4-b5c6-d7e8-f9a0-b1c2d3e4f5a6@de-fra.example:443",
    );
    expect(out).toContain("vless://***@de-fra.example:443");
    expect(out).not.toContain("d1e2f3a4-b5c6-d7e8-f9a0-b1c2d3e4f5a6");
  });

  it("masks a bare UUID wherever it appears", () => {
    const out = scrubSecrets("node id 550e8400-e29b-41d4-a716-446655440000 up");
    expect(out).toBe("node id *** up");
  });

  it("leaves protocols, hosts and error text intact", () => {
    const text = "hysteria2 handshake to de-fra.example:443 failed: timeout";
    expect(scrubSecrets(text)).toBe(text);
  });
});

describe("buildDiagnostics", () => {
  it("includes the header: app version, daemon version, platform and date", () => {
    const out = buildDiagnostics({
      appVersion: "0.4.4",
      daemonVersion: "0.4.4",
      platform: "TestAgent/1.0",
      when: WHEN,
      leak: null,
      logs: [],
    });
    expect(out).toContain("App version:    0.4.4");
    expect(out).toContain("Daemon version: 0.4.4");
    expect(out).toContain("Platform:       TestAgent/1.0");
    expect(out).toContain("Generated:      2026-07-18T12:00:00.000Z");
  });

  it("reports an unknown daemon version when the field is absent", () => {
    const out = buildDiagnostics({
      appVersion: "0.4.4",
      daemonVersion: undefined,
      platform: "TestAgent/1.0",
      when: WHEN,
      leak: null,
      logs: [],
    });
    expect(out).toContain("Daemon version: unknown");
  });

  it("notes when no leak check was run this session", () => {
    const out = buildDiagnostics({
      appVersion: "0.4.4",
      daemonVersion: "0.4.4",
      platform: "TestAgent/1.0",
      when: WHEN,
      leak: null,
      logs: [],
    });
    expect(out).toContain("Not run this session.");
  });

  it("summarises the last leak check", () => {
    const out = buildDiagnostics({
      appVersion: "0.4.4",
      daemonVersion: "0.4.4",
      platform: "TestAgent/1.0",
      when: WHEN,
      leak: CLEAN_LEAK,
      logs: [],
    });
    expect(out).toContain("IP verdict:  ok");
    expect(out).toContain("Public IP:   95.217.44.101 (FI) via ipify");
    expect(out).toContain("Tunnel exit: 95.217.44.101");
    expect(out).toContain("DNS:         ok — consistent");
    expect(out).toContain("Resolvers:   tls://1.1.1.1");
  });

  it("renders each log line with an ISO timestamp and its level", () => {
    const out = buildDiagnostics({
      appVersion: "0.4.4",
      daemonVersion: "0.4.4",
      platform: "TestAgent/1.0",
      when: WHEN,
      leak: null,
      logs: [
        logLine({ id: 1, level: "info", msg: "core started" }),
        logLine({ id: 2, level: "error", msg: "handshake failed" }),
      ],
    });
    expect(out).toContain("Logs (2)");
    expect(out).toContain("[2026-07-18T12:00:00.000Z] INFO  core started");
    expect(out).toContain("[2026-07-18T12:00:00.000Z] ERROR handshake failed");
  });

  it("marks an empty log buffer", () => {
    const out = buildDiagnostics({
      appVersion: "0.4.4",
      daemonVersion: "0.4.4",
      platform: "TestAgent/1.0",
      when: WHEN,
      leak: null,
      logs: [],
    });
    expect(out).toContain("Logs (0)");
    expect(out).toContain("(empty)");
  });

  it("scrubs secrets that reached a log line", () => {
    const out = buildDiagnostics({
      appVersion: "0.4.4",
      daemonVersion: "0.4.4",
      platform: "TestAgent/1.0",
      when: WHEN,
      leak: null,
      logs: [
        logLine({
          msg: "import https://vpsxd.pro/sub/abc123secret",
        }),
      ],
    });
    expect(out).not.toContain("abc123secret");
    expect(out).toContain("https://vpsxd.pro/sub/***");
  });
});
