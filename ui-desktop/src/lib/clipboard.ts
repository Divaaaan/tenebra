// Clipboard text reads for the import flow. The desktop shell runs in WebView2,
// where navigator.clipboard.readText() is available; we wrap it so callers can
// tell an empty clipboard apart from a denied/unavailable one and show the
// matching message.

/** Reasons a clipboard read can come up short. */
export type ClipboardErrorKind = "empty" | "denied";

export class ClipboardError extends Error {
  constructor(public readonly kind: ClipboardErrorKind) {
    super(kind);
    this.name = "ClipboardError";
  }
}

/**
 * Read trimmed text from the clipboard. Throws a {@link ClipboardError}: `empty`
 * when there is nothing (or only whitespace) to paste, `denied` when the API is
 * missing or the read was refused.
 */
export async function readClipboardText(): Promise<string> {
  const readText = navigator.clipboard?.readText?.bind(navigator.clipboard);
  if (!readText) {
    throw new ClipboardError("denied");
  }

  let text: string;
  try {
    text = await readText();
  } catch {
    throw new ClipboardError("denied");
  }

  const trimmed = text.trim();
  if (!trimmed) {
    throw new ClipboardError("empty");
  }
  return trimmed;
}
