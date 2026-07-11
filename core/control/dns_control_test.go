package control

import (
	"encoding/json"
	"testing"

	"github.com/Divaaaan/tenebra/core/routing"
)

// dnsFromConfig extracts the "dns" block from a built config.
func dnsFromConfig(t *testing.T, cfgJSON []byte) map[string]any {
	t.Helper()
	var cfg struct {
		DNS map[string]any `json:"dns"`
	}
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		t.Fatalf("parse config dns: %v", err)
	}
	return cfg.DNS
}

// dnsHasAdBlockReject reports whether the dns block carries the ad-block sinkhole:
// a reject action matching the geosite-ads rule-set. Values are inspected after a
// JSON round-trip, so slices are []any and strings are any.
func dnsHasAdBlockReject(dns map[string]any) bool {
	rules, _ := dns["rules"].([]any)
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok || m["action"] != "reject" {
			continue
		}
		sets, _ := m["rule_set"].([]any)
		for _, s := range sets {
			if s == "geosite-ads" {
				return true
			}
		}
	}
	return false
}

// routeRuleSetTypes maps each route.rule_set tag to its type ("local"/"remote").
func routeRuleSetTypes(t *testing.T, cfgJSON []byte) map[string]string {
	t.Helper()
	var cfg struct {
		Route struct {
			RuleSet []map[string]any `json:"rule_set"`
		} `json:"route"`
	}
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		t.Fatalf("parse config route: %v", err)
	}
	out := map[string]string{}
	for _, s := range cfg.Route.RuleSet {
		tag, _ := s["tag"].(string)
		typ, _ := s["type"].(string)
		out[tag] = typ
	}
	return out
}

// TestSetDNSRecordsAndReports: set_dns records the ad-block toggle and both custom
// resolvers, echoes them in its State, and status reflects them; the daemon's live
// routing options carry them for the next connect.
func TestSetDNSRecordsAndReports(t *testing.T) {
	h := newHarness(t)

	h.send(Request{
		ID:        1,
		Cmd:       CmdSetDNS,
		AdBlock:   true,
		DNSRemote: "tls://9.9.9.9",
		DNSDirect: "udp://8.8.8.8",
	})
	var st State
	h.dataInto(h.await(), &st)
	if !st.AdBlock {
		t.Errorf("ad_block = false, want true")
	}
	if st.DNSRemote != "tls://9.9.9.9" || st.DNSDirect != "udp://8.8.8.8" {
		t.Errorf("resolvers = %q / %q, want the custom pair", st.DNSRemote, st.DNSDirect)
	}

	// Status reflects it.
	h.send(Request{ID: 2, Cmd: CmdStatus})
	var st2 State
	h.dataInto(h.await(), &st2)
	if !st2.AdBlock || st2.DNSRemote != "tls://9.9.9.9" || st2.DNSDirect != "udp://8.8.8.8" {
		t.Errorf("status DNS = %v / %q / %q, want armed with the custom pair", st2.AdBlock, st2.DNSRemote, st2.DNSDirect)
	}

	// The daemon's live routing options carry it so the next connect uses it.
	ro := h.daemon.snapshotRouting()
	if !ro.AdBlock || ro.DNSRemote != "tls://9.9.9.9" || ro.DNSDirect != "udp://8.8.8.8" {
		t.Errorf("daemon routing DNS = %v / %q / %q, want the custom pair armed", ro.AdBlock, ro.DNSRemote, ro.DNSDirect)
	}
}

// TestSetDNSRejectsInvalidResolver: a malformed resolver is refused before
// anything is recorded, so garbage never reaches sing-box.
func TestSetDNSRejectsInvalidResolver(t *testing.T) {
	h := newHarness(t)

	h.send(Request{ID: 1, Cmd: CmdSetDNS, DNSRemote: "not a resolver"})
	r := h.await()
	if r.Ok {
		t.Fatal("set_dns accepted a malformed remote resolver")
	}

	// Nothing was recorded: the live options keep the defaults.
	ro := h.daemon.snapshotRouting()
	if ro.DNSRemote != routing.DefaultDNSRemote {
		t.Errorf("remote resolver = %q after a rejected set, want the untouched default", ro.DNSRemote)
	}

	// A bad direct resolver is likewise refused.
	h.send(Request{ID: 2, Cmd: CmdSetDNS, DNSDirect: "http://1.1.1.1"})
	if h.await().Ok {
		t.Fatal("set_dns accepted a non-DNS scheme for the direct resolver")
	}
}

// TestSetDNSEmptyFallsBackToDefault: clearing a resolver reports the default back,
// so the UI always shows an effective value.
func TestSetDNSEmptyFallsBackToDefault(t *testing.T) {
	h := newHarness(t)

	h.send(Request{ID: 1, Cmd: CmdSetDNS, AdBlock: true, DNSRemote: "", DNSDirect: ""})
	var st State
	h.dataInto(h.await(), &st)
	if st.DNSRemote != routing.DefaultDNSRemote {
		t.Errorf("remote resolver = %q, want the default %q", st.DNSRemote, routing.DefaultDNSRemote)
	}
	if st.DNSDirect != routing.DefaultDNSDirect {
		t.Errorf("direct resolver = %q, want the default %q", st.DNSDirect, routing.DefaultDNSDirect)
	}
	if !st.AdBlock {
		t.Errorf("ad_block = false, want the toggle to survive an empty-resolver submit")
	}
}

// TestSetDNSPersistsAcrossRestart: the ad-block toggle and custom resolvers
// round-trip through the settings file into a fresh daemon.
func TestSetDNSPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetDNS, AdBlock: true, DNSRemote: "tls://9.9.9.9", DNSDirect: "quic://dns.adguard.com"})
	h.await()

	// A "restarted" daemon over the same directory loads them back.
	h2 := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)
	loaded := h2.daemon.snapshotState()
	if !loaded.AdBlock {
		t.Error("ad-block did not survive the restart")
	}
	if loaded.DNSRemote != "tls://9.9.9.9" || loaded.DNSDirect != "quic://dns.adguard.com" {
		t.Errorf("resolvers after restart = %q / %q, want the custom pair", loaded.DNSRemote, loaded.DNSDirect)
	}
}

// TestSetDNSLiveReapplyInjectsAdBlockRule: arming ad-block on a live tunnel
// hot-swaps sing-box in place, and the new config's dns block carries the reject
// rule backed by a LOCAL geosite-ads rule-set (never remote).
func TestSetDNSLiveReapplyInjectsAdBlockRule(t *testing.T) {
	h := newHarness(t)
	// The blocklist is local-only, so ad-block is inert without a rule-set dir; the
	// fake runner never reads the path, so any directory drives the config shape.
	h.daemon.SetRuleSetDir("C:\\res")
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)
	node := connected["node"].(string)

	h.send(Request{ID: 2, Cmd: CmdSetDNS, AdBlock: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.AdBlock {
		t.Fatal("response ad_block = false, want true")
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
	if dnsHasAdBlockReject(dnsFromConfig(t, cfgs[0])) {
		t.Error("initial config already carried the ad-block reject rule")
	}
	swapped := cfgs[1]
	if !dnsHasAdBlockReject(dnsFromConfig(t, swapped)) {
		t.Error("hot-swapped config lacks the ad-block reject rule")
	}
	// The reject rule references geosite-ads, which must be DEFINED and LOCAL in the
	// route block — a dangling reference or a remote set would FATAL/freeze sing-box.
	if typ := routeRuleSetTypes(t, swapped)["geosite-ads"]; typ != "local" {
		t.Errorf("geosite-ads rule_set type = %q, want local (defined, never remote)", typ)
	}
}

// TestSetDNSUnchangedDoesNotRestart: re-sending the current DNS config must not
// bounce a live tunnel.
func TestSetDNSUnchangedDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	// Resend the effective defaults (ad-block off, empty resolvers → the defaults):
	// nothing changed, so no hot swap.
	h.send(Request{ID: 2, Cmd: CmdSetDNS, AdBlock: false, DNSRemote: "", DNSDirect: ""})
	h.await()
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (a no-op set_dns must not restart)", h.runner.starts())
	}
}

// TestSetDNSRecordsIPv4Only: set_dns records the IPv4-only toggle, echoes it in
// its State, status reflects it, and the daemon's live routing options carry it
// for the next connect. Disarming clears it again.
func TestSetDNSRecordsIPv4Only(t *testing.T) {
	h := newHarness(t)

	h.send(Request{ID: 1, Cmd: CmdSetDNS, IPv4Only: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.IPv4Only {
		t.Errorf("ipv4_only = false, want true")
	}

	// Status reflects it.
	h.send(Request{ID: 2, Cmd: CmdStatus})
	var st2 State
	h.dataInto(h.await(), &st2)
	if !st2.IPv4Only {
		t.Errorf("status ipv4_only = false, want true")
	}

	// The daemon's live routing options carry it so the next connect uses it.
	if ro := h.daemon.snapshotRouting(); !ro.IPv4Only {
		t.Errorf("daemon routing ipv4_only = false, want armed")
	}

	// Disarming clears the flag (omitted when off, so it decodes back to false).
	h.send(Request{ID: 3, Cmd: CmdSetDNS, IPv4Only: false})
	var st3 State
	h.dataInto(h.await(), &st3)
	if st3.IPv4Only {
		t.Errorf("ipv4_only = true after disarming, want false")
	}
}

// TestSetDNSIPv4OnlyPersistsAcrossRestart: the IPv4-only toggle round-trips
// through the settings file into a fresh daemon.
func TestSetDNSIPv4OnlyPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetDNS, IPv4Only: true})
	h.await()

	// A "restarted" daemon over the same directory loads it back.
	h2 := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)
	if !h2.daemon.snapshotState().IPv4Only {
		t.Error("ipv4_only did not survive the restart")
	}
}

// TestSetDNSIPv4OnlyLiveReapply: arming IPv4-only on a live tunnel hot-swaps
// sing-box in place, and the new config's dns block pins strategy ipv4_only; a
// resend of the same value must not bounce the tunnel again.
func TestSetDNSIPv4OnlyLiveReapply(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)
	node := connected["node"].(string)

	h.send(Request{ID: 2, Cmd: CmdSetDNS, IPv4Only: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.IPv4Only {
		t.Fatal("response ipv4_only = false, want true")
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
	if _, has := dnsFromConfig(t, cfgs[0])["strategy"]; has {
		t.Error("initial config already pinned a dns strategy")
	}
	if got := dnsFromConfig(t, cfgs[1])["strategy"]; got != "ipv4_only" {
		t.Errorf("hot-swapped dns strategy = %v, want ipv4_only", got)
	}

	// Re-sending the same value changes nothing, so no second hot swap.
	h.send(Request{ID: 3, Cmd: CmdSetDNS, IPv4Only: true})
	h.await()
	if got := h.runner.starts(); got != 2 {
		t.Errorf("starts = %d after a no-op set_dns, want 2 (no extra restart)", got)
	}
}

// TestSetDNSIPv4OnlyAbsentDefaultsOff: a settings file written before the field
// existed carries no ipv4_only key (false is dropped by omitempty), and a fresh
// daemon reads it back as off — while a sibling preference still loads, proving
// the file was actually read rather than rejected.
func TestSetDNSIPv4OnlyAbsentDefaultsOff(t *testing.T) {
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
	if loaded.IPv4Only {
		t.Error("ipv4_only should default off when absent from the settings file")
	}
	if !loaded.KillSwitch {
		t.Error("the sibling preference did not load, so the file was not actually read")
	}
}
