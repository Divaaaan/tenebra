import { describe, expect, it } from "vitest";

import { describeCoreError, dictionaries } from "./strings";

const en = dictionaries.en;
const ru = dictionaries.ru;

// The daemon answers a command it doesn't have with `unknown command "…"`
// (core/control/daemon.go), which is precisely what a background service older
// than the app does to every setting added since it was installed. That case has
// to reach the user as a cause they can act on, not as engine wording.
describe("describeCoreError", () => {
  it("names the out-of-date service for an unrecognised command", () => {
    expect(describeCoreError('unknown command "set_tls_fragment"', en)).toBe(
      en.daemon.commandUnknown,
    );
  });

  it("reads the same cause out of an Error instance", () => {
    const e = new Error('unknown command "set_multihop"');
    expect(describeCoreError(e, en)).toBe(en.daemon.commandUnknown);
  });

  it("is localized, not hardcoded to one dictionary", () => {
    expect(describeCoreError('unknown command "set_dns"', ru)).toBe(
      ru.daemon.commandUnknown,
    );
    expect(describeCoreError("boom", ru)).toBe(ru.daemon.commandFailed);
  });

  it("falls back to a plain refusal for any other rejection", () => {
    expect(describeCoreError("profile not found", en)).toBe(
      en.daemon.commandFailed,
    );
    expect(describeCoreError(new Error("timed out"), en)).toBe(
      en.daemon.commandFailed,
    );
  });

  it("never echoes the raw engine string at the user", () => {
    const raw = 'unknown command "set_auto_failover"';
    expect(describeCoreError(raw, en)).not.toContain(raw);
  });

  it("survives a rejection with no message at all", () => {
    expect(describeCoreError(undefined, en)).toBe(en.daemon.commandFailed);
    expect(describeCoreError(null, en)).toBe(en.daemon.commandFailed);
  });
});
