import { describe, expect, it } from "vitest";

import { importErrorMessage } from "./importError";
import { dictionaries } from "../i18n/strings";

const t = dictionaries.en;

describe("importErrorMessage", () => {
  it("maps a DNS / unreachable-host failure to the network message", () => {
    expect(
      importErrorMessage(
        'subscription: fetch: Get "https://host/sub": dial tcp: lookup host: no such host',
        t,
      ),
    ).toBe(t.errors.importUnreachable);
  });

  it("maps a refused connection to the network message", () => {
    expect(
      importErrorMessage(
        "dial tcp 1.2.3.4:443: connectex: No connection could be made because the target machine actively refused it",
        t,
      ),
    ).toBe(t.errors.importUnreachable);
  });

  it("maps a client timeout to the network message", () => {
    expect(
      importErrorMessage(
        "Client.Timeout exceeded while awaiting headers (context deadline exceeded)",
        t,
      ),
    ).toBe(t.errors.importUnreachable);
  });

  it("maps a non-2xx status to the status message", () => {
    expect(
      importErrorMessage("subscription: fetch: unexpected status 404 Not Found", t),
    ).toBe(t.errors.importBadStatus);
  });

  it("maps an unparseable body to the content message", () => {
    expect(importErrorMessage("subscription: no nodes in body", t)).toBe(
      t.errors.importBadContent,
    );
  });

  it("falls back to generic for an unrecognized error", () => {
    expect(importErrorMessage("kaboom", t)).toBe(t.errors.generic);
  });

  it("accepts an Error instance, not just a string", () => {
    expect(importErrorMessage(new Error("dial tcp: i/o timeout"), t)).toBe(
      t.errors.importUnreachable,
    );
  });

  it("never echoes the raw subscription URL (domain masking)", () => {
    const out = importErrorMessage(
      'Get "https://vpsxd.pro/sub/secret-token": dial tcp',
      t,
    );
    expect(out).not.toContain("vpsxd.pro");
    expect(out).not.toContain("secret-token");
  });
});
