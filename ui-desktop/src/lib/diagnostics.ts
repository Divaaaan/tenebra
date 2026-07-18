// Builds the single-block diagnostics text the logs screen copies to the
// clipboard, so a user can paste it into a bug report when something misbehaves.
// It is assembled from what is already on screen — the two build versions, the
// browser/OS string, the last leak check and the log buffer — and nothing is
// ever sent anywhere; the user pastes it themselves.
//
// Everything is run through scrubSecrets before it leaves this module, so a
// subscription token or a node credential that happened to reach a log line
// doesn't ride along into a chat. Stack traces, protocol names and error text
// are left intact — those are the point of a diagnostics dump.

import type { LeakCheck } from "../api";
import type { LogLine } from "../state/useTenebra";

export interface DiagnosticsInput {
  /** The app build, from the __APP_VERSION__ define. */
  appVersion: string;
  /** The daemon's reported build; null/undefined when it hasn't said (≤0.4.3). */
  daemonVersion: string | null | undefined;
  /** navigator.userAgent — the OS/engine line. We don't reach for a plugin. */
  platform: string;
  /** When the bundle was taken. */
  when: Date;
  /** The last leak check of the session, or null when none was run. */
  leak: LeakCheck | null;
  /** The log console buffer, oldest first. */
  logs: LogLine[];
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// Managed subscription hosts whose links carry a per-user token as the last path
// segment. We keep the host and the path shape (useful context) and drop only
// the token.
const MANAGED_HOSTS = ["vpsxd.pro", "vpnxd.pro", "chatakfix"];

const MANAGED_TOKEN = new RegExp(
  `(https?://\\S*(?:${MANAGED_HOSTS.map(escapeRegExp).join("|")})\\S*/)([^\\s/?#]+)`,
  "gi",
);

// Share links carry the node credential (a UUID or password) as the userinfo
// before the @. Keep the scheme and the host — they say what and where — and
// mask the secret.
const SHARE_LINK_USERINFO =
  /\b(vless|vmess|trojan|ss|ssr|hysteria2?|hy2|tuic|socks5?):\/\/[^\s/@]+@/gi;

// A bare UUID is almost always a node credential (VLESS/VMess id) when it turns
// up in this app's logs; mask it wherever it appears.
const UUID =
  /\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi;

/**
 * Mask subscription tokens and node credentials in a block of text, leaving the
 * rest (protocols, hosts, errors, stack traces) readable. Deliberately light:
 * it targets the few shapes that actually carry a secret, not everything that
 * looks vaguely sensitive.
 */
export function scrubSecrets(text: string): string {
  return text
    .replace(MANAGED_TOKEN, "$1***")
    .replace(SHARE_LINK_USERINFO, "$1://***@")
    .replace(UUID, "***");
}

function leakLines(leak: LeakCheck): string[] {
  const out: string[] = [];
  out.push(`Connected:   ${leak.connected ? "yes" : "no"}`);
  out.push(
    `IP verdict:  ${leak.ip_verdict}${
      leak.ip_message ? ` — ${leak.ip_message}` : ""
    }`,
  );
  if (leak.public_ip) {
    const country = leak.country ? ` (${leak.country})` : "";
    const source = leak.source ? ` via ${leak.source}` : "";
    out.push(`Public IP:   ${leak.public_ip}${country}${source}`);
  }
  if (leak.exit_server) {
    out.push(`Tunnel exit: ${leak.exit_server}`);
  }
  out.push(
    `DNS:         ${leak.dns.status}${
      leak.dns.message ? ` — ${leak.dns.message}` : ""
    }`,
  );
  if (leak.dns.resolvers && leak.dns.resolvers.length > 0) {
    out.push(`Resolvers:   ${leak.dns.resolvers.join(", ")}`);
  }
  return out;
}

/**
 * Assemble the diagnostics bundle: a fixed-format, developer-facing text block
 * (header, last leak check, full log buffer) with secrets masked. The format is
 * intentionally the same in every UI language — it is a report a maintainer
 * reads, not interface chrome.
 */
export function buildDiagnostics(input: DiagnosticsInput): string {
  const { appVersion, daemonVersion, platform, when, leak, logs } = input;

  const lines: string[] = [
    "Tenebra desktop diagnostics",
    "===========================",
    `App version:    ${appVersion}`,
    `Daemon version: ${daemonVersion ?? "unknown"}`,
    `Platform:       ${platform}`,
    `Generated:      ${when.toISOString()}`,
    "",
    "Leak check",
    "----------",
    ...(leak ? leakLines(leak) : ["Not run this session."]),
    "",
    `Logs (${logs.length})`,
    "----------",
    ...(logs.length === 0
      ? ["(empty)"]
      : logs.map(
          (line) =>
            `[${line.at.toISOString()}] ${line.level
              .toUpperCase()
              .padEnd(5)} ${line.msg}`,
        )),
  ];

  return scrubSecrets(lines.join("\n"));
}
