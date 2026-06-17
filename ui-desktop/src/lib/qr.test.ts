import { afterEach, describe, expect, it, vi } from "vitest";

import {
  decodeQrFromBlob,
  decodeQrFromClipboard,
  isQrDecodingSupported,
  QrError,
} from "./qr";

// A minimal ImageBitmap stand-in: only close() is exercised by the decoder.
function fakeBitmap() {
  return { close: vi.fn() } as unknown as ImageBitmap;
}

// Build a BarcodeDetector constructor whose instances resolve detect() to the
// given results (or reject when `reject` is set), with an optional static
// getSupportedFormats. Returned alongside the per-instance detect spy.
function fakeDetector(opts: {
  results?: { rawValue: string }[];
  reject?: boolean;
  formats?: string[] | "throw";
}) {
  const detect = vi.fn(() =>
    opts.reject
      ? Promise.reject(new Error("decoder gave up"))
      : Promise.resolve(opts.results ?? []),
  );

  class Detector {
    static getSupportedFormats =
      opts.formats === undefined
        ? undefined
        : opts.formats === "throw"
          ? () => Promise.reject(new Error("probe failed"))
          : () => Promise.resolve(opts.formats as string[]);
    detect = detect;
  }

  return { Detector, detect };
}

function setDetector(ctor: unknown) {
  (globalThis as { BarcodeDetector?: unknown }).BarcodeDetector = ctor;
}

const realCreateImageBitmap = globalThis.createImageBitmap;

afterEach(() => {
  delete (globalThis as { BarcodeDetector?: unknown }).BarcodeDetector;
  globalThis.createImageBitmap = realCreateImageBitmap;
  vi.restoreAllMocks();
});

describe("isQrDecodingSupported", () => {
  it("is false when no BarcodeDetector is present", () => {
    delete (globalThis as { BarcodeDetector?: unknown }).BarcodeDetector;
    expect(isQrDecodingSupported()).toBe(false);
  });

  it("is true once a BarcodeDetector constructor exists", () => {
    setDetector(class {});
    expect(isQrDecodingSupported()).toBe(true);
  });

  it("treats a non-function BarcodeDetector as unsupported", () => {
    setDetector({});
    expect(isQrDecodingSupported()).toBe(false);
  });
});

describe("decodeQrFromBlob", () => {
  const blob = new Blob(["x"], { type: "image/png" });

  it("throws unsupported when the API is missing", async () => {
    delete (globalThis as { BarcodeDetector?: unknown }).BarcodeDetector;
    await expect(decodeQrFromBlob(blob)).rejects.toMatchObject({
      kind: "unsupported",
    });
  });

  it("throws unsupported when the runtime supports no qr_code format", async () => {
    const { Detector } = fakeDetector({ formats: ["ean_13"] });
    setDetector(Detector);
    await expect(decodeQrFromBlob(blob)).rejects.toMatchObject({
      kind: "unsupported",
    });
  });

  it("throws unsupported when the format probe itself rejects", async () => {
    const { Detector } = fakeDetector({ formats: "throw" });
    setDetector(Detector);
    await expect(decodeQrFromBlob(blob)).rejects.toMatchObject({
      kind: "unsupported",
    });
  });

  it("returns the trimmed raw value of the first decoded code", async () => {
    const bitmap = fakeBitmap();
    globalThis.createImageBitmap = vi.fn().mockResolvedValue(bitmap);
    const { Detector } = fakeDetector({
      formats: ["qr_code"],
      results: [{ rawValue: "  vless://node\n" }],
    });
    setDetector(Detector);

    await expect(decodeQrFromBlob(blob)).resolves.toBe("vless://node");
    // The bitmap is always released, success or failure.
    expect(bitmap.close).toHaveBeenCalledTimes(1);
  });

  it("skips empty rawValues and uses the first non-empty match", async () => {
    globalThis.createImageBitmap = vi.fn().mockResolvedValue(fakeBitmap());
    const { Detector } = fakeDetector({
      formats: ["qr_code"],
      results: [{ rawValue: "" }, { rawValue: "ss://real" }],
    });
    setDetector(Detector);
    await expect(decodeQrFromBlob(blob)).resolves.toBe("ss://real");
  });

  it("throws notFound when the image holds no QR", async () => {
    const bitmap = fakeBitmap();
    globalThis.createImageBitmap = vi.fn().mockResolvedValue(bitmap);
    const { Detector } = fakeDetector({ formats: ["qr_code"], results: [] });
    setDetector(Detector);

    await expect(decodeQrFromBlob(blob)).rejects.toMatchObject({
      kind: "notFound",
    });
    expect(bitmap.close).toHaveBeenCalledTimes(1);
  });

  it("throws decodeFailed when the blob can't be turned into pixels", async () => {
    globalThis.createImageBitmap = vi
      .fn()
      .mockRejectedValue(new Error("corrupt"));
    const { Detector } = fakeDetector({ formats: ["qr_code"] });
    setDetector(Detector);
    await expect(decodeQrFromBlob(blob)).rejects.toMatchObject({
      kind: "decodeFailed",
    });
  });

  it("throws decodeFailed when detect() rejects on a valid image", async () => {
    const bitmap = fakeBitmap();
    globalThis.createImageBitmap = vi.fn().mockResolvedValue(bitmap);
    const { Detector } = fakeDetector({ formats: ["qr_code"], reject: true });
    setDetector(Detector);

    await expect(decodeQrFromBlob(blob)).rejects.toMatchObject({
      kind: "decodeFailed",
    });
    // Even on a decoder failure the bitmap is released.
    expect(bitmap.close).toHaveBeenCalledTimes(1);
  });

  it("works when getSupportedFormats is absent (skips the probe)", async () => {
    globalThis.createImageBitmap = vi.fn().mockResolvedValue(fakeBitmap());
    // formats undefined → no static getSupportedFormats on the ctor.
    const { Detector } = fakeDetector({ results: [{ rawValue: "trojan://x" }] });
    setDetector(Detector);
    await expect(decodeQrFromBlob(blob)).resolves.toBe("trojan://x");
  });
});

describe("decodeQrFromClipboard", () => {
  function setClipboard(value: unknown) {
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value,
    });
  }

  afterEach(() => {
    // Drop our override so the next test starts from jsdom's default.
    delete (navigator as { clipboard?: unknown }).clipboard;
  });

  it("throws unsupported when clipboard.read is missing", async () => {
    setClipboard({});
    await expect(decodeQrFromClipboard()).rejects.toMatchObject({
      kind: "unsupported",
    });
  });

  it("throws unsupported when the clipboard read is refused", async () => {
    setClipboard({ read: vi.fn().mockRejectedValue(new Error("denied")) });
    await expect(decodeQrFromClipboard()).rejects.toMatchObject({
      kind: "unsupported",
    });
  });

  it("decodes the first image item on the clipboard", async () => {
    const imgBlob = new Blob(["x"], { type: "image/png" });
    const item = {
      types: ["text/plain", "image/png"],
      getType: vi.fn().mockResolvedValue(imgBlob),
    };
    setClipboard({ read: vi.fn().mockResolvedValue([item]) });

    globalThis.createImageBitmap = vi.fn().mockResolvedValue(fakeBitmap());
    const { Detector } = fakeDetector({
      formats: ["qr_code"],
      results: [{ rawValue: "vless://from-clipboard" }],
    });
    setDetector(Detector);

    await expect(decodeQrFromClipboard()).resolves.toBe(
      "vless://from-clipboard",
    );
    expect(item.getType).toHaveBeenCalledWith("image/png");
  });

  it("throws notFound when no clipboard item is an image", async () => {
    const item = { types: ["text/plain"], getType: vi.fn() };
    setClipboard({ read: vi.fn().mockResolvedValue([item]) });
    await expect(decodeQrFromClipboard()).rejects.toMatchObject({
      kind: "notFound",
    });
    expect(item.getType).not.toHaveBeenCalled();
  });
});

describe("QrError", () => {
  it("carries its kind and is an Error", () => {
    const err = new QrError("notFound");
    expect(err).toBeInstanceOf(Error);
    expect(err.name).toBe("QrError");
    expect(err.kind).toBe("notFound");
  });
});
