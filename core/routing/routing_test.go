package routing

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestNormalizeDefaults(t *testing.T) {
	got := Options{}.Normalize()
	if got.Mode != ModeSmart {
		t.Errorf("default mode = %q, want %q", got.Mode, ModeSmart)
	}
	if got.DNSRemote != DefaultDNSRemote {
		t.Errorf("default remote DNS = %q, want %q", got.DNSRemote, DefaultDNSRemote)
	}
	if got.DNSDirect != DefaultDNSDirect {
		t.Errorf("default direct DNS = %q, want %q", got.DNSDirect, DefaultDNSDirect)
	}
}

func TestNormalizeKeepsExplicit(t *testing.T) {
	in := Options{Mode: ModeGlobal, DNSRemote: "tls://9.9.9.9", DNSDirect: "8.8.8.8"}
	got := in.Normalize()
	if got.Mode != ModeGlobal || got.DNSRemote != "tls://9.9.9.9" || got.DNSDirect != "8.8.8.8" {
		t.Errorf("normalize overwrote explicit values: %+v", got)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		wantErr bool
	}{
		{"smart ok", Options{Mode: ModeSmart, DNSRemote: "a", DNSDirect: "b"}, false},
		{"unknown mode", Options{Mode: "weird", DNSRemote: "a", DNSDirect: "b"}, true},
		{"empty remote", Options{Mode: ModeSmart, DNSDirect: "b"}, true},
		{"empty direct", Options{Mode: ModeSmart, DNSRemote: "a"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRuleSetsOnlyForSmart(t *testing.T) {
	if rs := (Options{Mode: ModeSmart}).Normalize().RouteRuleSets(); len(rs) != 2 {
		t.Fatalf("smart rule_sets = %d, want 2", len(rs))
	}
	if rs := (Options{Mode: ModeGlobal}).Normalize().RouteRuleSets(); rs != nil {
		t.Errorf("global rule_sets = %v, want nil", rs)
	}
	if rs := (Options{Mode: ModeDirect}).Normalize().RouteRuleSets(); rs != nil {
		t.Errorf("direct rule_sets = %v, want nil", rs)
	}
}

func TestRuleSetURLsAndDetour(t *testing.T) {
	rs := (Options{Mode: ModeSmart}).Normalize().RouteRuleSets()
	wantURL := map[string]string{
		ruleSetGeoIPRU:   urlGeoIPRU,
		ruleSetGeositeRU: urlGeositeRU,
	}
	for _, set := range rs {
		tag := set["tag"].(string)
		if set["url"] != wantURL[tag] {
			t.Errorf("rule_set %q url = %v, want %v", tag, set["url"], wantURL[tag])
		}
		if set["download_detour"] != tagDirect {
			t.Errorf("rule_set %q detour = %v, want %q", tag, set["download_detour"], tagDirect)
		}
		if set["format"] != "binary" {
			t.Errorf("rule_set %q format = %v, want binary", tag, set["format"])
		}
		if set["type"] != "remote" {
			t.Errorf("rule_set %q type = %v, want remote", tag, set["type"])
		}
	}
	// Both official public sources must be referenced.
	if urlGeoIPRU == "" || urlGeositeRU == "" {
		t.Fatal("rule-set URLs must be set")
	}
}

func TestRuleSetsLocalWhenDirSet(t *testing.T) {
	dir := filepath.Join("C:\\", "resources")
	rs := (Options{Mode: ModeSmart, RuleSetDir: dir}).Normalize().RouteRuleSets()
	if len(rs) != 2 {
		t.Fatalf("local rule_sets = %d, want 2", len(rs))
	}
	wantPath := map[string]string{
		ruleSetGeoIPRU:   filepath.Join(dir, fileGeoIPRU),
		ruleSetGeositeRU: filepath.Join(dir, fileGeositeRU),
	}
	for _, set := range rs {
		tag := set["tag"].(string)
		if set["type"] != "local" {
			t.Errorf("rule_set %q type = %v, want local", tag, set["type"])
		}
		if set["format"] != "binary" {
			t.Errorf("rule_set %q format = %v, want binary", tag, set["format"])
		}
		if set["path"] != wantPath[tag] {
			t.Errorf("rule_set %q path = %v, want %v", tag, set["path"], wantPath[tag])
		}
		// A local rule-set must carry none of the remote-download fields, or
		// sing-box would still try to fetch it at startup.
		for _, k := range []string{"url", "download_detour", "update_interval"} {
			if _, has := set[k]; has {
				t.Errorf("local rule_set %q must not have %q, got %v", tag, k, set[k])
			}
		}
	}
}

// TestRuleSetsRemoteWhenDirEmpty pins the fallback: with no RuleSetDir the sets
// stay remote and carry no local path.
func TestRuleSetsRemoteWhenDirEmpty(t *testing.T) {
	rs := (Options{Mode: ModeSmart}).Normalize().RouteRuleSets()
	if len(rs) != 2 {
		t.Fatalf("remote rule_sets = %d, want 2", len(rs))
	}
	for _, set := range rs {
		if set["type"] != "remote" {
			t.Errorf("rule_set %q type = %v, want remote", set["tag"], set["type"])
		}
		if _, has := set["path"]; has {
			t.Errorf("remote rule_set %q must not have a path, got %v", set["tag"], set["path"])
		}
	}
}

// TestRuleSetDirSurvivesNormalize guards the plumbing: Normalize must preserve
// RuleSetDir so the value the daemon sets reaches Build unchanged.
func TestRuleSetDirSurvivesNormalize(t *testing.T) {
	dir := filepath.Join("C:\\", "some", "resources")
	if got := (Options{Mode: ModeSmart, RuleSetDir: dir}).Normalize().RuleSetDir; got != dir {
		t.Errorf("RuleSetDir after Normalize = %q, want %q", got, dir)
	}
}

func TestRouteRulesDNSHijackFirst(t *testing.T) {
	rules := (Options{Mode: ModeSmart}).Normalize().RouteRules()
	if len(rules) < 2 {
		t.Fatalf("expected at least sniff+hijack rules, got %d", len(rules))
	}
	if rules[0]["action"] != "sniff" {
		t.Errorf("first rule action = %v, want sniff", rules[0]["action"])
	}
	if rules[1]["action"] != "hijack-dns" || rules[1]["protocol"] != "dns" {
		t.Errorf("second rule = %v, want dns hijack", rules[1])
	}
}

func TestSmartRouteSendsRUDirect(t *testing.T) {
	rules := (Options{Mode: ModeSmart}).Normalize().RouteRules()
	var found bool
	for _, r := range rules {
		if r["action"] == "route" && r["outbound"] == tagDirect {
			if sets, ok := r["rule_set"].([]string); ok && len(sets) == 2 {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("smart mode missing RU->direct rule_set route; rules=%v", rules)
	}
}

func TestBypassLANRule(t *testing.T) {
	with := (Options{Mode: ModeGlobal, BypassLAN: true}).Normalize().RouteRules()
	var hasPrivate bool
	for _, r := range with {
		if r["ip_is_private"] == true && r["outbound"] == tagDirect {
			hasPrivate = true
		}
	}
	if !hasPrivate {
		t.Error("BypassLAN should add ip_is_private->direct rule")
	}

	without := (Options{Mode: ModeGlobal, BypassLAN: false}).Normalize().RouteRules()
	for _, r := range without {
		if r["ip_is_private"] == true {
			t.Error("no BypassLAN should not add ip_is_private rule")
		}
	}
}

func TestFinalOutbound(t *testing.T) {
	if got := (Options{Mode: ModeDirect}).Normalize().FinalOutbound(); got != tagDirect {
		t.Errorf("direct final = %q, want %q", got, tagDirect)
	}
	if got := (Options{Mode: ModeSmart}).Normalize().FinalOutbound(); got != tagProxy {
		t.Errorf("smart final = %q, want %q", got, tagProxy)
	}
	if got := (Options{Mode: ModeGlobal}).Normalize().FinalOutbound(); got != tagProxy {
		t.Errorf("global final = %q, want %q", got, tagProxy)
	}
}

func TestDNSBlockStructure(t *testing.T) {
	dns := (Options{Mode: ModeSmart, DNSRemote: "tls://1.1.1.1", DNSDirect: "https://77.88.8.8/dns-query"}).Normalize().DNS()

	servers, ok := dns["servers"].([]map[string]any)
	if !ok || len(servers) != 2 {
		t.Fatalf("dns servers = %v, want 2", dns["servers"])
	}
	// remote uses proxy detour, direct uses direct detour.
	byTag := map[string]map[string]any{}
	for _, s := range servers {
		byTag[s["tag"].(string)] = s
	}
	if byTag[dnsRemoteTag]["detour"] != tagProxy {
		t.Errorf("remote DNS detour = %v, want proxy", byTag[dnsRemoteTag]["detour"])
	}
	if byTag[dnsRemoteTag]["type"] != "tls" || byTag[dnsRemoteTag]["server"] != "1.1.1.1" {
		t.Errorf("remote DNS = %v, want type tls server 1.1.1.1", byTag[dnsRemoteTag])
	}
	if _, has := byTag[dnsDirectTag]["detour"]; has {
		t.Errorf("direct DNS should have no detour (1.13 rejects a detour to a bare direct outbound), got %v", byTag[dnsDirectTag]["detour"])
	}
	if byTag[dnsDirectTag]["type"] != "https" || byTag[dnsDirectTag]["server"] != "77.88.8.8" || byTag[dnsDirectTag]["path"] != "/dns-query" {
		t.Errorf("direct DNS = %v, want type https server 77.88.8.8 path /dns-query", byTag[dnsDirectTag])
	}
	if dns["final"] != dnsRemoteTag {
		t.Errorf("dns final = %v, want %q", dns["final"], dnsRemoteTag)
	}
}

// TestDNSFinalModeAware covers fix #4: in direct mode the DNS final must resolve
// via the direct resolver (route.final is direct, so forcing DNS through the
// proxy selector would break resolution exactly when the user picks direct). In
// smart and global modes the final stays on the remote resolver.
func TestDNSFinalModeAware(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeSmart, dnsRemoteTag},
		{ModeGlobal, dnsRemoteTag},
		{ModeDirect, dnsDirectTag},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			dns := (Options{Mode: tt.mode}).Normalize().DNS()
			if dns["final"] != tt.want {
				t.Errorf("%s dns final = %v, want %q", tt.mode, dns["final"], tt.want)
			}
		})
	}
}

func TestDNSStrategyIPv4Only(t *testing.T) {
	on := (Options{Mode: ModeSmart, IPv4Only: true}).Normalize().DNS()
	if on["strategy"] != "ipv4_only" {
		t.Errorf("IPv4Only dns strategy = %v, want ipv4_only", on["strategy"])
	}
	off := (Options{Mode: ModeSmart, IPv4Only: false}).Normalize().DNS()
	if _, has := off["strategy"]; has {
		t.Errorf("non-IPv4Only should omit dns strategy, got %v", off["strategy"])
	}
}

func TestDNSRulesSmartOnly(t *testing.T) {
	smart := (Options{Mode: ModeSmart}).Normalize().dnsRules()
	if len(smart) != 1 || smart[0]["server"] != dnsDirectTag {
		t.Errorf("smart dns rules = %v, want RU->direct", smart)
	}
	global := (Options{Mode: ModeGlobal}).Normalize().dnsRules()
	if len(global) != 0 {
		t.Errorf("global dns rules = %v, want empty", global)
	}
}

// TestDNSMarshals guards against emitting anything encoding/json can't handle.
func TestDNSMarshals(t *testing.T) {
	dns := (Options{Mode: ModeSmart, IPv4Only: true}).Normalize().DNS()
	if _, err := json.Marshal(dns); err != nil {
		t.Fatalf("dns block does not marshal: %v", err)
	}
}
