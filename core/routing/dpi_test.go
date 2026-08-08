package routing

import (
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The DPI bypass has to hold across every routing mode and every split state, so
// the assertions below walk this matrix rather than spot-checking smart mode.
var (
	dpiModes  = []Mode{ModeSmart, ModeGlobal, ModeDirect}
	dpiSplits = []SplitMode{SplitOff, SplitExclude, SplitInclude}
)

// dpiOptions builds a fully-loaded Options for the matrix: LAN bypass, split apps
// and both custom rule directions are on, so one walk exercises every rule the
// bypass can redirect.
func dpiOptions(m Mode, sm SplitMode, on bool) Options {
	return Options{
		Mode:        m,
		BypassLAN:   true,
		SplitMode:   sm,
		SplitApps:   []string{"chrome.exe"},
		RulesDirect: []string{"example.ru"},
		RulesProxy:  []string{"example.com"},
		DPIBypass:   on,
	}.Normalize()
}

// appProcessRule finds the split-tunnelling rule for the given executable,
// skipping the bypass helper's own process rule.
func appProcessRule(rules []map[string]any, app string) (map[string]any, bool) {
	for _, r := range rules {
		names, ok := r["process_name"].([]string)
		if !ok {
			continue
		}
		for _, n := range names {
			if n == app {
				return r, true
			}
		}
	}
	return nil, false
}

func TestDPIBypassNormalizeAndValidate(t *testing.T) {
	if !(Options{DPIBypass: true}).Normalize().DPIBypass {
		t.Error("Normalize dropped DPIBypass")
	}
	if (Options{}).Normalize().DPIBypass {
		t.Error("DPIBypass must default off")
	}
	if err := (Options{Mode: ModeSmart, DNSRemote: "a", DNSDirect: "b", DPIBypass: true}).Validate(); err != nil {
		t.Errorf("Validate rejected DPIBypass: %v", err)
	}
}

// TestDPIBypassOffEmitsNothing is the regression gate at the rule level: with the
// toggle off nothing anywhere may mention the helper — no outbound, no process
// rule, no LAN guard the user did not ask for.
func TestDPIBypassOffEmitsNothing(t *testing.T) {
	for _, m := range dpiModes {
		for _, sm := range dpiSplits {
			o := dpiOptions(m, sm, false)
			for i, r := range o.RouteRules() {
				if r["outbound"] == tagDPI {
					t.Errorf("%s/%s rule %d routes to the helper with the bypass off: %v", m, sm, i, r)
				}
				if names, ok := r["process_name"].([]string); ok {
					for _, n := range names {
						if n == dpiBinaryName() {
							t.Errorf("%s/%s rule %d matches the helper binary with the bypass off: %v", m, sm, i, r)
						}
					}
				}
			}
			if got := o.FinalOutbound(); got == tagDPI {
				t.Errorf("%s/%s final = %q with the bypass off", m, sm, got)
			}
		}
	}
}

// TestDPIBypassOffAddsNoLANRule pins the other half of the off-state: the LAN
// guard the bypass needs must not appear when the user has neither the bypass nor
// the LAN bypass on.
func TestDPIBypassOffAddsNoLANRule(t *testing.T) {
	for _, m := range dpiModes {
		rules := (Options{Mode: m, DPIBypass: false}).Normalize().RouteRules()
		for _, r := range rules {
			if _, has := r["ip_is_private"]; has {
				t.Errorf("%s emitted an ip_is_private rule with both toggles off: %v", m, r)
			}
		}
	}
}

// TestDPIBypassHelperRuleComesFirst pins the loop guard: the helper's own sockets
// must leave on the plain direct outbound, and the rule saying so has to precede
// every rule that could route them into the tunnel or back into the helper. It
// sits immediately after sniff/hijack-dns in every mode and split state.
func TestDPIBypassHelperRuleComesFirst(t *testing.T) {
	want := map[string]any{
		"process_name": []string{dpiBinaryName()},
		"action":       "route",
		"outbound":     tagDirect,
	}
	for _, m := range dpiModes {
		for _, sm := range dpiSplits {
			rules := dpiOptions(m, sm, true).RouteRules()
			if len(rules) < 3 {
				t.Fatalf("%s/%s: expected at least sniff+hijack+helper rules, got %d", m, sm, len(rules))
			}
			if rules[0]["action"] != "sniff" {
				t.Errorf("%s/%s first rule = %v, want sniff", m, sm, rules[0])
			}
			if rules[1]["action"] != "hijack-dns" || rules[1]["protocol"] != "dns" {
				t.Errorf("%s/%s second rule = %v, want dns hijack", m, sm, rules[1])
			}
			if !reflect.DeepEqual(rules[2], want) {
				t.Errorf("%s/%s third rule = %v, want %v", m, sm, rules[2], want)
			}
		}
	}
}

// TestDPIBypassRedirectsDirectInternet covers the point of the feature: every
// direction that keeps internet traffic off the tunnel now hands it to the helper
// instead of dialling raw, while proxy-bound directions are untouched.
func TestDPIBypassRedirectsDirectInternet(t *testing.T) {
	// Smart mode: the RU geo split is the big one.
	smart := dpiOptions(ModeSmart, SplitOff, true).RouteRules()
	var geo map[string]any
	for _, r := range smart {
		if sets, ok := r["rule_set"].([]string); ok && len(sets) == 2 {
			geo = r
		}
	}
	if geo == nil {
		t.Fatalf("smart mode lost the geo rule: %v", smart)
	}
	if geo["outbound"] != tagDPI {
		t.Errorf("smart geo rule outbound = %v, want %q", geo["outbound"], tagDPI)
	}

	// User rules: direct-pinned domains go through the helper, proxy-pinned ones
	// keep using the tunnel.
	var userDirect, userProxy map[string]any
	for _, r := range smart {
		sfx, ok := r["domain_suffix"].([]string)
		if !ok || len(sfx) != 1 {
			continue
		}
		switch sfx[0] {
		case "example.ru":
			userDirect = r
		case "example.com":
			userProxy = r
		}
	}
	if userDirect == nil || userDirect["outbound"] != tagDPI {
		t.Errorf("user direct rule = %v, want outbound %q", userDirect, tagDPI)
	}
	if userProxy == nil || userProxy["outbound"] != tagProxy {
		t.Errorf("user proxy rule = %v, want outbound %q", userProxy, tagProxy)
	}

	// Split exclude pulls apps out of the tunnel, so they get the helper too.
	excl := dpiOptions(ModeSmart, SplitExclude, true).RouteRules()
	app, ok := appProcessRule(excl, "chrome.exe")
	if !ok {
		t.Fatalf("exclude split lost its process rule: %v", excl)
	}
	if app["outbound"] != tagDPI {
		t.Errorf("excluded app outbound = %v, want %q", app["outbound"], tagDPI)
	}

	// Split include pins apps to the tunnel; that direction must not change.
	incl := dpiOptions(ModeSmart, SplitInclude, true).RouteRules()
	app, ok = appProcessRule(incl, "chrome.exe")
	if !ok {
		t.Fatalf("include split lost its process rule: %v", incl)
	}
	if app["outbound"] != tagProxy {
		t.Errorf("included app outbound = %v, want %q", app["outbound"], tagProxy)
	}
}

// TestDPIBypassFinalOutbound: wherever unmatched traffic used to leave direct it
// now leaves through the helper — direct mode, and include split where everything
// unlisted falls through. Modes that tunnel the remainder keep the proxy final.
func TestDPIBypassFinalOutbound(t *testing.T) {
	tests := []struct {
		mode  Mode
		split SplitMode
		want  string
	}{
		{ModeSmart, SplitOff, tagProxy},
		{ModeGlobal, SplitOff, tagProxy},
		{ModeDirect, SplitOff, tagDPI},
		{ModeSmart, SplitExclude, tagProxy},
		{ModeDirect, SplitExclude, tagDPI},
		{ModeSmart, SplitInclude, tagDPI},
		{ModeGlobal, SplitInclude, tagDPI},
		{ModeDirect, SplitInclude, tagDPI},
	}
	for _, tt := range tests {
		if got := dpiOptions(tt.mode, tt.split, true).FinalOutbound(); got != tt.want {
			t.Errorf("%s/%s final = %q, want %q", tt.mode, tt.split, got, tt.want)
		}
	}
}

// TestDPIBypassKeepsLANDirect is the hard rule: LAN never touches the helper. Any
// ip_is_private rule stays on direct, in every mode and split state.
func TestDPIBypassKeepsLANDirect(t *testing.T) {
	for _, m := range dpiModes {
		for _, sm := range dpiSplits {
			for _, lan := range []bool{true, false} {
				o := dpiOptions(m, sm, true)
				o.BypassLAN = lan
				for _, r := range o.RouteRules() {
					if _, has := r["ip_is_private"]; !has {
						continue
					}
					if r["outbound"] != tagDirect {
						t.Errorf("%s/%s lan=%v: LAN rule routes to %v, want %q", m, sm, lan, r["outbound"], tagDirect)
					}
				}
			}
		}
	}
}

// TestDPIBypassGuardsLANWhenFinalIsHelper closes the gap the LAN toggle leaves:
// when unmatched traffic falls through to the helper, LAN would ride along unless
// a rule pulls it back to direct — so that rule must exist even with the LAN
// bypass off.
func TestDPIBypassGuardsLANWhenFinalIsHelper(t *testing.T) {
	for _, m := range dpiModes {
		for _, sm := range dpiSplits {
			o := dpiOptions(m, sm, true)
			o.BypassLAN = false
			if o.FinalOutbound() != tagDPI {
				continue
			}
			var guarded bool
			for _, r := range o.RouteRules() {
				if r["ip_is_private"] == true && r["outbound"] == tagDirect {
					guarded = true
				}
			}
			if !guarded {
				t.Errorf("%s/%s: final is the helper but LAN has no direct guard: %v", m, sm, o.RouteRules())
			}
		}
	}
}

// TestDPIBypassEmitsOneLANRule guards against the guard and the LAN bypass both
// firing and emitting the same rule twice.
func TestDPIBypassEmitsOneLANRule(t *testing.T) {
	for _, m := range dpiModes {
		for _, sm := range dpiSplits {
			o := dpiOptions(m, sm, true) // BypassLAN on
			var n int
			for _, r := range o.RouteRules() {
				if _, has := r["ip_is_private"]; has {
					n++
				}
			}
			if n > 1 {
				t.Errorf("%s/%s emitted %d ip_is_private rules, want at most 1", m, sm, n)
			}
		}
	}
}

// TestDPIBinaryNamePerPlatform pins the executable name the process rule matches;
// it has to be the file name the runner actually spawns or the loop guard silently
// matches nothing.
func TestDPIBinaryNamePerPlatform(t *testing.T) {
	got := dpiBinaryName()
	want := "ciadpi"
	if runtime.GOOS == "windows" {
		want = "ciadpi.exe"
	}
	if got != want {
		t.Errorf("dpiBinaryName() = %q, want %q", got, want)
	}
}

// TestDPIBypassLeavesDNSAlone: the helper speaks SOCKS5, not DNS, so no resolver
// and no DNS rule may detour through it in any mode.
func TestDPIBypassLeavesDNSAlone(t *testing.T) {
	for _, m := range dpiModes {
		for _, sm := range dpiSplits {
			dns := dpiOptions(m, sm, true).DNS()
			for _, s := range dns["servers"].([]map[string]any) {
				if s["detour"] == tagDPI {
					t.Errorf("%s/%s DNS server %v detours through the helper", m, sm, s["tag"])
				}
			}
			for _, r := range dns["rules"].([]map[string]any) {
				if r["server"] == tagDPI || r["outbound"] == tagDPI {
					t.Errorf("%s/%s DNS rule points at the helper: %v", m, sm, r)
				}
			}
			if dns["final"] == tagDPI {
				t.Errorf("%s/%s DNS final = %v", m, sm, dns["final"])
			}
			raw, err := json.Marshal(dns)
			if err != nil {
				t.Fatalf("%s/%s dns does not marshal: %v", m, sm, err)
			}
			if strings.Contains(string(raw), `"`+tagDPI+`"`) {
				t.Errorf("%s/%s dns block mentions the helper: %s", m, sm, raw)
			}
		}
	}
}

// TestDPIBypassRuleSetsStayDirect: the geodata download detour is the one direct
// reference that must not move — sing-box fetches the rule-sets at startup, before
// the helper is guaranteed to be answering.
func TestDPIBypassRuleSetsStayDirect(t *testing.T) {
	for _, set := range dpiOptions(ModeSmart, SplitOff, true).RouteRuleSets() {
		if d, has := set["download_detour"]; has && d != tagDirect {
			t.Errorf("rule-set %v download_detour = %v, want %q", set["tag"], d, tagDirect)
		}
	}
}

// TestDPIBypassRulesMarshal guards against emitting anything encoding/json chokes
// on, the same way the split and DNS suites do.
func TestDPIBypassRulesMarshal(t *testing.T) {
	for _, m := range dpiModes {
		for _, sm := range dpiSplits {
			if _, err := json.Marshal(dpiOptions(m, sm, true).RouteRules()); err != nil {
				t.Fatalf("%s/%s route rules do not marshal: %v", m, sm, err)
			}
		}
	}
}
