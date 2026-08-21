package routing

import "path/filepath"

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
// sniffing come first, then per-app split tunnelling, then the mode-specific
// split, and the proxy selector is the route "final" (set by the singbox
// package), so anything unmatched here flows through the route final.
//
// Split tunnelling sits above the geo split so a per-app decision wins over the
// smart/global RU rules:
//   - exclude: listed apps are pinned to direct early; everything else keeps
//     following the base mode.
//   - include: listed apps are pinned to the proxy early and the base proxy
//     split is dropped, because in include mode the route final is direct (see
//     FinalOutbound) so only the listed apps reach the proxy. The LAN bypass
//     still applies — it only ever sends traffic direct, which include already
//     does for everything unlisted, so it stays harmless and consistent.
func (o Options) RouteRules() []map[string]any {
	rules := []map[string]any{
		// Capture DNS queries from the tun and answer them through the dns block
		// rather than letting them escape to whatever resolver the app picked.
		{"action": "sniff"},
		{"protocol": "dns", "action": "hijack-dns"},
	}

	// Per-app split tunnelling. process_name matches the executable file name,
	// e.g. "chrome.exe". Placed before the geo split so an app rule wins.
	// The games preset merges into the exclude list, so a switched-on preset and a
	// hand-added app land in the same rule. It only affects exclude: pinning games
	// to the proxy in include mode would be the opposite of what the preset means.
	excludeApps := o.splitAppsWithPresets()
	switch o.SplitMode {
	case SplitExclude:
		if len(excludeApps) > 0 {
			rules = append(rules,
				route(map[string]any{"process_name": excludeApps}, tagDirect),
			)
		}
	case SplitInclude:
		if len(o.SplitApps) > 0 {
			rules = append(rules,
				route(map[string]any{"process_name": o.SplitApps}, tagProxy),
			)
		}
	default:
		// With split tunnelling off entirely, the games preset still has to work —
		// otherwise "keep games direct" would silently require the user to also
		// turn on split mode and understand what it is.
		if o.gamesDirectActive() && len(excludeApps) > 0 {
			rules = append(rules,
				route(map[string]any{"process_name": excludeApps}, tagDirect),
			)
		}
	}

	// Real-time UDP goes direct before anything else can claim it, since the
	// latency it is escaping is added by the tunnel regardless of which rule
	// would otherwise match.
	if o.voiceDirectActive() {
		rules = append(rules, route(map[string]any{
			"network":    "udp",
			"port_range": []string{voicePortRange},
		}, tagDirect))
	}

	// In include mode the route final is direct and only the listed apps are
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
			route(map[string]any{"domain_suffix": direct}, tagDirect),
		)
	}

	// With the bypass running, the censored services take the direct path: it
	// gets them through at the ISP's own latency, so tunnelling them would buy
	// nothing and cost the entire round trip.
	if bypass := o.zapretDirectSuffixes(); len(bypass) > 0 {
		rules = append(rules,
			route(map[string]any{"domain_suffix": bypass}, tagDirect),
		)
	}
	// Proxy-pinned domains include the blocked-services preset. This sits before
	// the geo split on purpose: googlevideo.com and friends resolve to RU cache
	// nodes, and the geo rule would otherwise pin the video direct while the VPN
	// reports itself connected.
	if proxy := o.proxySuffixesWithPresets(); len(proxy) > 0 {
		rules = append(rules,
			route(map[string]any{"domain_suffix": proxy}, tagProxy),
		)
	}

	if !includeOnly {
		switch o.Mode {
		case ModeDirect:
			// Everything direct; nothing else needs a rule, final stays proxy but
			// the singbox layer points final at direct for this mode.
		case ModeGlobal:
			// Everything via proxy; LAN may still be excused below.
		case ModeSmart:
			// RU IPs and RU domains go direct.
			rules = append(rules,
				route(map[string]any{"rule_set": []string{ruleSetGeoIPRU, ruleSetGeositeRU}}, tagDirect),
			)
		}
	}

	if o.BypassLAN {
		// Private/LAN destinations should never traverse the tunnel. Match the
		// well-known private ranges directly instead of relying on a rule-set so
		// this works offline before any download completes.
		rules = append(rules,
			route(map[string]any{"ip_is_private": true}, tagDirect),
		)
	}

	return rules
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
// Include split tunnelling forces the final to direct: only the explicitly
// listed apps are routed to the proxy (by an early process_name rule), so
// everything that falls through must go direct to honour "only these apps".
func (o Options) FinalOutbound() string {
	if o.SplitMode == SplitInclude && len(o.SplitApps) > 0 {
		return tagDirect
	}
	if o.Mode == ModeDirect {
		return tagDirect
	}
	return tagProxy
}
