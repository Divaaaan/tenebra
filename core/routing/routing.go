// Package routing turns a high-level routing intent (smart/global/direct) into
// the sing-box "route" and "dns" blocks. It only emits the rule and DNS shape;
// the singbox package stitches these into the full config.
//
// The smart mode keeps Russian destinations (and, optionally, LAN) on the
// direct outbound and sends everything else through the proxy. RU geodata comes
// from the official public sing-geoip/sing-geosite rule-sets: when the client
// ships them locally (Options.RuleSetDir) sing-box loads them from disk at
// startup; otherwise it fetches them at runtime over the direct outbound.
package routing

import (
	"fmt"
	"sort"
	"strings"
)

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

// SplitMode selects per-application split tunnelling on top of the base Mode.
// Apps are matched by their executable file name (process_name), e.g.
// "chrome.exe". The default is off, which leaves the base routing untouched.
type SplitMode string

const (
	// SplitOff disables per-app split tunnelling; the base Mode decides routing.
	SplitOff SplitMode = "off"
	// SplitExclude sends the listed apps direct (out of the tunnel) and lets
	// everything else follow the normal routing for the base Mode.
	SplitExclude SplitMode = "exclude"
	// SplitInclude routes only the listed apps through the proxy and sends
	// everything else direct.
	SplitInclude SplitMode = "include"
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
// releases from SagerNet, not Tenebra infrastructure. When the client ships the
// .srs files locally (RuleSetDir set) they load from disk; otherwise they are
// fetched over the direct outbound so a blocked proxy can't stop the client from
// bootstrapping its own routing rules.
const (
	ruleSetGeoIPRU   = "geoip-ru"
	ruleSetGeositeRU = "geosite-ru"

	urlGeoIPRU   = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-ru.srs"
	urlGeositeRU = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ru.srs"

	// fileGeoIPRU and fileGeositeRU are the on-disk names of the bundled rule-set
	// binaries, resolved against RuleSetDir. They must match what
	// scripts/fetch-resources.ps1 writes into the resources directory.
	fileGeoIPRU   = "geoip-ru.srs"
	fileGeositeRU = "geosite-ru.srs"

	// ruleSetGeositeAds is the opt-in ad/tracker blocklist. It is loaded ONLY as a
	// local bundled .srs and, unlike the RU sets, has no remote URL fallback on
	// purpose: a remote rule-set blocks sing-box at startup (the freeze the local
	// sets already fix), and the blocklist is the exact case that must never
	// reintroduce it. fileGeositeAds ships via scripts/fetch-resources.* and is
	// referenced only when RuleSetDir is set (see Options.adBlockActive).
	ruleSetGeositeAds = "geosite-ads"
	fileGeositeAds    = "geosite-ads.srs"

	// ruleSetUpdateInterval keeps the cached rule-sets reasonably fresh without
	// hammering GitHub. sing-box only re-downloads after this elapses. It applies
	// only to the remote form (no RuleSetDir).
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
	Mode        Mode
	BypassLAN   bool   // keep private/LAN ranges on the direct outbound
	IPv4Only    bool   // resolve A records only (strategy ipv4_only)
	KillSwitch  bool   // drop proxied traffic instead of leaking when tun is down
	TLSFragment bool   // force TLS ClientHello fragmentation on every TLS outbound
	DNSRemote   string // resolver for proxied destinations, e.g. tls://1.1.1.1
	DNSDirect   string // resolver for direct destinations, e.g. https://77.88.8.8/dns-query

	// AdBlock opts into DNS-level ad/tracker blocking: matching lookups are
	// sinkholed (answered REFUSED) before any routing rule, in every mode. It is
	// off by default. It only takes effect when the bundled blocklist is present
	// (RuleSetDir set) — see adBlockActive — because the blocklist ships strictly
	// as a local rule-set and is never fetched remotely.
	AdBlock bool

	// SplitMode and SplitApps configure per-application split tunnelling. Apps
	// are matched on their executable file name (process_name). SplitMode off
	// (the zero-equivalent after Normalize) leaves base routing untouched.
	SplitMode SplitMode
	SplitApps []string // executable names, normalized to lowercase

	// RulesDirect and RulesProxy are user-defined domain-suffix routing rules:
	// destinations matching RulesDirect are pinned to the direct outbound, those
	// matching RulesProxy to the proxy. They are normalized (lowercased, trimmed,
	// de-duplicated, sorted) like SplitApps and are emitted after the per-app split
	// and before the geo split, so a user rule beats the RU geo preset. They are
	// inert in direct mode, where nothing is tunnelled (see rulesActive).
	RulesDirect []string
	RulesProxy  []string

	// PresetRuBanking and PresetRuGov opt into the bundled direct-rule presets
	// (major Russian banking / government domains). When on, their suffixes are
	// merged into the direct rules. Off by default. See rules.go for the lists and
	// why those destinations are kept off the tunnel.
	PresetRuBanking bool
	PresetRuGov     bool

	// Multihop, when true, chains the proxy through two of the profile's nodes:
	// the exit outbound carries detour=<MultihopEntry> so traffic leaves via the
	// entry node first and then the exit node, and the selector/route final point
	// at the exit. MultihopEntry and MultihopExit are the builder outbound tags
	// (what singbox.sanitizeTag assigns) of the two chosen nodes, already resolved
	// from the user's stable server-ID selection by the control layer — the builder
	// works only in tags. Multihop is inert unless both tags are set, distinct, and
	// resolve to regular built outbounds; the builder then falls back to the normal
	// selector, so a stale or unresolvable selection degrades to a single hop rather
	// than a broken config.
	Multihop      bool
	MultihopEntry string
	MultihopExit  string

	// RuleSetDir is the absolute directory holding the bundled RU rule-set
	// binaries (geoip-ru.srs / geosite-ru.srs). When set, smart mode references
	// them as local rule-sets loaded from disk, so sing-box starts instantly and
	// never blocks on a GitHub download. Empty keeps the legacy remote behaviour:
	// sing-box fetches the .srs over the direct outbound at startup. The daemon
	// only sets this once it has confirmed both files exist there.
	RuleSetDir string
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
	switch n.SplitMode {
	case SplitExclude, SplitInclude:
	default:
		n.SplitMode = SplitOff
	}
	n.SplitApps = normalizeApps(n.SplitApps)
	// An empty app list is kept as the user's mode choice rather than collapsed
	// to off: the config emitters (route/dns) already no-op on an empty list, and
	// collapsing here made the Settings UI dead — the mode snapped back to off
	// before the user could add the first app (the app editor only shows for an
	// active mode).
	// Normalize the custom rule suffixes the same way, so the reported state and
	// the emitted config stay stable regardless of input order or casing.
	n.RulesDirect = normalizeSuffixes(n.RulesDirect)
	n.RulesProxy = normalizeSuffixes(n.RulesProxy)
	return n
}

// normalizeApps lowercases, trims, de-duplicates and sorts executable names so
// the rule output and persisted state are stable regardless of input order or
// casing. process_name matching in sing-box is case-insensitive on the file
// name, so lowercasing here is safe and keeps the config deterministic.
func normalizeApps(apps []string) []string {
	if len(apps) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(apps))
	out := make([]string, 0, len(apps))
	for _, a := range apps {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if _, dup := seen[a]; dup {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
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
	switch o.SplitMode {
	case "", SplitOff, SplitExclude, SplitInclude:
		// Empty is the unset zero value and means off; Normalize canonicalizes it.
	default:
		return fmt.Errorf("routing: unknown split mode %q", o.SplitMode)
	}
	// Reject a malformed custom rule suffix so garbage can never reach sing-box.
	// The control layer already refuses one at the command boundary; this is the
	// second net for any caller that builds Options directly.
	for _, s := range o.RulesDirect {
		if !ValidDomainSuffix(s) {
			return fmt.Errorf("routing: invalid direct rule suffix %q", s)
		}
	}
	for _, s := range o.RulesProxy {
		if !ValidDomainSuffix(s) {
			return fmt.Errorf("routing: invalid proxy rule suffix %q", s)
		}
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
