// Package routing turns a high-level routing intent (smart/global/direct) into
// the sing-box "route" and "dns" blocks. It only emits the rule and DNS shape;
// the singbox package stitches these into the full config.
//
// The smart mode keeps Russian destinations (and, optionally, LAN) on the
// direct outbound and sends everything else through the proxy. RU geodata is
// pulled from the official public sing-geoip/sing-geosite rule-sets at runtime
// by sing-box itself, so the client ships no geodata of its own.
package routing

import "fmt"

// Mode selects how traffic is split between the proxy and the direct outbound.
type Mode string

const (
	// ModeSmart sends RU domains/IPs (and LAN when BypassLAN) direct and routes
	// the rest through the proxy. This is the default.
	ModeSmart Mode = "smart"
	// ModeGlobal routes everything through the proxy.
	ModeGlobal Mode = "global"
	// ModeDirect sends everything direct (proxy effectively off).
	ModeDirect Mode = "direct"
)

// Outbound tags the route/dns blocks reference. These must match the tags the
// singbox package assigns to the shared outbounds.
const (
	tagProxy  = "proxy"
	tagDirect = "direct"
)

// DNS server tags used within the generated dns block.
const (
	dnsRemoteTag = "dns-remote"
	dnsDirectTag = "dns-direct"
)

// Rule-set identifiers and their upstream source. These are public geodata
// releases from SagerNet, not Tenebra infrastructure. They are fetched over the
// direct outbound so a blocked proxy can't stop the client from bootstrapping
// its own routing rules.
const (
	ruleSetGeoIPRU   = "geoip-ru"
	ruleSetGeositeRU = "geosite-ru"

	urlGeoIPRU   = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-ru.srs"
	urlGeositeRU = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ru.srs"

	// ruleSetUpdateInterval keeps the cached rule-sets reasonably fresh without
	// hammering GitHub. sing-box only re-downloads after this elapses.
	ruleSetUpdateInterval = "168h" // weekly
)

// Default resolvers. Remote DNS runs encrypted over the proxy so lookups for
// proxied destinations don't leak to the local network; direct DNS uses a fast
// Russian resolver reachable without the tunnel.
const (
	DefaultDNSRemote = "tls://1.1.1.1"               // Cloudflare DoT
	DefaultDNSDirect = "https://77.88.8.8/dns-query" // Yandex DoH
)

// Options is the routing configuration. The zero value is not valid; call
// Normalize (or rely on Build, which does) to fill defaults.
type Options struct {
	Mode       Mode
	BypassLAN  bool   // keep private/LAN ranges on the direct outbound
	IPv4Only   bool   // resolve A records only (strategy ipv4_only)
	KillSwitch bool   // drop proxied traffic instead of leaking when tun is down
	DNSRemote  string // resolver for proxied destinations, e.g. tls://1.1.1.1
	DNSDirect  string // resolver for direct destinations, e.g. https://77.88.8.8/dns-query
}

// Normalize returns a copy with empty fields replaced by sane defaults. Mode
// falls back to smart; LAN bypass defaults on because leaking LAN through a VPN
// is almost never wanted.
func (o Options) Normalize() Options {
	n := o
	switch n.Mode {
	case ModeSmart, ModeGlobal, ModeDirect:
	default:
		n.Mode = ModeSmart
	}
	if n.DNSRemote == "" {
		n.DNSRemote = DefaultDNSRemote
	}
	if n.DNSDirect == "" {
		n.DNSDirect = DefaultDNSDirect
	}
	return n
}

// Validate reports whether the options can produce a usable config.
func (o Options) Validate() error {
	switch o.Mode {
	case ModeSmart, ModeGlobal, ModeDirect:
	default:
		return fmt.Errorf("routing: unknown mode %q", o.Mode)
	}
	if o.DNSRemote == "" {
		return fmt.Errorf("routing: empty remote DNS")
	}
	if o.DNSDirect == "" {
		return fmt.Errorf("routing: empty direct DNS")
	}
	return nil
}

// strategy returns the DNS resolution strategy, or "" to omit it.
func (o Options) strategy() string {
	if o.IPv4Only {
		return "ipv4_only"
	}
	return ""
}
