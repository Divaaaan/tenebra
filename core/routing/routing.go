// Package routing turns a high-level routing intent (smart/global/direct) into
// the sing-box "route" and "dns" blocks. It only emits the rule and DNS shape;
// the singbox package stitches these into the full config.
//
// The smart mode keeps Russian destinations (and, optionally, LAN) on the
// direct outbound and sends everything else through the proxy. RU geodata comes
// from the official public sing-geoip/sing-geosite rule-sets, shipped as local
// .srs binaries in Options.RuleSetDir and loaded from disk. When they are not
// there, smart emits no geo rules at all and behaves as global for the session
// — a degradation, never a download and never a reference to a file sing-box
// cannot open.
package routing

import (
	"fmt"
	"os"
	"path/filepath"
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

// Rule-set identifiers and the on-disk names of the bundled binaries. The
// geodata itself is a public release from SagerNet, not Tenebra infrastructure;
// scripts/fetch-resources.* download it at build time and write it into the
// resources directory Options.RuleSetDir points at.
//
// Every rule-set is loaded strictly as a LOCAL .srs. There is deliberately no
// remote form: sing-box resolves a remote rule-set before it will serve traffic,
// so on a network where raw.githubusercontent.com is blocked — the network this
// client exists for — the fetch times out and the process exits fatally instead
// of connecting. A missing file degrades the affected rules away (see
// geoRuleSetsReady / adBlockActive) rather than being downloaded at connect time.
const (
	ruleSetGeoIPRU   = "geoip-ru"
	ruleSetGeositeRU = "geosite-ru"

	// fileGeoIPRU and fileGeositeRU are the on-disk names of the bundled rule-set
	// binaries, resolved against RuleSetDir. They must match what
	// scripts/fetch-resources.ps1 writes into the resources directory.
	fileGeoIPRU   = "geoip-ru.srs"
	fileGeositeRU = "geosite-ru.srs"

	// ruleSetGeositeAds is the opt-in ad/tracker blocklist, referenced only when
	// its own file is present and the toggle is on (see Options.adBlockActive).
	ruleSetGeositeAds = "geosite-ads"
	fileGeositeAds    = "geosite-ads.srs"
)

// ruleSetFilePresent reports whether name is a readable file inside RuleSetDir,
// checked at the moment the config is assembled rather than once at daemon
// start.
//
// The timing is the whole point. sing-box treats a rule_set whose path it cannot
// open as a fatal config error: "parse rule-set: cannot find the path" and the
// process never starts, so every candidate node in the fallback walk dies at
// launch and the user is told "all protocols failed" — a message about the nodes
// for a fault that is entirely local. A start-of-day check cannot prevent that,
// because it answers for the directory as it was when the daemon started: an
// update that replaces the resources folder, a half-finished install, an
// antivirus quarantine or a user tidying up leaves a stale "yes" behind and every
// connect from then on is fatal. Statting per build costs two syscalls on a path
// that already spawns a process.
func (o Options) ruleSetFilePresent(name string) bool {
	if o.RuleSetDir == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(o.RuleSetDir, name))
	return err == nil && !st.IsDir()
}

// geoRuleSetsReady reports whether both RU geodata sets can be loaded right now.
// Both are required together: the route rule names them in one match, and half
// the geodata is not a usable smart split.
func (o Options) geoRuleSetsReady() bool {
	return o.ruleSetFilePresent(fileGeoIPRU) && o.ruleSetFilePresent(fileGeositeRU)
}

// smartGeoActive reports whether the RU geo split may be emitted: smart mode with
// its geodata actually on disk. When the files are missing, smart degrades to
// global — everything through the proxy — instead of referencing a rule-set that
// would stop sing-box from starting at all. Slower is a worse tunnel; fatal is no
// tunnel, and the failure surfaces as an accusation against the nodes.
func (o Options) smartGeoActive() bool {
	return o.Mode == ModeSmart && o.geoRuleSetsReady()
}

// SmartGeoDegraded reports whether smart mode is running without its geodata, so
// the caller can say so out loud. Silence here is the expensive part: the tunnel
// works, but every Russian destination now takes the long way round and nothing
// in the UI distinguishes that from the smart split doing its job.
func (o Options) SmartGeoDegraded() bool {
	return o.Mode == ModeSmart && !o.geoRuleSetsReady()
}

// MissingRuleSets names the rule-set files the current options would use but
// cannot read, as full paths when RuleSetDir is known. It exists for diagnostics:
// a warning that says which file is missing and where it was looked for is
// actionable, one that says "geodata unavailable" is not.
func (o Options) MissingRuleSets() []string {
	var want []string
	if o.Mode == ModeSmart {
		want = append(want, fileGeoIPRU, fileGeositeRU)
	}
	if o.AdBlock {
		want = append(want, fileGeositeAds)
	}
	var missing []string
	for _, f := range want {
		if o.ruleSetFilePresent(f) {
			continue
		}
		if o.RuleSetDir == "" {
			missing = append(missing, f)
			continue
		}
		missing = append(missing, filepath.Join(o.RuleSetDir, f))
	}
	return missing
}

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

	// ZapretActive marks that the DPI bypass is running alongside the tunnel.
	//
	// It flips where the censored services go. Without the bypass they must be
	// tunnelled or they do not load at all; with it they load on the direct path
	// at the ISP's own latency, and tunnelling them would add the full round trip
	// to another country for nothing — the difference measured here was 239ms
	// against 9ms, which is the whole distance between usable voice chat and
	// unusable.
	//
	// The tunnel is not redundant: it still carries everything the bypass does not
	// cover. The two split the work rather than duplicating it.
	ZapretActive bool

	// ZapretCovered is the set of domains the running bypass actually acts on,
	// read from the installed bundle's host lists (see zapret.Covered).
	//
	// It is what keeps "the bypass is on" from meaning "everything censored now
	// works directly". The bundle's lists carry YouTube, Google and Discord and
	// nothing else, while the blocked-services preset also lists Instagram, X,
	// Anthropic and OpenAI — handing those to the direct path because a bypass is
	// running does not slow them down, it breaks them, and on this ISP
	// api.anthropic.com answers a forged 403 rather than timing out, so the
	// breakage surfaces as a false authentication error in every tool above it.
	//
	// Empty means "coverage unknown": the routing layer then falls back to the
	// narrow YouTube/Discord set every published bundle is built around, rather
	// than assuming full coverage.
	ZapretCovered []string

	// UnblockServices pins the commonly-censored services (YouTube, Discord,
	// Meta, X and friends) to the proxy by domain, ahead of the geo split.
	//
	// It exists because smart mode alone does not reliably get them through. Smart
	// mode sends RU-resolved addresses direct, and several of these services
	// answer from inside Russia: `googlevideo.com`, where YouTube video actually
	// streams from, usually resolves to a Google cache node hosted at the ISP. The
	// geo rule then pins the video direct, and the user sees a connected VPN with
	// a spinner where the video should be — a failure that looks like the VPN is
	// broken when it is doing exactly what it was told.
	//
	// Matching by domain before the geo split is the only ordering that survives
	// that, which is why this is a preset rather than advice to add rules by hand.
	UnblockServices bool

	// GamesDirect keeps game clients and their launchers on the direct outbound,
	// so a match is played over the ISP path instead of through the exit node.
	//
	// This is the one split every user of a Russian ISP ends up building by hand,
	// and building it by hand is where it goes wrong: miss `riotclientservices.exe`
	// and the match never starts, add `steamwebhelper.exe` and the Steam store goes
	// blank. The preset ships the list so the common case is one switch rather than
	// a dozen remembered executable names.
	//
	// The apps it pins resolve through the direct resolver too (see
	// directSplitApps): a game routed direct while its names were answered from the
	// exit country was handed content servers on the wrong continent, which is how
	// a launcher ends up hanging on a login rather than merely running slowly.
	//
	// Games are the traffic that least needs a tunnel and suffers most from one:
	// nothing about a match is censored, while the detour adds the same latency
	// measured for voice (239ms tunnelled vs 9ms direct on the author's machine)
	// straight onto every input. Anti-cheat systems also flag the sudden change of
	// exit address, so tunnelling a game can cost a ban on top of the lag.
	//
	// It composes with SplitApps rather than replacing it: the preset's names are
	// merged into the user's exclude list, so a manually added app keeps working.
	// Inert in direct mode, and — like VoiceDirect — never applied under
	// KillSwitch, whose promise is that nothing escapes the tunnel.
	GamesDirect bool

	// VoiceDirect keeps real-time UDP (voice chat, game traffic) on the direct
	// outbound instead of tunnelling it, trading exit-IP hiding for latency.
	//
	// It exists because the tunnel is the wrong shape for real-time media. A
	// measurement on the author's machine: STUN through the tunnel answered in
	// 239ms while the direct path answered in 9ms — a ~26x difference that is the
	// entire distance between a usable voice call and an unusable one. Nothing in
	// the proxy chain can close that gap; the packets are simply travelling to
	// another country and back.
	//
	// The trade is real and belongs to the user, which is why it is off by
	// default: with it on, a voice peer sees the ISP address rather than the exit
	// node's, and a censor that blocks the voice UDP outright will break the call
	// that would otherwise have survived inside the tunnel. Signalling (the TCP/
	// HTTPS side that actually gets blocked) is untouched and keeps going through
	// the proxy, which is what makes the split useful rather than merely faster.
	//
	// It is inert in direct mode (nothing is tunnelled anyway) and is deliberately
	// NOT applied when KillSwitch is set: the kill switch's promise is that
	// nothing leaves outside the tunnel, and quietly exempting a whole class of
	// traffic would turn that promise into a lie.
	VoiceDirect bool

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

	// RuleSetDir is the absolute directory holding the bundled rule-set binaries
	// (geoip-ru.srs / geosite-ru.srs, plus geosite-ads.srs for the blocklist).
	// Every set is loaded from disk; nothing is ever downloaded at connect time.
	//
	// Presence is re-checked per file each time a config is built, so this is a
	// hint about where to look rather than a promise that anything is there. An
	// empty value, a directory that has since been replaced by an update, or a
	// single missing file all lead to the same place: the rules that would have
	// referenced it are simply not emitted.
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
	// The bypass coverage comes from files on disk, so it gets the same treatment
	// as user input: lowercased, trimmed, de-duplicated and sorted, which also
	// makes the suffix comparison in coveredByZapret a plain string match.
	n.ZapretCovered = normalizeSuffixes(n.ZapretCovered)
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

// directPinAllowed reports whether a rule that takes traffic out of the tunnel
// may be emitted at all. Every such rule — the game and voice presets, the
// services the bypass covers, the bundled RU banking/government rule presets and
// the user's own direct rules, the LAN bypass — has to ask this before it emits,
// on the route layer and on the DNS layer alike.
//
// Two answers, for different reasons:
//
//   - Direct mode tunnels nothing, so "keep this direct" is a no-op there and a
//     "force this through the tunnel" rule would re-tunnel traffic the user asked
//     to send straight out.
//   - The kill switch's promise is that nothing leaves outside the tunnel. A rule
//     that quietly exempts a class of traffic makes that promise false without the
//     user ever choosing it, and the exemptions are not small: voice UDP carries
//     the real address to whoever is on the call, and the banking/government
//     presets carry the names of the sites being visited to the ISP's resolver.
//
// It deliberately does not cover the user's own split-exclude app list. That list
// is typed in one executable at a time, so it is the explicit per-application
// choice the presets are not; the preset half merged into it still drops out,
// because gamesDirectActive asks here first.
func (o Options) directPinAllowed() bool {
	return o.Mode != ModeDirect && !o.KillSwitch
}

// strategy returns the DNS resolution strategy, or "" to omit it.
func (o Options) strategy() string {
	if o.IPv4Only {
		return "ipv4_only"
	}
	return ""
}
