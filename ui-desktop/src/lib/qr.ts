// QR decoding for subscription/link import. We lean on the platform's native
// BarcodeDetector instead of bundling a decoder: the desktop shell runs inside
// WebView2 (Chromium), which ships it. When it is genuinely unavailable — an
// older runtime, or a build where the format isn't supported — we surface a
// clear, typed failure so the UI can tell the user to paste the link instead.

// The DOM lib doesn't yet type BarcodeDetector, so declare the slim surface we
// use. Kept minimal on purpose: just the constructor, the static support probe,
// and detect() returning the one field we read.
interface DetectedBarcode {
  rawValue: string;
}

interface BarcodeDetectorLike {
  detect(source: CanvasImageSource | Blob | ImageData): Promise<DetectedBarcode[]>;
}

interface BarcodeDetectorCtor {
  new (options?: { formats?: string[] }): BarcodeDetectorLike;
  getSupportedFormats?: () => Promise<string[]>;
}

/** Reasons a decode can fail, mapped to a user-facing string by the caller. */
export type QrErrorKind = "unsupported" | "notFound" | "decodeFailed";

/** Carries which kind of failure occurred so the UI can pick the right copy. */
export class QrError extends Error {
  constructor(public readonly kind: QrErrorKind) {
    super(kind);
    this.name = "QrError";
  }
}

function detectorCtor(): BarcodeDetectorCtor | null {
  const ctor = (globalThis as { BarcodeDetector?: BarcodeDetectorCtor })
    .BarcodeDetector;
  return typeof ctor === "function" ? ctor : null;
}

/** True when the native QR decoder is present in this runtime. */
export function isQrDecodingSupported(): boolean {
  return detectorCtor() !== null;
}

/**
 * Decode the first QR code found in an image blob and return its raw text.
 * Throws a {@link QrError} describing the failure: `unsupported` when the API is
 * missing or can't do QR, `notFound` when the image holds no QR, and
 * `decodeFailed` when the image itself couldn't be read.
 */
export async function decodeQrFromBlob(blob: Blob): Promise<string> {
  const Ctor = detectorCtor();
  if (!Ctor) {
    throw new QrError("unsupported");
  }

  // Some runtimes expose the constructor but support no formats; treat that as
  // unsupported rather than letting detect() fail opaquely later.
  if (Ctor.getSupportedFormats) {
    try {
      const formats = await Ctor.getSupportedFormats();
      if (!formats.includes("qr_code")) {
        throw new QrError("unsupported");
      }
    } catch (err) {
      if (err instanceof QrError) {
        throw err;
      }
      throw new QrError("unsupported");
    }
  }

  // Decode the bytes into pixels first; a corrupt or non-image blob fails here,
  // which is distinct from a valid image that simply has no QR in it.
  let bitmap: ImageBitmap;
  try {
    bitmap = await createImageBitmap(blob);
  } catch {
    throw new QrError("decodeFailed");
  }

  try {
    const detector = new Ctor({ formats: ["qr_code"] });
    const found = await detector.detect(bitmap);
    const value = found.find((b) => b.rawValue)?.rawValue;
    if (!value) {
      throw new QrError("notFound");
    }
    return value.trim();
  } catch (err) {
    if (err instanceof QrError) {
      throw err;
    }
    // detect() rejecting (rather than returning []) means the decoder itself
    // gave up on the image, not that the image lacked a code.
    throw new QrError("decodeFailed");
  } finally {
    bitmap.close();
  }
}

/**
 * Pull the first image off the clipboard and decode a QR from it. Throws a
 * {@link QrError} with kind `notFound` when the clipboard holds no image (so the
 * caller can reuse the same "no QR" copy), or `unsupported` when the Clipboard
 * read API is missing or blocked.
 */
export async function decodeQrFromClipboard(): Promise<string> {
  const read = navigator.clipboard?.read?.bind(navigator.clipboard);
  if (!read) {
    throw new QrError("unsupported");
  }

  let items: ClipboardItems;
  try {
    items = await read();
  } catch {
    throw new QrError("unsupported");
  }

  for (const item of items) {
    const type = item.types.find((t) => t.startsWith("image/"));
    if (type) {
      const blob = await item.getType(type);
      return decodeQrFromBlob(blob);
    }
  }
  throw new QrError("notFound");
}
