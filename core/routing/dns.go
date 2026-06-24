package routing

import (
	"net"
	"strconv"
	"strings"
)

// DNS returns the sing-box "dns" block. Two resolvers are configured: the
// remote one (encrypted, reached over the proxy) handles general lookups so
// they don't leak onto the local network, and the direct one resolves Russian
// domains so RU traffic kept direct also resolves direct.
//
// This emits the 1.12+ DNS schema: each server is a typed object with a "type"
// discriminator (the legacy string "address" form was removed in the 1.13/1.14
// line).
func (o Options) DNS() map[string]any {
	dns := map[string]any{
		"servers": []map[string]any{
			dnsServer(dnsRemoteTag, o.DNSRemote, tagProxy),
			dnsServer(dnsDirectTag, o.DNSDirect, tagDirect),
		},
		"rules": o.dnsRules(),
		"final": o.dnsFinal(),
	}
	if s := o.strategy(); s != "" {
		dns["strategy"] = s
	}
	return dns
}

// dnsServer converts a resolver address (tls://, https://, quic://, h3://,
// tcp://, udp://, or a bare host) into a 1.12+ typed DNS server object. The
// scheme becomes the "type"; host, port and DoH path are split out into their
// own fields.
func dnsServer(tag, addr, detour string) map[string]any {
	scheme, rest := "udp", addr
	if i := strings.Index(addr, "://"); i >= 0 {
		scheme, rest = addr[:i], addr[i+3:]
	}
	host, path := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		host, path = rest[:i], rest[i:]
	}
	server, port := host, 0
	if h, p, err := net.SplitHostPort(host); err == nil {
		if n, convErr := strconv.Atoi(p); convErr == nil {
			server, port = h, n
		}
	}
	switch scheme {
	case "https", "h3", "tls", "quic", "tcp", "udp":
	default:
		scheme = "udp"
	}
	s := map[string]any{
		"type":   scheme,
		"tag":    tag,
		"server": server,
	}
	// sing-box 1.13 rejects a DNS detour to a bare "direct" outbound as
	// meaningless — a server with no detour already dials directly — so only set
	// a detour for the proxied resolver.
	if detour != "" && detour != tagDirect {
		s["detour"] = detour
	}
	if port > 0 {
		s["server_port"] = port
	}
	if path != "" && (scheme == "https" || scheme == "h3") {
		s["path"] = path
	}
	return s
}

// dnsRules routes RU-domain lookups to the direct resolver in smart mode. In
// global and direct modes everything falls through to "final", so no per-domain
// rules are needed.
func (o Options) dnsRules() []map[string]any {
	if o.Mode != ModeSmart {
		return []map[string]any{}
	}
	return []map[string]any{
		{
			"rule_set": []string{ruleSetGeositeRU},
			"action":   "route",
			"server":   dnsDirectTag,
		},
	}
}

// dnsFinal is the fallback DNS server for lookups no rule matched. It must track
// the route final: in direct mode route.final is the direct outbound, so DNS has
// to resolve via the direct resolver too — forcing it through dns-remote (which
// detours to the proxy selector) would break resolution exactly when the user
// picks direct mode because the proxy is down. Smart and global keep dns-remote
// so general lookups stay encrypted over the proxy and don't leak to the LAN.
func (o Options) dnsFinal() string {
	if o.Mode == ModeDirect {
		return dnsDirectTag
	}
	return dnsRemoteTag
}
