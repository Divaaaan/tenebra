package routing

import (
	"encoding/json"
	"reflect"
	"testing"
)

// firstActionRules returns the leading sniff+hijack rules unchanged across every
// split configuration; per-app rules must never displace them.
func assertSniffHijackFirst(t *testing.T, rules []map[string]any) {
	t.Helper()
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

// processRule finds the first route rule carrying a process_name match.
func processRule(rules []map[string]any) (map[string]any, bool) {
	for _, r := range rules {
		if _, ok := r["process_name"]; ok {
			return r, true
		}
	}
	return nil, false
}

func TestSplitNormalizeOffByDefault(t *testing.T) {
	got := Options{}.Normalize()
	if got.SplitMode != SplitOff {
		t.Errorf("default split mode = %q, want %q", got.SplitMode, SplitOff)
	}
	if got.SplitApps != nil {
		t.Errorf("default split apps = %v, want nil", got.SplitApps)
	}
}

func TestSplitNormalizeLowercasesDedupesSorts(t *testing.T) {
	got := Options{
		SplitMode: SplitExclude,
		SplitApps: []string{"Chrome.exe", "  steam.exe ", "CHROME.EXE", "", "discord.exe"},
	}.Normalize()
	want := []string{"chrome.exe", "discord.exe", "steam.exe"}
	if !reflect.DeepEqual(got.SplitApps, want) {
		t.Errorf("normalized apps = %v, want %v", got.SplitApps, want)
	}
}

func TestSplitEmptyListCollapsesToOff(t *testing.T) {
	got := Options{SplitMode: SplitInclude, SplitApps: []string{"   ", ""}}.Normalize()
	if got.SplitMode != SplitOff {
		t.Errorf("empty include list split mode = %q, want %q", got.SplitMode, SplitOff)
	}
	if got.SplitApps != nil {
		t.Errorf("empty include list apps = %v, want nil", got.SplitApps)
	}
}

func TestSplitUnknownModeCanonicalizedToOff(t *testing.T) {
	got := Options{SplitMode: "weird", SplitApps: []string{"a.exe"}}.Normalize()
	if got.SplitMode != SplitOff {
		t.Errorf("unknown split mode normalized to %q, want %q", got.SplitMode, SplitOff)
	}
}

func TestSplitValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		wantErr bool
	}{
		{"off ok", Options{Mode: ModeSmart, DNSRemote: "a", DNSDirect: "b", SplitMode: SplitOff}, false},
		{"empty split ok", Options{Mode: ModeSmart, DNSRemote: "a", DNSDirect: "b"}, false},
		{"exclude ok", Options{Mode: ModeSmart, DNSRemote: "a", DNSDirect: "b", SplitMode: SplitExclude}, false},
		{"include ok", Options{Mode: ModeSmart, DNSRemote: "a", DNSDirect: "b", SplitMode: SplitInclude}, false},
		{"unknown split", Options{Mode: ModeSmart, DNSRemote: "a", DNSDirect: "b", SplitMode: "weird"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.opts.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSplitOffLeavesRoutingUnchanged(t *testing.T) {
	base := (Options{Mode: ModeSmart}).Normalize().RouteRules()
	off := (Options{Mode: ModeSmart, SplitMode: SplitOff}).Normalize().RouteRules()
	if !reflect.DeepEqual(base, off) {
		t.Errorf("split off changed the rules:\nbase = %v\noff  = %v", base, off)
	}
	// An empty app list with a real mode is also a no-op.
	emptyList := (Options{Mode: ModeSmart, SplitMode: SplitExclude, SplitApps: nil}).Normalize().RouteRules()
	if !reflect.DeepEqual(base, emptyList) {
		t.Errorf("empty exclude list changed the rules:\nbase     = %v\nemptyList= %v", base, emptyList)
	}
}

func TestSplitExcludeAddsProcessNameDirectRule(t *testing.T) {
	rules := (Options{Mode: ModeSmart, SplitMode: SplitExclude, SplitApps: []string{"Chrome.exe", "steam.exe"}}).
		Normalize().RouteRules()
	assertSniffHijackFirst(t, rules)

	pr, ok := processRule(rules)
	if !ok {
		t.Fatalf("exclude mode missing process_name rule; rules=%v", rules)
	}
	if pr["action"] != "route" || pr["outbound"] != tagDirect {
		t.Errorf("exclude process rule = %v, want action route outbound direct", pr)
	}
	names, ok := pr["process_name"].([]string)
	if !ok || !reflect.DeepEqual(names, []string{"chrome.exe", "steam.exe"}) {
		t.Errorf("exclude process_name = %v, want [chrome.exe steam.exe]", pr["process_name"])
	}

	// The base smart RU->direct rule must still be present: exclude only pins the
	// listed apps, the rest keeps following the base mode.
	var hasRU bool
	for _, r := range rules {
		if sets, ok := r["rule_set"].([]string); ok && len(sets) == 2 && r["outbound"] == tagDirect {
			hasRU = true
		}
	}
	if !hasRU {
		t.Errorf("exclude mode dropped the base smart RU rule; rules=%v", rules)
	}
}

func TestSplitExcludeRuleComesBeforeGeoSplit(t *testing.T) {
	rules := (Options{Mode: ModeSmart, SplitMode: SplitExclude, SplitApps: []string{"chrome.exe"}}).
		Normalize().RouteRules()
	procIdx, geoIdx := -1, -1
	for i, r := range rules {
		if _, ok := r["process_name"]; ok {
			procIdx = i
		}
		if _, ok := r["rule_set"]; ok {
			geoIdx = i
		}
	}
	if procIdx == -1 || geoIdx == -1 {
		t.Fatalf("expected both a process_name and a rule_set rule; rules=%v", rules)
	}
	if procIdx >= geoIdx {
		t.Errorf("process_name rule (idx %d) must precede geo split (idx %d)", procIdx, geoIdx)
	}
	// And both must still sit after sniff/hijack.
	if procIdx < 2 {
		t.Errorf("process_name rule at idx %d must come after sniff+hijack", procIdx)
	}
}

func TestSplitIncludeSendsListedToProxyOthersDirect(t *testing.T) {
	opts := (Options{Mode: ModeSmart, SplitMode: SplitInclude, SplitApps: []string{"Chrome.exe"}}).Normalize()
	rules := opts.RouteRules()
	assertSniffHijackFirst(t, rules)

	pr, ok := processRule(rules)
	if !ok {
		t.Fatalf("include mode missing process_name rule; rules=%v", rules)
	}
	if pr["action"] != "route" || pr["outbound"] != tagProxy {
		t.Errorf("include process rule = %v, want action route outbound proxy", pr)
	}

	// Everything unlisted must fall through to direct.
	if got := opts.FinalOutbound(); got != tagDirect {
		t.Errorf("include FinalOutbound = %q, want %q (others go direct)", got, tagDirect)
	}

	// The base smart RU->direct *route* rule must be dropped: it would otherwise
	// be redundant noise, and more importantly the base proxy split must not run.
	for _, r := range rules {
		if sets, ok := r["rule_set"].([]string); ok && len(sets) == 2 {
			t.Errorf("include mode should drop the base RU route rule, found %v", r)
		}
	}
}

func TestSplitIncludeFinalDirectForEveryBaseMode(t *testing.T) {
	for _, m := range []Mode{ModeSmart, ModeGlobal, ModeDirect} {
		opts := (Options{Mode: m, SplitMode: SplitInclude, SplitApps: []string{"chrome.exe"}}).Normalize()
		if got := opts.FinalOutbound(); got != tagDirect {
			t.Errorf("include final for base %q = %q, want %q", m, got, tagDirect)
		}
		// The single proxy-bound rule must be the app rule.
		pr, ok := processRule(opts.RouteRules())
		if !ok || pr["outbound"] != tagProxy {
			t.Errorf("include base %q: expected process_name->proxy rule, got %v", m, pr)
		}
	}
}

func TestSplitIncludeWithGlobalDropsBlanketProxy(t *testing.T) {
	// Global normally sends everything to proxy via final. With include, final
	// becomes direct and only the listed app is proxied, so there must be no
	// proxy-bound rule other than the process_name one.
	opts := (Options{Mode: ModeGlobal, SplitMode: SplitInclude, SplitApps: []string{"chrome.exe"}}).Normalize()
	if opts.FinalOutbound() != tagDirect {
		t.Fatalf("include+global final = %q, want direct", opts.FinalOutbound())
	}
	for _, r := range opts.RouteRules() {
		if r["outbound"] == tagProxy {
			if _, isProc := r["process_name"]; !isProc {
				t.Errorf("include+global has a non-app proxy rule: %v", r)
			}
		}
	}
}

func TestSplitExcludeKeepsFinalProxy(t *testing.T) {
	// Exclude does not change which outbound unmatched traffic uses.
	if got := (Options{Mode: ModeSmart, SplitMode: SplitExclude, SplitApps: []string{"a.exe"}}).Normalize().FinalOutbound(); got != tagProxy {
		t.Errorf("exclude+smart final = %q, want %q", got, tagProxy)
	}
	if got := (Options{Mode: ModeDirect, SplitMode: SplitExclude, SplitApps: []string{"a.exe"}}).Normalize().FinalOutbound(); got != tagDirect {
		t.Errorf("exclude+direct final = %q, want %q", got, tagDirect)
	}
}

func TestSplitBypassLANStillApplies(t *testing.T) {
	for _, sm := range []SplitMode{SplitExclude, SplitInclude} {
		rules := (Options{Mode: ModeGlobal, BypassLAN: true, SplitMode: sm, SplitApps: []string{"a.exe"}}).
			Normalize().RouteRules()
		var hasPrivate bool
		for _, r := range rules {
			if r["ip_is_private"] == true && r["outbound"] == tagDirect {
				hasPrivate = true
			}
		}
		if !hasPrivate {
			t.Errorf("split %q should keep the BypassLAN rule; rules=%v", sm, rules)
		}
	}
}

// dnsProcessRule finds the first dns rule carrying a process_name match.
func dnsProcessRule(rules []map[string]any) (map[string]any, bool) {
	for _, r := range rules {
		if _, ok := r["process_name"]; ok {
			return r, true
		}
	}
	return nil, false
}

// TestSplitIncludeDNSFollowsTunnelInDirectMode locks the fix for a DNS
// split-tunnel leak: in direct base mode the DNS final is dns-direct (resolves
// outside the tunnel), but an app pinned to the proxy by include split
// tunnelling routes its traffic through the proxy. Without a matching dns rule
// its lookups would go out direct while its traffic went through the tunnel,
// leaking every domain it visits to the local resolver/ISP. The dns rules must
// carry a process_name rule that sends the listed apps to dns-remote, so their
// DNS follows the tunnel like their traffic does.
func TestSplitIncludeDNSFollowsTunnelInDirectMode(t *testing.T) {
	opts := (Options{Mode: ModeDirect, SplitMode: SplitInclude, SplitApps: []string{"Chrome.exe"}}).Normalize()

	// The unmatched final stays dns-direct — that is correct for direct mode and
	// is not what covers the tunnelled apps.
	if got := opts.dnsFinal(); got != dnsDirectTag {
		t.Errorf("direct+include dnsFinal = %q, want %q", got, dnsDirectTag)
	}

	rules := opts.dnsRules()
	pr, ok := dnsProcessRule(rules)
	if !ok {
		t.Fatalf("direct+include missing process_name dns rule (DNS would leak); rules=%v", rules)
	}
	if pr["action"] != "route" || pr["server"] != dnsRemoteTag {
		t.Errorf("include dns process rule = %v, want action route server %s", pr, dnsRemoteTag)
	}
	names, ok := pr["process_name"].([]string)
	if !ok || !reflect.DeepEqual(names, []string{"chrome.exe"}) {
		t.Errorf("include dns process_name = %v, want [chrome.exe]", pr["process_name"])
	}

	// The traffic for the same app is pinned to the proxy, so DNS and traffic
	// agree — the whole point of the fix.
	route, ok := processRule(opts.RouteRules())
	if !ok || route["outbound"] != tagProxy {
		t.Errorf("include route rule = %v, want the app pinned to proxy", route)
	}
}

// TestSplitIncludeDNSRuleInEveryBaseMode: the tunnelled-apps dns rule is emitted
// under include split for every base mode. In smart/global it restates the
// dns-remote final harmlessly; in direct it is the leak fix. It must always send
// the listed apps to dns-remote and precede the smart RU->direct rule so a
// tunnelled app's DNS is never pulled direct.
func TestSplitIncludeDNSRuleInEveryBaseMode(t *testing.T) {
	for _, m := range []Mode{ModeSmart, ModeGlobal, ModeDirect} {
		opts := (Options{Mode: m, SplitMode: SplitInclude, SplitApps: []string{"chrome.exe"}}).Normalize()
		rules := opts.dnsRules()
		pr, ok := dnsProcessRule(rules)
		if !ok {
			t.Errorf("include base %q: missing process_name dns rule; rules=%v", m, rules)
			continue
		}
		if pr["server"] != dnsRemoteTag {
			t.Errorf("include base %q: dns process rule server = %v, want %s", m, pr["server"], dnsRemoteTag)
		}
		// The process rule must come before any RU->direct rule.
		procIdx, ruIdx := -1, -1
		for i, r := range rules {
			if _, has := r["process_name"]; has {
				procIdx = i
			}
			if _, has := r["rule_set"]; has && r["server"] == dnsDirectTag {
				ruIdx = i
			}
		}
		if ruIdx != -1 && procIdx >= ruIdx {
			t.Errorf("include base %q: process dns rule (idx %d) must precede RU rule (idx %d)", m, procIdx, ruIdx)
		}
	}
}

// TestSplitExcludeAddsNoDNSProcessRule: exclude split does not add a dns
// process_name rule. Excluded apps go direct, so resolving them via the base
// final (dns-remote in smart/global, dns-direct in direct) never leaks a
// tunnelled app's DNS — there is nothing to pin.
func TestSplitExcludeAddsNoDNSProcessRule(t *testing.T) {
	for _, m := range []Mode{ModeSmart, ModeGlobal, ModeDirect} {
		opts := (Options{Mode: m, SplitMode: SplitExclude, SplitApps: []string{"chrome.exe"}}).Normalize()
		if _, ok := dnsProcessRule(opts.dnsRules()); ok {
			t.Errorf("exclude base %q unexpectedly added a dns process_name rule", m)
		}
	}
}

// TestSplitRulesMarshal guards against emitting anything encoding/json chokes on.
func TestSplitRulesMarshal(t *testing.T) {
	for _, sm := range []SplitMode{SplitOff, SplitExclude, SplitInclude} {
		opts := (Options{Mode: ModeSmart, SplitMode: sm, SplitApps: []string{"chrome.exe", "steam.exe"}}).Normalize()
		if _, err := json.Marshal(opts.RouteRules()); err != nil {
			t.Fatalf("split %q route rules do not marshal: %v", sm, err)
		}
	}
}
