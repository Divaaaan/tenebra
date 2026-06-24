package control

import (
	"testing"

	"github.com/Divaaaan/tenebra/core/routing"
)

// TestSetRuleSetDirThreadsIntoRouting proves the wiring: a dir installed via
// SetRuleSetDir lands on the daemon's live routing options, so the next connect
// builds local rule-sets and never blocks on a GitHub download.
func TestSetRuleSetDirThreadsIntoRouting(t *testing.T) {
	h := newHarness(t)
	dir := "C:\\Tenebra\\resources"
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
	dir := "C:\\Tenebra\\resources"
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
// empty so the remote fallback stays in effect for dev builds.
func TestRuleSetDirEmptyByDefault(t *testing.T) {
	h := newHarness(t)
	if got := h.daemon.snapshotRouting().RuleSetDir; got != "" {
		t.Errorf("default RuleSetDir = %q, want empty", got)
	}
}
