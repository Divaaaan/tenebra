package routing

import (
	"path/filepath"

	"github.com/Divaaaan/tenebra/core/dpi"
)

// RouteRuleSets returns the rule_set definitions the route/dns blocks reference
// by tag. Only smart mode needs them (global/direct match no geodata), so the
// other modes get an empty slice and sing-box loads/downloads nothing.
//
// When RuleSetDir is set, the sets are local .srs binaries loaded from disk, so
// sing-box never blocks startup on a network fetch. When it is empty, they are
// remote .srs binaries pulled through the direct outbound so the download
// survives a blocked proxy — the original behaviour, kept as a fallback for dev
// builds or installs missing the bundled files.
func (o Options) RouteRuleSets() []map[string]any {
	local := func(tag, file string) map[string]any {
		return map[string]any{
			"type":   "local",
			"tag":    tag,
			"format": "binary",
			"path":   filepath.Join(o.RuleSetDir, file),
		}
	}

	var sets []map[string]any

	// Smart mode needs the RU geodata (local when the .srs are bundled, else
	// remote through the direct outbound). Global and direct match no geodata, so
	// they reference none of it.
	if o.Mode == ModeSmart {
		if o.RuleSetDir != "" {
			sets = append(sets,
				local(ruleSetGeoIPRU, fileGeoIPRU),
				local(ruleSetGeositeRU, fileGeositeRU),
			)
		} else {
			remote := func(tag, url string) map[string]any {
				return map[string]any{
					"type":            "remote",
					"tag":             tag,
					"format":          "binary",
					"url":             url,
					"download_detour": tagDirect,
					"update_interval": ruleSetUpdateInterval,
				}
			}
			sets = append(sets,
				remote(ruleSetGeoIPRU, urlGeoIPRU),
				remote(ruleSetGeositeRU, urlGeositeRU),
			)
		}
	}

	// The ad/tracker blocklist applies in every mode when opted in. The dns reject
	// rule references it by tag, so it must be defined here. adBlockActive already
	// requires RuleSetDir, so it is emitted strictly as a local set and can never
	// reintroduce a startup-blocking remote fetch.
	if o.adBlockActive() {
		sets = append(sets, local(ruleSetGeositeAds, fileGeositeAds))
	}

	return sets
}

// RouteRules returns the ordered "route".rules. Order matters: DNS hijack and
// sniffing come first, then the bypass helper's own loop guard, then per-app
// split tunnelling, then the mode-specific split, and the proxy selector is the
// route "final" (set by the singbox package), so anything unmatched here flows
// through the route final.
//
// Split tunnelling sits above the geo split so a per-app decision wins over the
// smart/global RU rules:
//   - exclude: listed apps are pinned to the untunnelled outbound early (see
//     internetDirect); everything else keeps following the base mode.
//   - include: listed apps are pinned to the proxy early and the base proxy
//     split is dropped, because in include mode the route final is untunnelled
//     (see FinalOutbound) so only the listed apps reach the proxy. The LAN bypass
//     still applies — it only ever sends traffic direct, which include already
//     does for everything unlisted, so it stays harmless and consistent.
func (o Options) RouteRules() []map[string]any {
	rules := []map[string]any{
		// Capture DNS queries from the tun and answer them through the dns block
		// rather than letting them escape to whatever resolver the app picked.
		{"action": "sniff"},
		{"protocol": "dns", "action": "hijack-dns"},
	}

	// The bypass helper's own sockets must leave on the plain direct outbound. Its
	// traffic enters the tun like any other process, so without this rule the
	// connections it opens on behalf of the tunnel's direct half would be handed
	// straight back to it — a loop that hangs every untunnelled destination. It sits
	// ahead of every routing rule because there is no case where the helper's own
	// traffic should be routed by them.
	if o.DPIBypass {
		rules = append(rules,
			route(map[string]any{"process_name": []string{dpiBinaryName()}}, tagDirect),
		)
	}

	// Per-app split tunnelling. process_name matches the executable file name,
	// e.g. "chrome.exe". Placed before the geo split so an app rule wins.
	switch o.SplitMode {
	case SplitExclude:
		if len(o.SplitApps) > 0 {
			rules = append(rules,
				route(map[string]any{"process_name": o.SplitApps}, o.internetDirect()),
			)
		}
	case SplitInclude:
		if len(o.SplitApps) > 0 {
			rules = append(rules,
				route(map[string]any{"process_name": o.SplitApps}, tagProxy),
			)
		}
	}

	// In include mode the route final is untunnelled and only the listed apps are
	// pinned to the proxy above, so the base proxy split must not run — it would
	// otherwise hand unlisted traffic to the proxy and defeat "only these apps".
	includeOnly := o.SplitMode == SplitInclude && len(o.SplitApps) > 0

	// Custom and preset domain rules sit here: after the per-app split (an app
	// rule still wins) and before the geo split (a user rule beats the RU geo
	// preset). Direct rules first, then proxy rules, each collapsed into a single
	// domain_suffix match. Both are empty in direct mode (rulesActive), where
	// nothing is tunnelled.
	if direct := o.directRuleSuffixes(); len(direct) > 0 {
		rules = append(rules,
			route(map[string]any{"domain_suffix": direct}, o.internetDirect()),
		)
	}
	if proxy := o.proxyRuleSuffixes(); len(proxy) > 0 {
		rules = append(rules,
			route(map[string]any{"domain_suffix": proxy}, tagProxy),
		)
	}

	if !includeOnly {
		switch o.Mode {
		case ModeDirect:
			// Everything untunnelled; nothing else needs a rule, the final carries it
			// (see FinalOutbound) — direct, or the bypass helper when it is armed.
		case ModeGlobal:
			// Everything via proxy; LAN may still be excused below.
		case ModeSmart:
			// RU IPs and RU domains go direct.
			rules = append(rules,
				route(map[string]any{"rule_set": []string{ruleSetGeoIPRU, ruleSetGeositeRU}}, o.internetDirect()),
			)
		}
	}

	// Private/LAN destinations should never traverse the tunnel. Match the
	// well-known private ranges directly instead of relying on a rule-set so
	// this works offline before any download completes.
	//
	// The same rule doubles as the LAN guard for the bypass: when unmatched traffic
	// falls through to the helper (direct mode, or include split where everything
	// unlisted falls through) LAN would ride along with it, and a helper that
	// reshapes TCP for a DPI box on the way to the internet has nothing to offer a
	// printer or a NAS — it only breaks them. So it is emitted regardless of the LAN
	// toggle in that case. That is not a change of routing: the traffic it catches
	// is exactly the traffic that left on the direct outbound before the bypass
	// existed, and it can only ever pull traffic back to direct, never out of the
	// tunnel.
	if o.BypassLAN || o.FinalOutbound() == tagDPI {
		rules = append(rules,
			route(map[string]any{"ip_is_private": true}, tagDirect),
		)
	}

	return rules
}

// internetDirect is the outbound for destinations this config keeps off the
// tunnel. Normally that is the plain direct outbound; with the bypass armed the
// same traffic goes through the local helper instead, so "not tunnelled" stops
// meaning "dialled raw at whatever is inspecting the line". LAN is deliberately
// not routed through here — see the ip_is_private rule.
func (o Options) internetDirect() string {
	if o.DPIBypass {
		return tagDPI
	}
	return tagDirect
}

// dpiBinaryName is the file name of the bypass helper for the current platform,
// which is what the loop-guard rule matches on (sing-box's process_name matches
// the executable file name, case-insensitively). It delegates to core/dpi rather
// than repeating the name: a guard that drifts from the binary the runner
// actually spawns matches nothing, and the helper silently loops back into
// itself — a failure with no error message anywhere.
func dpiBinaryName() string {
	return dpi.BinaryName()
}

// route builds a single "action":"route" rule with the given match fields and
// target outbound. Centralizing this keeps the action/outbound pairing
// consistent across the 1.11+ schema.
func route(match map[string]any, outbound string) map[string]any {
	r := make(map[string]any, len(match)+2)
	for k, v := range match {
		r[k] = v
	}
	r["action"] = "route"
	r["outbound"] = outbound
	return r
}

// DefaultDomainResolver tells sing-box how to resolve the domain names of
// outbound servers. It must resolve without the proxy — connecting to a proxy
// whose address is a hostname would otherwise deadlock — so it points at the
// direct resolver.
func (o Options) DefaultDomainResolver() map[string]any {
	return map[string]any{"server": dnsDirectTag}
}

// FinalOutbound is the tag the route "final" should point at for this mode.
// Smart and global default to the proxy; direct sends unmatched traffic out
// the direct outbound.
//
// Include split tunnelling forces the final off the proxy: only the explicitly
// listed apps are routed to the proxy (by an early process_name rule), so
// everything that falls through must stay untunnelled to honour "only these apps".
//
// Both untunnelled cases follow internetDirect, so with the bypass armed the
// traffic that falls through reaches the internet through the helper rather than
// raw — it is the same traffic the geo and user rules hand to it, just matched by
// the final instead of by a rule.
func (o Options) FinalOutbound() string {
	if o.SplitMode == SplitInclude && len(o.SplitApps) > 0 {
		return o.internetDirect()
	}
	if o.Mode == ModeDirect {
		return o.internetDirect()
	}
	return tagProxy
}
