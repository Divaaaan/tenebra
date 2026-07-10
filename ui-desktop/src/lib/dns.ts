// Client-side validation for the custom-DNS resolver inputs. It mirrors the
// core's routing.ValidDNSServer so the Settings screen can flag a malformed
// resolver with a localized hint before sending it — the core validates again and
// is the source of truth, this is only for immediate feedback.
//
// Accepted forms match the schemes the core's DNS builder parses: an optional
// scheme (tls, https, quic, h3, tcp, udp) then a host, an optional port, and — for
// DoH (https/h3) only — a path. The empty string is valid: the core substitutes
// the default resolver for it.

const DNS_SCHEMES = ["https", "h3", "tls", "quic", "tcp", "udp"];

/** The scheme tokens a resolver address may carry, for the localized hint. */
export const DNS_SCHEME_LIST = DNS_SCHEMES.join(", ");

/**
 * Whether `addr` is a resolver address the core can turn into a working DNS
 * server. Empty is valid (the core fills the default). This is a pragmatic check,
 * not a full RFC validator — it rejects obvious garbage; the core makes the final
 * call.
 */
export function isValidDnsServer(addr: string): boolean {
  if (addr === "") {
    return true; // the core substitutes the default
  }

  let scheme = "udp";
  let rest = addr;
  const sep = addr.indexOf("://");
  if (sep >= 0) {
    scheme = addr.slice(0, sep);
    rest = addr.slice(sep + 3);
  }
  if (!DNS_SCHEMES.includes(scheme)) {
    return false;
  }

  let host = rest;
  const slash = rest.indexOf("/");
  if (slash >= 0) {
    if (scheme !== "https" && scheme !== "h3") {
      return false; // a path is only meaningful for DoH
    }
    host = rest.slice(0, slash);
  }

  return isValidDnsHostPort(host);
}

/** Validate a `host`, a `host:port`, or a bracketed `[ipv6]`/`[ipv6]:port`. */
function isValidDnsHostPort(hostPort: string): boolean {
  let host = hostPort;

  // Bracketed IPv6, optionally followed by :port.
  if (host.startsWith("[")) {
    const close = host.indexOf("]");
    if (close < 0) {
      return false;
    }
    const inner = host.slice(1, close);
    const after = host.slice(close + 1);
    if (after !== "") {
      if (!after.startsWith(":") || !isValidPort(after.slice(1))) {
        return false;
      }
    }
    return isIpv6(inner);
  }

  // A single trailing :port on a host or IPv4 (a bare IPv6 has many colons and is
  // handled whole below).
  const colon = host.lastIndexOf(":");
  if (colon >= 0 && host.indexOf(":") === colon) {
    const port = host.slice(colon + 1);
    if (!isValidPort(port)) {
      return false;
    }
    host = host.slice(0, colon);
  }

  return isValidHost(host);
}

function isValidPort(p: string): boolean {
  if (!/^[0-9]+$/.test(p)) {
    return false;
  }
  const n = Number(p);
  return n >= 1 && n <= 65535;
}

/** An IP literal (v4/v6) or a DNS hostname. */
function isValidHost(host: string): boolean {
  if (host === "") {
    return false;
  }
  if (isIpv4(host) || isIpv6(host)) {
    return true;
  }
  if (host.length > 253) {
    return false;
  }
  return host.split(".").every((label) => {
    if (label.length === 0 || label.length > 63) {
      return false;
    }
    if (label.startsWith("-") || label.endsWith("-")) {
      return false;
    }
    return /^[A-Za-z0-9_-]+$/.test(label);
  });
}

function isIpv4(h: string): boolean {
  const parts = h.split(".");
  if (parts.length !== 4) {
    return false;
  }
  return parts.every((p) => /^[0-9]{1,3}$/.test(p) && Number(p) <= 255);
}

function isIpv6(h: string): boolean {
  // Loose: hex groups and colons, at least one colon. The core (net.ParseIP) is
  // authoritative; this only needs to accept plausible IPv6 and reject prose.
  return h.includes(":") && /^[0-9A-Fa-f:.]+$/.test(h);
}
