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

// dnsRules builds the ordered "dns".rules. Ad/tracker blocking comes first so a
// blocked lookup is sinkholed in every mode, before any routing rule can send it
// on. Next, apps pinned to the proxy by include split tunnelling are forced to
// resolve over the proxy (see below). Smart mode then resolves RU-domain lookups
// through the direct resolver so RU traffic kept direct also resolves direct. In
// global and direct modes with none of those active, everything falls through to
// "final", so the list is empty.
func (o Options) dnsRules() []map[string]any {
	rules := []map[string]any{}
	if o.adBlockActive() {
		rules = append(rules, dnsRejectRule(ruleSetGeositeAds))
	}
	// Apps pinned to the proxy by include split tunnelling must resolve over the
	// proxy too, or their DNS leaks. The route final governs where unmatched DNS
	// goes: in smart/global it is dns-remote (over the proxy), so an included
	// app's lookups already stay in the tunnel — but in direct mode dnsFinal is
	// dns-direct, which resolves on the local network path outside the tunnel.
	// Without this rule a tunnelled app in direct mode would send its traffic
	// through the proxy while its DNS queries went out direct, revealing every
	// domain it visits to the local resolver/ISP. Match the same process names
	// the route layer pins to the proxy and send their DNS to dns-remote. It sits
	// before the smart RU rule so a tunnelled app's DNS always follows the tunnel
	// (matching where its traffic goes) rather than being pulled direct by the RU
	// rule; in smart/global it is a harmless restatement of the existing final.
	if o.SplitMode == SplitInclude && len(o.SplitApps) > 0 {
		rules = append(rules, map[string]any{
			"process_name": o.SplitApps,
			"action":       "route",
			"server":       dnsRemoteTag,
		})
	}
	// Mirror the route-layer custom/preset rules onto DNS so each domain resolves
	// through the resolver its traffic uses: a domain pinned direct resolves via
	// dns-direct, one pinned to the proxy via dns-remote. Without this a domain
	// forced direct would still resolve over the proxy resolver (or vice versa) —
	// the same split-tunnel DNS mismatch the include rule above fixes. Placed
	// before the smart RU rule so a user rule wins over the RU preset, matching the
	// route order; both are empty in direct mode (rulesActive).
	if direct := o.directRuleSuffixes(); len(direct) > 0 {
		rules = append(rules, map[string]any{
			"domain_suffix": direct,
			"action":        "route",
			"server":        dnsDirectTag,
		})
	}
	if proxy := o.proxyRuleSuffixes(); len(proxy) > 0 {
		rules = append(rules, map[string]any{
			"domain_suffix": proxy,
			"action":        "route",
			"server":        dnsRemoteTag,
		})
	}
	if o.Mode == ModeSmart {
		rules = append(rules, map[string]any{
			"rule_set": []string{ruleSetGeositeRU},
			"action":   "route",
			"server":   dnsDirectTag,
		})
	}
	return rules
}

// adBlockActive reports whether ad/tracker DNS blocking should be emitted. It
// requires both the opt-in toggle and a local rule-set directory: the blocklist
// ships ONLY as a bundled local .srs (never a remote rule-set, which would block
// sing-box at startup), so without RuleSetDir there is nothing to match and the
// feature stays inert rather than referencing an undefined rule-set — which would
// FATAL the config. The desktop app always sets RuleSetDir in a real install, so
// this gate only makes ad-blocking a no-op in dev builds missing the bundled file.
func (o Options) adBlockActive() bool {
	return o.AdBlock && o.RuleSetDir != ""
}

// dnsRejectRule sinkholes every lookup matching the named domain rule-set. The
// reject action replies REFUSED (method "default"), so a blocked name fails fast
// instead of resolving; no_drop keeps it REFUSED under load rather than letting
// sing-box's built-in rate-limit silently switch to a slow, timing-out drop after
// 50 rejects in 30s — an ad-heavy page trips that easily.
func dnsRejectRule(ruleSet string) map[string]any {
	return map[string]any{
		"rule_set": []string{ruleSet},
		"action":   "reject",
		"method":   "default",
		"no_drop":  true,
	}
}

// ValidDNSServer reports whether addr is a resolver address DNS() can turn into a
// working typed server. Accepted forms mirror dnsServer's parser: an optional
// scheme (tls, https, quic, h3, tcp, udp) then a host, an optional port, and —
// for DoH only — a path. The empty string is valid: Normalize substitutes the
// default resolver for it. Anything else (an unknown scheme, an empty or
// malformed host, a non-numeric or out-of-range port, a path on a non-DoH scheme)
// is rejected so the daemon can refuse garbage before it ever reaches sing-box.
func ValidDNSServer(addr string) bool {
	if addr == "" {
		return true // Normalize fills the default
	}
	scheme, rest := "udp", addr
	if i := strings.Index(addr, "://"); i >= 0 {
		scheme, rest = addr[:i], addr[i+3:]
	}
	switch scheme {
	case "https", "h3", "tls", "quic", "tcp", "udp":
	default:
		return false
	}
	host := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		if scheme != "https" && scheme != "h3" {
			return false // a path is only meaningful for DoH (https/h3)
		}
		host = rest[:i]
	}
	// Split an optional port; when present it must be numeric and in range. A bare
	// IPv6 literal without a port trips SplitHostPort ("too many colons"), which is
	// fine — host is then validated whole below.
	if h, p, err := net.SplitHostPort(host); err == nil {
		n, convErr := strconv.Atoi(p)
		if convErr != nil || n < 1 || n > 65535 {
			return false
		}
		host = h
	}
	return validDNSHost(host)
}

// validDNSHost reports whether h is a plausible resolver host: an IP literal (v4,
// v6, or bracketed v6) or a DNS hostname. It rejects obvious garbage — empty,
// whitespace, illegal characters, over-long labels — without being a full RFC
// validator; sing-box makes the final call, this just keeps nonsense out of the
// config.
func validDNSHost(h string) bool {
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if h == "" {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	if len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
				c >= '0' && c <= '9' || c == '-' || c == '_'
			if !ok {
				return false
			}
		}
	}
	return true
}

// dnsFinal is the fallback DNS server for lookups no rule matched. It must track
// the route final: in direct mode route.final is the direct outbound, so DNS has
// to resolve via the direct resolver too — forcing it through dns-remote (which
// detours to the proxy selector) would break resolution exactly when the user
// picks direct mode because the proxy is down. Smart and global keep dns-remote
// so general lookups stay encrypted over the proxy and don't leak to the LAN.
//
// This is the *unmatched* final only: apps pinned to the proxy by include split
// tunnelling are handled earlier by a process_name rule in dnsRules, so the
// direct final here never pulls a tunnelled app's DNS out of the tunnel.
func (o Options) dnsFinal() string {
	if o.Mode == ModeDirect {
		return dnsDirectTag
	}
	return dnsRemoteTag
}
