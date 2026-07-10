package routing

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestNormalizePreservesAdBlockAndCustomDNS(t *testing.T) {
	in := Options{
		Mode:      ModeSmart,
		AdBlock:   true,
		DNSRemote: "tls://9.9.9.9",
		DNSDirect: "udp://8.8.8.8",
	}
	got := in.Normalize()
	if !got.AdBlock {
		t.Error("Normalize dropped AdBlock")
	}
	if got.DNSRemote != "tls://9.9.9.9" || got.DNSDirect != "udp://8.8.8.8" {
		t.Errorf("Normalize overwrote custom DNS: remote=%q direct=%q", got.DNSRemote, got.DNSDirect)
	}
}

func TestValidDNSServer(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		// Empty is valid: Normalize substitutes the default resolver.
		{"", true},
		// The schemes dnsServer parses, with and without ports/paths.
		{"tls://1.1.1.1", true},
		{"tls://1.1.1.1:853", true},
		{"https://77.88.8.8/dns-query", true},
		{"h3://1.1.1.1/dns-query", true},
		{"quic://dns.adguard.com", true},
		{"tcp://8.8.8.8:53", true},
		{"udp://8.8.8.8", true},
		// A bare host (no scheme) is the udp default form.
		{"1.1.1.1", true},
		{"8.8.8.8:53", true},
		{"one.one.one.one", true},
		{"dns.google", true},
		// IPv6, bare and bracketed-with-port.
		{"tls://2606:4700:4700::1111", true},
		{"tls://[2606:4700:4700::1111]:853", true},
		// Garbage and malformed inputs must be rejected.
		{"http://1.1.1.1", false},      // not a DNS scheme
		{"ftp://example", false},       // not a DNS scheme
		{"tls://", false},              // empty host
		{"https://", false},            // empty host
		{"not a url", false},           // whitespace in host
		{"udp://8.8.8.8:99999", false}, // port out of range
		{"udp://8.8.8.8:0", false},     // port out of range
		{"udp://8.8.8.8:abc", false},   // non-numeric port
		{"tls://1.1.1.1/path", false},  // path on a non-DoH scheme
		{"-lead.example", false},       // label starts with hyphen
		{"bad_.example-", false},       // label ends with hyphen
	}
	for _, tt := range tests {
		if got := ValidDNSServer(tt.addr); got != tt.want {
			t.Errorf("ValidDNSServer(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}

// TestDNSAdBlockRule locks the sinkhole rule: when ad-blocking is active it is
// the FIRST dns rule (so ads are refused before any routing rule), it is a REFUSED
// reject with no_drop set, and it matches the local ads rule-set.
func TestDNSAdBlockRule(t *testing.T) {
	dir := filepath.Join("C:\\", "res")
	rules := (Options{Mode: ModeSmart, AdBlock: true, RuleSetDir: dir}).Normalize().dnsRules()
	if len(rules) != 2 {
		t.Fatalf("smart+adblock dns rules = %d, want 2 (reject then RU route)", len(rules))
	}
	first := rules[0]
	if first["action"] != "reject" {
		t.Errorf("first rule action = %v, want reject", first["action"])
	}
	if first["method"] != "default" {
		t.Errorf("reject method = %v, want default (REFUSED)", first["method"])
	}
	if first["no_drop"] != true {
		t.Errorf("reject no_drop = %v, want true", first["no_drop"])
	}
	sets, ok := first["rule_set"].([]string)
	if !ok || len(sets) != 1 || sets[0] != ruleSetGeositeAds {
		t.Errorf("reject rule_set = %v, want [%s]", first["rule_set"], ruleSetGeositeAds)
	}
	// The RU route rule still follows in smart mode.
	if rules[1]["action"] != "route" || rules[1]["server"] != dnsDirectTag {
		t.Errorf("second rule = %v, want RU->direct route", rules[1])
	}
}

// TestDNSAdBlockActiveInEveryMode: the reject rule leads the dns rules in smart,
// global and direct alike — ad-blocking is not mode-specific.
func TestDNSAdBlockActiveInEveryMode(t *testing.T) {
	dir := filepath.Join("C:\\", "res")
	for _, mode := range []Mode{ModeSmart, ModeGlobal, ModeDirect} {
		rules := (Options{Mode: mode, AdBlock: true, RuleSetDir: dir}).Normalize().dnsRules()
		if len(rules) == 0 || rules[0]["action"] != "reject" {
			t.Errorf("%s+adblock dns rules = %v, want reject first", mode, rules)
		}
	}
}

// TestDNSAdBlockGatedOnLocalRuleSet: ad-blocking is inert without RuleSetDir. The
// blocklist ships strictly as a local set, so with nothing bundled the reject rule
// must not be emitted (it would reference an undefined rule-set and FATAL).
func TestDNSAdBlockGatedOnLocalRuleSet(t *testing.T) {
	noDir := (Options{Mode: ModeGlobal, AdBlock: true}).Normalize().dnsRules()
	for _, r := range noDir {
		if r["action"] == "reject" {
			t.Errorf("adblock without RuleSetDir emitted a reject rule: %v", noDir)
		}
	}
	if len(noDir) != 0 {
		t.Errorf("global+adblock (no RuleSetDir) dns rules = %v, want empty", noDir)
	}

	// Ad-blocking off keeps the rules clean even with a rule-set dir present.
	off := (Options{Mode: ModeGlobal, RuleSetDir: filepath.Join("C:\\", "res")}).Normalize().dnsRules()
	if len(off) != 0 {
		t.Errorf("global no-adblock dns rules = %v, want empty", off)
	}
}

// TestRouteRuleSetsAdBlockLocalOnly: the ads rule-set is defined for the dns rule
// to reference, in every mode, and ALWAYS as a local set — never remote, so it can
// never reintroduce the startup-blocking fetch.
func TestRouteRuleSetsAdBlockLocalOnly(t *testing.T) {
	dir := filepath.Join("C:\\", "res")

	// Global mode: RU is smart-only, so adblock contributes exactly the ads set.
	rs := (Options{Mode: ModeGlobal, AdBlock: true, RuleSetDir: dir}).Normalize().RouteRuleSets()
	if len(rs) != 1 {
		t.Fatalf("global+adblock rule_sets = %d, want 1 (ads only)", len(rs))
	}
	ads := rs[0]
	if ads["tag"] != ruleSetGeositeAds {
		t.Errorf("ads tag = %v, want %s", ads["tag"], ruleSetGeositeAds)
	}
	if ads["type"] != "local" {
		t.Errorf("ads type = %v, want local (blocklist is never remote)", ads["type"])
	}
	if got := filepath.Base(ads["path"].(string)); got != fileGeositeAds {
		t.Errorf("ads path base = %q, want %q", got, fileGeositeAds)
	}
	// None of the remote-download fields may appear, or sing-box would fetch it at
	// startup and freeze.
	for _, k := range []string{"url", "download_detour", "update_interval"} {
		if _, has := ads[k]; has {
			t.Errorf("ads local rule_set must not carry %q, got %v", k, ads[k])
		}
	}

	// Smart mode: the RU pair plus the ads set.
	smart := (Options{Mode: ModeSmart, AdBlock: true, RuleSetDir: dir}).Normalize().RouteRuleSets()
	if len(smart) != 3 {
		t.Fatalf("smart+adblock rule_sets = %d, want 3 (RU pair + ads)", len(smart))
	}
	var haveAds bool
	for _, s := range smart {
		if s["tag"] == ruleSetGeositeAds {
			haveAds = true
			if s["type"] != "local" {
				t.Errorf("ads set in smart mode type = %v, want local", s["type"])
			}
		}
	}
	if !haveAds {
		t.Errorf("smart+adblock rule_sets missing the ads set: %v", smart)
	}

	// Without RuleSetDir the ads set is withheld (local-only); global mode then has
	// no rule-sets at all.
	noDir := (Options{Mode: ModeGlobal, AdBlock: true}).Normalize().RouteRuleSets()
	if len(noDir) != 0 {
		t.Errorf("global+adblock without RuleSetDir rule_sets = %v, want none", noDir)
	}
}

// TestDNSCustomResolvers: custom resolvers flow through into the typed servers.
func TestDNSCustomResolvers(t *testing.T) {
	dns := (Options{
		Mode:      ModeSmart,
		DNSRemote: "quic://dns.adguard.com",
		DNSDirect: "tls://8.8.8.8:853",
	}).Normalize().DNS()

	servers := dns["servers"].([]map[string]any)
	byTag := map[string]map[string]any{}
	for _, s := range servers {
		byTag[s["tag"].(string)] = s
	}
	remote := byTag[dnsRemoteTag]
	if remote["type"] != "quic" || remote["server"] != "dns.adguard.com" {
		t.Errorf("remote resolver = %v, want type quic server dns.adguard.com", remote)
	}
	direct := byTag[dnsDirectTag]
	if direct["type"] != "tls" || direct["server"] != "8.8.8.8" || direct["server_port"] != 853 {
		t.Errorf("direct resolver = %v, want type tls server 8.8.8.8 port 853", direct)
	}
}

// TestDNSWithAdBlockMarshals guards the whole dns block (reject rule included)
// against emitting anything encoding/json can't handle.
func TestDNSWithAdBlockMarshals(t *testing.T) {
	dns := (Options{Mode: ModeSmart, AdBlock: true, RuleSetDir: filepath.Join("C:\\", "res")}).Normalize().DNS()
	if _, err := json.Marshal(dns); err != nil {
		t.Fatalf("dns block with adblock does not marshal: %v", err)
	}
}
