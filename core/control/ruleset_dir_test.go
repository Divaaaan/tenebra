package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Divaaaan/tenebra/core/routing"
)

// ruleSetBundle returns a temp directory holding the bundled .srs binaries. The
// routing layer stats them while it builds a config rather than trusting the
// path it was handed, so a test that expects the geo split needs real files.
func ruleSetBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"geoip-ru.srs", "geosite-ru.srs", "geosite-ads.srs"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("srs"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", f, err)
		}
	}
	return dir
}

// TestSetRuleSetDirThreadsIntoRouting proves the wiring: a dir installed via
// SetRuleSetDir lands on the daemon's live routing options, so the next connect
// builds local rule-sets and never blocks on a GitHub download.
func TestSetRuleSetDirThreadsIntoRouting(t *testing.T) {
	h := newHarness(t)
	dir := ruleSetBundle(t)
	h.daemon.SetRuleSetDir(dir)

	if got := h.daemon.snapshotRouting().RuleSetDir; got != dir {
		t.Errorf("routing RuleSetDir = %q, want %q", got, dir)
	}
}

// TestSetRuleSetDirSurvivesSplitChange guards against a later Normalize (run by
// set_split / set_routing) dropping the dir: the rule-set source must not depend
// on whether the user has touched split tunnelling.
func TestSetRuleSetDirSurvivesSplitChange(t *testing.T) {
	h := newHarness(t)
	dir := ruleSetBundle(t)
	h.daemon.SetRuleSetDir(dir)

	h.send(Request{ID: 1, Cmd: CmdSetSplit, Mode: "exclude", Apps: []string{"chrome.exe"}})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdSetRouting, Mode: "global"})
	h.await()

	ro := h.daemon.snapshotRouting()
	if ro.RuleSetDir != dir {
		t.Errorf("RuleSetDir after split/routing changes = %q, want %q", ro.RuleSetDir, dir)
	}
	// And it still produces local rule-sets when back in smart mode.
	ro.Mode = routing.ModeSmart
	rs := ro.Normalize().RouteRuleSets()
	if len(rs) != 2 || rs[0]["type"] != "local" {
		t.Errorf("rule-sets after changes = %v, want 2 local", rs)
	}
}

// TestRuleSetDirEmptyByDefault: a bare daemon (no SetRuleSetDir) keeps the dir
// empty, which is how a dev build ends up with smart mode routing like global.
func TestRuleSetDirEmptyByDefault(t *testing.T) {
	h := newHarness(t)
	ro := h.daemon.snapshotRouting()
	if got := ro.RuleSetDir; got != "" {
		t.Errorf("default RuleSetDir = %q, want empty", got)
	}
	// And that state is reported rather than silent: the connect path logs it, so
	// SmartGeoDegraded has to be the thing that says so.
	ro.Mode = routing.ModeSmart
	if !ro.Normalize().SmartGeoDegraded() {
		t.Error("smart mode with no bundle did not report itself degraded")
	}
}

// TestConnectWarnsWhenSmartHasNoGeodata: the degradation is invisible from
// outside — the mode still reads "smart", the tunnel still comes up, and every
// Russian destination quietly takes the long way round. The connect path has to
// say so, and name the file it could not find, because that is the only part the
// user can act on.
func TestConnectWarnsWhenSmartHasNoGeodata(t *testing.T) {
	h := newHarness(t)
	h.daemon.SetRuleSetDir(t.TempDir()) // configured, but empty

	ro := h.daemon.snapshotRouting()
	ro.Mode = routing.ModeSmart
	ro = ro.Normalize()
	if !ro.SmartGeoDegraded() {
		t.Fatal("an empty rule-set directory did not degrade smart mode")
	}
	missing := ro.MissingRuleSets()
	if len(missing) != 2 {
		t.Fatalf("missing rule-sets = %v, want both RU files", missing)
	}
	for _, m := range missing {
		if !filepath.IsAbs(m) {
			t.Errorf("missing rule-set %q is not a path anyone can act on", m)
		}
	}
}
