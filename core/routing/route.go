package routing

// RouteRuleSets returns the rule_set entries the route/dns blocks reference.
// Only smart mode needs them (global/direct match no geodata), so the other
// modes get an empty slice and sing-box downloads nothing.
//
// The sets are remote .srs binaries fetched through the direct outbound so the
// download survives a blocked proxy.
func (o Options) RouteRuleSets() []map[string]any {
	if o.Mode != ModeSmart {
		return nil
	}
	mk := func(tag, url string) map[string]any {
		return map[string]any{
			"type":            "remote",
			"tag":             tag,
			"format":          "binary",
			"url":             url,
			"download_detour": tagDirect,
			"update_interval": ruleSetUpdateInterval,
		}
	}
	return []map[string]any{
		mk(ruleSetGeoIPRU, urlGeoIPRU),
		mk(ruleSetGeositeRU, urlGeositeRU),
	}
}

// RouteRules returns the ordered "route".rules. Order matters: DNS hijack and
// sniffing come first, then the mode-specific split, and the proxy selector is
// the route "final" (set by the singbox package), so anything unmatched here
// flows through the proxy.
func (o Options) RouteRules() []map[string]any {
	rules := []map[string]any{
		// Capture DNS queries from the tun and answer them through the dns block
		// rather than letting them escape to whatever resolver the app picked.
		{"action": "sniff"},
		{"protocol": "dns", "action": "hijack-dns"},
	}

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
func (o Options) FinalOutbound() string {
	if o.Mode == ModeDirect {
		return tagDirect
	}
	return tagProxy
}
