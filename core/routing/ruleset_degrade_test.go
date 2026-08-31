package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The failure these tests exist for: sing-box refuses to start when a rule_set
// names a path it cannot open ("parse rule-set: cannot find the path"), and the
// remote form it used to fall back to is worse still on the networks this client
// is built for — raw.githubusercontent.com times out after five seconds and the
// process exits anyway. Either way every candidate node in the fallback walk dies
// at launch, and the user is told "all protocols failed", which reads as an
// accusation against the servers for a fault entirely on their own disk.
//
// So the rule is: no geodata, no geo rules. Smart mode routes like global for the
// session and says so.

// hasRuleSetRef reports whether any rule names a rule_set.
func hasRuleSetRef(rules []map[string]any) bool {
	for _, r := range rules {
		if _, ok := r["rule_set"]; ok {
			return true
		}
	}
	return false
}

// TestSmartWithoutRuleSetsEmitsNoGeoRules: with no RuleSetDir at all, smart mode
// must emit neither the rule_set definitions nor anything referencing them, on
// either layer.
func TestSmartWithoutRuleSetsEmitsNoGeoRules(t *testing.T) {
	o := (Options{Mode: ModeSmart}).Normalize()

	if rs := o.RouteRuleSets(); len(rs) != 0 {
		t.Errorf("rule_set definitions without a bundle = %v, want none", rs)
	}
	if hasRuleSetRef(o.RouteRules()) {
		t.Errorf("route rules reference a rule-set that is not defined: %v", o.RouteRules())
	}
	if hasRuleSetRef(o.dnsRules()) {
		t.Errorf("dns rules reference a rule-set that is not defined: %v", o.dnsRules())
	}
}

// TestSmartWithMissingSRSFileEmitsNoGeoRules is the case a start-of-day check
// cannot see: the directory is configured and looks right, but the file is not
// there — an interrupted update, a quarantined file, a half-copied install. The
// presence check has to run while the config is built, not once when the daemon
// started.
func TestSmartWithMissingSRSFileEmitsNoGeoRules(t *testing.T) {
	// One of the pair present, the other missing: half the geodata is not a smart
	// split, and naming the missing half is fatal.
	for _, present := range [][]string{
		{},
		{fileGeoIPRU},
		{fileGeositeRU},
	} {
		dir := ruleSetDirWith(t, present...)
		o := (Options{Mode: ModeSmart, RuleSetDir: dir}).Normalize()
		if rs := o.RouteRuleSets(); len(rs) != 0 {
			t.Errorf("with %v present: rule_set definitions = %v, want none", present, rs)
		}
		if hasRuleSetRef(o.RouteRules()) {
			t.Errorf("with %v present: route rules still reference a rule-set", present)
		}
		if hasRuleSetRef(o.dnsRules()) {
			t.Errorf("with %v present: dns rules still reference a rule-set", present)
		}
	}
}

// TestSmartWithoutRuleSetsStillTunnels: degrading must not disarm the tunnel.
// Everything unmatched keeps going to the proxy — that is what "smart behaves as
// global" means, and it is the whole reason a missing file is allowed to be a
// degradation instead of a refusal to connect.
func TestSmartWithoutRuleSetsStillTunnels(t *testing.T) {
	o := (Options{Mode: ModeSmart}).Normalize()
	if got := o.FinalOutbound(); got != tagProxy {
		t.Errorf("degraded smart final = %q, want %q", got, tagProxy)
	}
	if got := o.dnsFinal(); got != dnsRemoteTag {
		t.Errorf("degraded smart dns final = %q, want %q", got, dnsRemoteTag)
	}
	// The sniff/hijack pair is still there, so the config is a working config and
	// not an empty one.
	rules := o.RouteRules()
	if len(rules) < 2 || rules[0]["action"] != "sniff" {
		t.Errorf("degraded smart rules = %v, want the usual leading sniff/hijack", rules)
	}
}

// TestDegradedSmartMatchesGlobal states the promise exactly: with the geodata
// gone, smart emits the same rules global does. Anything else would be a third,
// untested routing shape reachable only by accident.
func TestDegradedSmartMatchesGlobal(t *testing.T) {
	degraded, err := json.Marshal((Options{Mode: ModeSmart}).Normalize().RouteRules())
	if err != nil {
		t.Fatalf("marshal degraded: %v", err)
	}
	global, err := json.Marshal((Options{Mode: ModeGlobal}).Normalize().RouteRules())
	if err != nil {
		t.Fatalf("marshal global: %v", err)
	}
	if string(degraded) != string(global) {
		t.Errorf("degraded smart rules differ from global:\ndegraded = %s\nglobal   = %s", degraded, global)
	}
}

// TestNoRemoteRuleSetsEverEmitted: the remote form is gone from the build path
// entirely. It was never a fallback here — on a network where GitHub is blocked
// it is a guaranteed startup failure five seconds in — so no config may carry a
// remote rule-set or the fields that make one.
func TestNoRemoteRuleSetsEverEmitted(t *testing.T) {
	dir := ruleSetDir(t)
	for _, o := range []Options{
		{Mode: ModeSmart},
		{Mode: ModeSmart, RuleSetDir: dir},
		{Mode: ModeSmart, RuleSetDir: dir, AdBlock: true},
		{Mode: ModeGlobal, RuleSetDir: dir, AdBlock: true},
		{Mode: ModeSmart, RuleSetDir: filepath.Join(dir, "gone")},
	} {
		for _, set := range o.Normalize().RouteRuleSets() {
			if set["type"] != "local" {
				t.Errorf("%v: rule_set %v type = %v, want local", o.Mode, set["tag"], set["type"])
			}
			for _, k := range []string{"url", "download_detour", "update_interval"} {
				if _, has := set[k]; has {
					t.Errorf("%v: rule_set %v carries the remote field %q", o.Mode, set["tag"], k)
				}
			}
		}
	}
}

// TestEveryReferencedRuleSetIsDefined is the invariant underneath all of the
// above, checked across the switch combinations rather than case by case: a tag
// used by a route or dns rule must appear in the definitions, because sing-box
// treats a dangling reference as a fatal config error.
func TestEveryReferencedRuleSetIsDefined(t *testing.T) {
	full := ruleSetDir(t)
	geoOnly := ruleSetDirWith(t, fileGeoIPRU, fileGeositeRU)
	adsOnly := ruleSetDirWith(t, fileGeositeAds)
	empty := ruleSetDirWith(t)

	for _, dir := range []string{"", full, geoOnly, adsOnly, empty} {
		for _, mode := range []Mode{ModeSmart, ModeGlobal, ModeDirect} {
			for _, adBlock := range []bool{false, true} {
				o := (Options{Mode: mode, RuleSetDir: dir, AdBlock: adBlock}).Normalize()
				defined := map[string]bool{}
				for _, set := range o.RouteRuleSets() {
					defined[set["tag"].(string)] = true
				}
				check := func(layer string, rules []map[string]any) {
					for _, r := range rules {
						tags, ok := r["rule_set"].([]string)
						if !ok {
							continue
						}
						for _, tag := range tags {
							if !defined[tag] {
								t.Errorf("%s/%v adblock=%v dir=%q: %s rule names undefined rule_set %q",
									mode, dir, adBlock, dir, layer, tag)
							}
						}
					}
				}
				check("route", o.RouteRules())
				check("dns", o.dnsRules())
			}
		}
	}
}

// TestAdBlockNeedsOnlyItsOwnFile covers the other half of the old all-or-nothing
// gate. The blocklist is optional; the geodata is not. A bundle carrying the RU
// pair but no blocklist must still give smart mode its geo split, and switching
// ad-blocking on there is a no-op rather than a config that will not start.
func TestAdBlockNeedsOnlyItsOwnFile(t *testing.T) {
	geoOnly := ruleSetDirWith(t, fileGeoIPRU, fileGeositeRU)
	o := (Options{Mode: ModeSmart, RuleSetDir: geoOnly, AdBlock: true}).Normalize()

	if !o.smartGeoActive() {
		t.Error("a missing ad blocklist disabled the RU geo split")
	}
	for _, set := range o.RouteRuleSets() {
		if set["tag"] == ruleSetGeositeAds {
			t.Errorf("ads rule-set defined without its file: %v", set)
		}
	}
	for _, r := range o.dnsRules() {
		if r["action"] == "reject" {
			t.Errorf("ad-block reject rule emitted without the blocklist file: %v", r)
		}
	}

	// And the converse: only the blocklist present means ad-blocking works while
	// smart mode degrades.
	adsOnly := ruleSetDirWith(t, fileGeositeAds)
	only := (Options{Mode: ModeSmart, RuleSetDir: adsOnly, AdBlock: true}).Normalize()
	if only.smartGeoActive() {
		t.Error("geo split emitted with no geodata on disk")
	}
	var rejects int
	for _, r := range only.dnsRules() {
		if r["action"] == "reject" {
			rejects++
		}
	}
	if rejects != 1 {
		t.Errorf("ad-block reject rules = %d, want 1", rejects)
	}
}

// TestSmartGeoDegradedReportsTheMissingFiles: the daemon logs this, so it has to
// name what is missing and where it looked. "Geodata unavailable" is not
// something a user can act on; a path is.
func TestSmartGeoDegradedReportsTheMissingFiles(t *testing.T) {
	full := ruleSetDir(t)
	if o := (Options{Mode: ModeSmart, RuleSetDir: full}).Normalize(); o.SmartGeoDegraded() {
		t.Error("a complete bundle still reported smart as degraded")
	} else if got := o.MissingRuleSets(); len(got) != 0 {
		t.Errorf("complete bundle missing = %v, want none", got)
	}

	partial := ruleSetDirWith(t, fileGeoIPRU)
	o := (Options{Mode: ModeSmart, RuleSetDir: partial}).Normalize()
	if !o.SmartGeoDegraded() {
		t.Error("a half-present bundle did not report smart as degraded")
	}
	missing := o.MissingRuleSets()
	if len(missing) != 1 || missing[0] != filepath.Join(partial, fileGeositeRU) {
		t.Errorf("missing = %v, want just the geosite path under %q", missing, partial)
	}

	// Non-smart modes need no geodata, so they are never degraded by its absence.
	for _, mode := range []Mode{ModeGlobal, ModeDirect} {
		if (Options{Mode: mode}).Normalize().SmartGeoDegraded() {
			t.Errorf("%s reported as degraded although it uses no geodata", mode)
		}
	}

	// Ad-blocking contributes its own file to the report, in every mode.
	ads := (Options{Mode: ModeGlobal, AdBlock: true, RuleSetDir: partial}).Normalize()
	got := ads.MissingRuleSets()
	if len(got) != 1 || got[0] != filepath.Join(partial, fileGeositeAds) {
		t.Errorf("adblock missing = %v, want the blocklist path", got)
	}
}

// TestRuleSetDirectoryIsNotAFile: a directory named geoip-ru.srs is not a
// rule-set. os.Stat succeeds on it, so a naive existence check would hand
// sing-box a path it cannot parse — the same FATAL by a different route.
func TestRuleSetDirectoryIsNotAFile(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{fileGeoIPRU, fileGeositeRU} {
		if err := os.Mkdir(filepath.Join(dir, f), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", f, err)
		}
	}
	if (Options{Mode: ModeSmart, RuleSetDir: dir}).Normalize().smartGeoActive() {
		t.Error("a directory standing in for a .srs was accepted as the rule-set")
	}
}

// TestRuleSetPresenceIsRecheckedPerBuild: the same Options value must answer
// differently once the file appears, because the check belongs to the moment the
// config is assembled. Caching the answer is how a stale "yes" survives an update
// that emptied the directory.
func TestRuleSetPresenceIsRecheckedPerBuild(t *testing.T) {
	dir := t.TempDir()
	o := (Options{Mode: ModeSmart, RuleSetDir: dir}).Normalize()
	if len(o.RouteRuleSets()) != 0 {
		t.Fatal("empty directory produced rule-set definitions")
	}
	for _, f := range []string{fileGeoIPRU, fileGeositeRU} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("srs"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", f, err)
		}
	}
	if len(o.RouteRuleSets()) != 2 {
		t.Error("files appearing on disk did not bring the geo split back")
	}
	if err := os.Remove(filepath.Join(dir, fileGeositeRU)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(o.RouteRuleSets()) != 0 {
		t.Error("a file disappearing did not take the geo split with it")
	}
}
