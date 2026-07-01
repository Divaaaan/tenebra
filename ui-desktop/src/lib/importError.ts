import type { Strings } from "../i18n/strings";

// Turn a raw import failure — a Go/Rust error string bubbled up through Tauri —
// into a localized, user-facing message. We deliberately never show the raw
// text: it is English, technical, and, worst, usually carries the subscription
// URL (e.g. `Get "https://host/sub/secret": dial tcp ...`), which must not spill
// onto the screen. Instead we classify it into a few plain causes the user can
// actually act on, and fall back to the generic line when nothing matches.
export function importErrorMessage(err: unknown, t: Strings): string {
  const raw =
    err && typeof err === "object" && "message" in err
      ? (err as { message: unknown }).message
      : err;
  const msg = String(raw ?? "").toLowerCase();

  // Couldn't reach the host at all — DNS, refused, timeout, TLS. This is the
  // common case behind a subscription domain that a provider is blocking.
  if (
    /dial|lookup|no such host|deadline exceeded|timeout|timed out|refused|connectex|unreachable|network is|connection reset|\beof\b|\btls\b|handshake|certificate/.test(
      msg,
    )
  ) {
    return t.errors.importUnreachable;
  }
  // Reached the server, but it answered with a non-2xx status.
  if (
    /unexpected status|forbidden|unauthorized|not found|bad gateway|service unavailable|too many requests/.test(
      msg,
    )
  ) {
    return t.errors.importBadStatus;
  }
  // Downloaded something, but it wasn't a subscription we could parse.
  if (
    /no nodes|no valid|no servers|empty|unsupported scheme|missing scheme|parse|decode|malformed|invalid/.test(
      msg,
    )
  ) {
    return t.errors.importBadContent;
  }
  return t.errors.generic;
}
