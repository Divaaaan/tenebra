package routing

import (
	"reflect"
	"regexp"
	"testing"
)

func TestLiteralBasenamePatterns(t *testing.T) {
	for _, split := range []SplitMode{SplitInclude, SplitExclude} {
		o := (Options{SplitMode: split, SplitApps: []string{"App[1].exe", "Приложение+.exe"}}).Normalize()
		var patterns []string
		for _, rule := range o.RouteRules() {
			if p, ok := rule["process_path_regex"].([]string); ok {
				patterns = append(patterns, p...)
			}
		}
		if len(patterns) != 2 {
			t.Fatalf("%s: literal app rules missing: %v", split, o.RouteRules())
		}
		matches := func(path string) bool {
			for _, p := range patterns {
				r, err := regexp.Compile(p)
				if err != nil {
					t.Fatal(err)
				}
				if r.MatchString(path) {
					return true
				}
			}
			return false
		}
		for _, path := range []string{"/opt/App[1].exe", `C:\apps\APP[1].EXE`, "App[1].exe", "c:/bin/ПРИЛОЖЕНИЕ+.EXE"} {
			if !matches(path) {
				t.Errorf("%s: expected literal match for %q", split, path)
			}
		}
		for _, path := range []string{"/opt/App1.exe", "prefixApp[1].exe", "App[1].exe.bak", "App[1].exe/child", "Приложениеее.exe"} {
			if matches(path) {
				t.Errorf("%s: unexpected app match for %q", split, path)
			}
		}
		for _, rule := range o.dnsRules() {
			if p, ok := rule["process_path_regex"].([]string); ok && !reflect.DeepEqual(p, patterns) {
				t.Errorf("DNS and route process policy differs")
			}
		}
	}
}

func TestNormalizeAppsKeepsDisplayCase(t *testing.T) {
	apps := []string{"  App[1].exe ", "APP[1].EXE", "Σ.exe", "ς.exe"}
	got := (Options{SplitApps: apps}).Normalize().SplitApps
	if !reflect.DeepEqual(got, []string{"App[1].exe", "Σ.exe"}) {
		t.Fatalf("display spelling/fold dedup lost: %v", got)
	}
	if apps[0] != "  App[1].exe " {
		t.Fatal("normalization mutated caller input")
	}
}

func TestLiteralBasenameValidation(t *testing.T) {
	for _, name := range []string{"/opt/app", `C:\apps\app.exe`, "app/child", "app\x00.exe", ".", ".."} {
		o := (Options{SplitMode: SplitInclude, SplitApps: []string{name}}).Normalize()
		if err := o.Validate(); err == nil {
			t.Errorf("accepted invalid basename %q", name)
		}
	}
	for _, name := range []string{"App[1].exe", "Приложение+.exe", "an app.exe"} {
		o := (Options{SplitMode: SplitInclude, SplitApps: []string{name}}).Normalize()
		if err := o.Validate(); err != nil {
			t.Errorf("rejected literal basename %q: %v", name, err)
		}
	}
}

func TestIncludePolicyAllBaseModes(t *testing.T) {
	for _, mode := range []Mode{ModeSmart, ModeGlobal, ModeDirect} {
		for _, apps := range [][]string{nil, {"App.exe"}} {
			for _, killSwitch := range []bool{false, true} {
				o := (Options{Mode: mode, SplitMode: SplitInclude, SplitApps: apps,
					RulesProxy: []string{"example.com"}, UnblockServices: true, KillSwitch: killSwitch}).Normalize()
				if o.FinalOutbound() != tagDirect {
					t.Errorf("%s apps=%v: unlisted traffic enters VPN", mode, apps)
				}
				if o.dnsFinal() != dnsDirectTag {
					t.Errorf("%s apps=%v: unlisted DNS enters VPN", mode, apps)
				}
				for _, r := range o.RouteRules() {
					if r["outbound"] == tagProxy && r["process_path_regex"] == nil {
						t.Errorf("%s apps=%v: unlisted proxy rule: %v", mode, apps, r)
					}
				}
				for _, r := range o.dnsRules() {
					if r["server"] == dnsRemoteTag && r["process_path_regex"] == nil {
						t.Errorf("%s apps=%v: unlisted proxy DNS: %v", mode, apps, r)
					}
				}
				assertSniffHijackFirst(t, o.RouteRules())
			}
		}
	}
}

func TestLANBeforeIncludeOnlyWhenEnabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, killSwitch := range []bool{false, true} {
			o := (Options{Mode: ModeGlobal, SplitMode: SplitInclude, SplitApps: []string{"App.exe"}, BypassLAN: enabled, KillSwitch: killSwitch}).Normalize()
			lan, app := -1, -1
			for i, rule := range o.RouteRules() {
				if rule["ip_is_private"] == true {
					lan = i
				}
				if rule["process_path_regex"] != nil || rule["process_name"] != nil {
					app = i
				}
			}
			if enabled && !killSwitch {
				if lan != 2 || app <= lan {
					t.Errorf("LAN exception must precede app rule: lan=%d app=%d", lan, app)
				}
			} else if lan != -1 {
				t.Errorf("unexpected LAN bypass enabled=%v killSwitch=%v", enabled, killSwitch)
			}
		}
	}
}
