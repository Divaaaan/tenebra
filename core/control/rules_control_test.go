package control

import (
	"encoding/json"
	"testing"
)

// routeRulesFromConfig extracts the ordered "route".rules from a built config.
// Values come back post-JSON-round-trip, so slices are []any and scalars are any.
func routeRulesFromConfig(t *testing.T, cfgJSON []byte) []map[string]any {
	t.Helper()
	var cfg struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		t.Fatalf("parse config route rules: %v", err)
	}
	return cfg.Route.Rules
}

// suffixDNSRule reports whether the dns block carries a domain_suffix rule for
// the given suffix pointing at server.
func suffixDNSRule(dns map[string]any, suffix, server string) bool {
	rules, _ := dns["rules"].([]any)
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok || m["server"] != server {
			continue
		}
		sufs, _ := m["domain_suffix"].([]any)
		for _, s := range sufs {
			if s == suffix {
				return true
			}
		}
	}
	return false
}

// suffixRouteRuleContains reports whether a route rule pins suffix to outbound
// via a domain_suffix match.
func suffixRouteRuleContains(rules []map[string]any, suffix, outbound string) bool {
	for _, r := range rules {
		if r["outbound"] != outbound {
			continue
		}
		sufs, _ := r["domain_suffix"].([]any)
		for _, s := range sufs {
			if s == suffix {
				return true
			}
		}
	}
	return false
}

// TestSetRulesRecordsAndReports: set_rules records the custom rules and the
// presets, echoes them in its State, status reflects them, and the daemon's live
// routing options carry them for the next connect.
func TestSetRulesRecordsAndReports(t *testing.T) {
	h := newHarness(t)

	h.send(Request{
		ID:              1,
		Cmd:             CmdSetRules,
		RulesDirect:     []string{"Sberbank.ru", "sberbank.ru", "bank.example"},
		RulesProxy:      []string{"work.example"},
		PresetRuBanking: true,
	})
	var st State
	h.dataInto(h.await(), &st)

	// The response carries the normalized (lowercased, de-duplicated, sorted)
	// lists and the preset toggle.
	wantDirect := []string{"bank.example", "sberbank.ru"}
	if len(st.RulesDirect) != len(wantDirect) || st.RulesDirect[0] != wantDirect[0] || st.RulesDirect[1] != wantDirect[1] {
		t.Errorf("rules_direct = %v, want %v", st.RulesDirect, wantDirect)
	}
	if len(st.RulesProxy) != 1 || st.RulesProxy[0] != "work.example" {
		t.Errorf("rules_proxy = %v, want [work.example]", st.RulesProxy)
	}
	if !st.PresetRuBanking {
		t.Error("preset_ru_banking = false, want true")
	}
	if st.PresetRuGov {
		t.Error("preset_ru_gov = true, want false (never set)")
	}

	// Status reflects it.
	h.send(Request{ID: 2, Cmd: CmdStatus})
	var st2 State
	h.dataInto(h.await(), &st2)
	if len(st2.RulesDirect) != 2 || !st2.PresetRuBanking {
		t.Errorf("status rules = %v / banking %v, want the recorded pair", st2.RulesDirect, st2.PresetRuBanking)
	}

	// The daemon's live routing options carry it so the next connect uses it.
	ro := h.daemon.snapshotRouting()
	if len(ro.RulesDirect) != 2 || len(ro.RulesProxy) != 1 || !ro.PresetRuBanking {
		t.Errorf("daemon routing rules = %v / %v / banking %v, want the recorded set", ro.RulesDirect, ro.RulesProxy, ro.PresetRuBanking)
	}
}

// TestSetRulesRejectsInvalidSuffix: a malformed suffix is refused before anything
// is recorded, so garbage never reaches sing-box.
func TestSetRulesRejectsInvalidSuffix(t *testing.T) {
	h := newHarness(t)

	h.send(Request{ID: 1, Cmd: CmdSetRules, RulesDirect: []string{"ok.example", "https://nope"}})
	if h.await().Ok {
		t.Fatal("set_rules accepted a malformed direct suffix")
	}
	// Nothing was recorded: the live options keep their empty rule set.
	if ro := h.daemon.snapshotRouting(); len(ro.RulesDirect) != 0 {
		t.Errorf("direct rules = %v after a rejected set, want empty", ro.RulesDirect)
	}

	// A bad proxy suffix is likewise refused.
	h.send(Request{ID: 2, Cmd: CmdSetRules, RulesProxy: []string{"x.com:443"}})
	if h.await().Ok {
		t.Fatal("set_rules accepted a proxy suffix with a port")
	}
}

// TestSetRulesAbsentDefaultsEmpty: set_rules with no fields clears to empty and
// reports nothing, and the request omits the rule fields on the wire.
func TestSetRulesAbsentDefaultsEmpty(t *testing.T) {
	h := newHarness(t)

	h.send(Request{ID: 1, Cmd: CmdSetRules})
	var st State
	h.dataInto(h.await(), &st)
	if len(st.RulesDirect) != 0 || len(st.RulesProxy) != 0 || st.PresetRuBanking || st.PresetRuGov {
		t.Errorf("empty set_rules reported %v / %v / %v / %v, want all empty/off", st.RulesDirect, st.RulesProxy, st.PresetRuBanking, st.PresetRuGov)
	}
}

// TestSetRulesPersistsAcrossRestart: the rules and presets round-trip through the
// settings file into a fresh daemon.
func TestSetRulesPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{
		ID:          1,
		Cmd:         CmdSetRules,
		RulesDirect: []string{"bank.example"},
		RulesProxy:  []string{"work.example"},
		PresetRuGov: true,
	})
	h.await()

	// A "restarted" daemon over the same directory loads them back.
	h2 := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)
	loaded := h2.daemon.snapshotState()
	if len(loaded.RulesDirect) != 1 || loaded.RulesDirect[0] != "bank.example" {
		t.Errorf("direct rules after restart = %v, want [bank.example]", loaded.RulesDirect)
	}
	if len(loaded.RulesProxy) != 1 || loaded.RulesProxy[0] != "work.example" {
		t.Errorf("proxy rules after restart = %v, want [work.example]", loaded.RulesProxy)
	}
	if !loaded.PresetRuGov {
		t.Error("preset_ru_gov did not survive the restart")
	}
}

// TestSetRulesAbsentFromOldFileDefaultsEmpty: a settings file written before the
// fields existed carries no rules keys, and a fresh daemon reads them back empty —
// while a sibling preference still loads, proving the file was actually read.
func TestSetRulesAbsentFromOldFileDefaultsEmpty(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	if err := st.Save(persistedSettings{Version: settingsVersion, KillSwitch: true}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	h := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h.daemon.SetSettings(st2)
	loaded := h.daemon.snapshotState()
	if len(loaded.RulesDirect) != 0 || loaded.PresetRuBanking || loaded.PresetRuGov {
		t.Error("rules should default empty/off when absent from the settings file")
	}
	if !loaded.KillSwitch {
		t.Error("the sibling preference did not load, so the file was not actually read")
	}
}

// TestSetRulesLiveReapplyInjectsRules: setting rules on a live tunnel hot-swaps
// sing-box in place, and the new config's route block carries the custom rules
// (direct + proxy) plus their DNS mirrors — none of which the initial config had.
func TestSetRulesLiveReapplyInjectsRules(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)
	node := connected["node"].(string)

	h.send(Request{
		ID:          2,
		Cmd:         CmdSetRules,
		RulesDirect: []string{"bank.example"},
		RulesProxy:  []string{"work.example"},
	})
	var st State
	h.dataInto(h.await(), &st)
	if len(st.RulesDirect) != 1 {
		t.Fatalf("response rules_direct = %v, want [bank.example]", st.RulesDirect)
	}

	// The swap dips through connecting and lands connected on the same node.
	re := h.awaitState(StateConnected)
	if re["node"] != node {
		t.Errorf("reconnected node = %v, want the same node %s", re["node"], node)
	}

	cfgs := h.runner.startCfgs()
	if len(cfgs) != 2 {
		t.Fatalf("starts = %d, want 2 (one connect, one hot swap)", len(cfgs))
	}

	// The initial config carried no custom rule; the swapped one does, in both the
	// route block and its DNS mirror.
	initial := routeRulesFromConfig(t, cfgs[0])
	if suffixRouteRuleContains(initial, "bank.example", "direct") {
		t.Error("initial config already carried the custom direct rule")
	}
	swapped := cfgs[1]
	swappedRoutes := routeRulesFromConfig(t, swapped)
	if !suffixRouteRuleContains(swappedRoutes, "bank.example", "direct") {
		t.Error("hot-swapped route block lacks the custom direct rule")
	}
	if !suffixRouteRuleContains(swappedRoutes, "work.example", "proxy") {
		t.Error("hot-swapped route block lacks the custom proxy rule")
	}
	dns := dnsFromConfig(t, swapped)
	if !suffixDNSRule(dns, "bank.example", "dns-direct") {
		t.Error("hot-swapped dns block lacks the direct rule mirror (bank.example -> dns-direct)")
	}
	if !suffixDNSRule(dns, "work.example", "dns-remote") {
		t.Error("hot-swapped dns block lacks the proxy rule mirror (work.example -> dns-remote)")
	}
}

// TestSetRulesUnchangedDoesNotRestart: re-sending the current rules must not
// bounce a live tunnel.
func TestSetRulesUnchangedDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	// Record a rule, then connect so the tunnel already carries it.
	h.send(Request{ID: 1, Cmd: CmdSetRules, RulesDirect: []string{"bank.example"}})
	h.await()

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	startsAfterConnect := h.runner.starts()

	// Resend the same rule (order/casing differ but normalize equal): no change,
	// so no hot swap.
	h.send(Request{ID: 3, Cmd: CmdSetRules, RulesDirect: []string{"Bank.example"}})
	h.await()
	if got := h.runner.starts(); got != startsAfterConnect {
		t.Errorf("starts = %d, want %d (a no-op set_rules must not restart)", got, startsAfterConnect)
	}
}
