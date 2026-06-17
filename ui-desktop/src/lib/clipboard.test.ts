import { afterEach, describe, expect, it, vi } from "vitest";

import { ClipboardError, readClipboardText } from "./clipboard";

function setClipboard(value: unknown) {
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value,
  });
}

afterEach(() => {
  delete (navigator as { clipboard?: unknown }).clipboard;
  vi.restoreAllMocks();
});

describe("readClipboardText", () => {
  it("throws denied when the clipboard API is unavailable", async () => {
    setClipboard(undefined);
    await expect(readClipboardText()).rejects.toMatchObject({
      kind: "denied",
    });
  });

  it("throws denied when readText is missing", async () => {
    setClipboard({});
    await expect(readClipboardText()).rejects.toMatchObject({
      kind: "denied",
    });
  });

  it("throws denied when the read is refused", async () => {
    setClipboard({ readText: vi.fn().mockRejectedValue(new Error("no")) });
    await expect(readClipboardText()).rejects.toMatchObject({
      kind: "denied",
    });
  });

  it("throws empty when the clipboard holds only whitespace", async () => {
    setClipboard({ readText: vi.fn().mockResolvedValue("   \n\t ") });
    await expect(readClipboardText()).rejects.toMatchObject({
      kind: "empty",
    });
  });

  it("throws empty on a truly empty string", async () => {
    setClipboard({ readText: vi.fn().mockResolvedValue("") });
    await expect(readClipboardText()).rejects.toMatchObject({
      kind: "empty",
    });
  });

  it("returns the trimmed text on success", async () => {
    setClipboard({ readText: vi.fn().mockResolvedValue("  vless://x  ") });
    await expect(readClipboardText()).resolves.toBe("vless://x");
  });
});

describe("ClipboardError", () => {
  it("carries its kind and is an Error", () => {
    const err = new ClipboardError("empty");
    expect(err).toBeInstanceOf(Error);
    expect(err.name).toBe("ClipboardError");
    expect(err.kind).toBe("empty");
  });
});
