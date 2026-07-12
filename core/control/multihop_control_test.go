package control

import (
	"encoding/json"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

// These tests cover the set_multihop command end to end at the protocol level:
// validating the entry/exit selection, reporting and persisting it, resolving the
// stored server IDs into the built config's detour chain on connect, and
// hot-swapping a live tunnel in place when the selection changes — the same
// record/persist/reapply contract the kill switch and TLS-fragmentation toggles
// hold, extended with the two-node selection multihop needs.

// twoNodeProfile stores a manual profile with two renderable, distinctly named
// VLESS nodes and returns it. The server addresses differ so their stable IDs (and
// their sing-box tags "Entry"/"Exit") differ, giving set_multihop a valid pair.
func twoNodeProfile(t *testing.T, h *harness) profile.Profile {
	t.Helper()
	return h.addProfile([]model.Node{
		vlessNode("Entry", "entry.example.aa"),
		vlessNode("Exit", "exit.example.bb"),
	})
}

// selectedOutboundDetour returns the tag the config's selector defaults to and
// that outbound's detour (empty when it carries none), so a test can assert the
// multihop chain reached the exit that actually came up.
func selectedOutboundDetour(t *testing.T, cfgJSON []byte) (exitTag, detour string) {
	t.Helper()
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	def := ""
	for _, o := range cfg.Outbounds {
		if o["type"] == "selector" {
			def, _ = o["default"].(string)
		}
	}
	for _, o := range cfg.Outbounds {
		if o["tag"] == def {
			d, _ := o["detour"].(string)
			return def, d
		}
	}
	t.Fatalf("no outbound tagged %q in config", def)
	return "", ""
}

// TestSetMultihopValidation: enabling with a bad selection is rejected whole — the
// daemon must never half-apply a chain — while disabling ignores the IDs.
func TestSetMultihopValidation(t *testing.T) {
	h := newHarness(t)
	p := twoNodeProfile(t, h)
	entry := p.Servers[0].ID
	exit := p.Servers[1].ID

	cases := []struct {
		name    string
		req     Request
		wantErr bool
	}{
		{"equal endpoints", Request{Cmd: CmdSetMultihop, Enabled: true, Profile: p.ID, EntryID: entry, ExitID: entry}, true},
		{"empty entry", Request{Cmd: CmdSetMultihop, Enabled: true, Profile: p.ID, EntryID: "", ExitID: exit}, true},
		{"empty exit", Request{Cmd: CmdSetMultihop, Enabled: true, Profile: p.ID, EntryID: entry, ExitID: ""}, true},
		{"entry not in profile", Request{Cmd: CmdSetMultihop, Enabled: true, Profile: p.ID, EntryID: "nope", ExitID: exit}, true},
		{"profile not found", Request{Cmd: CmdSetMultihop, Enabled: true, Profile: "ghost", EntryID: entry, ExitID: exit}, true},
		{"valid pair", Request{Cmd: CmdSetMultihop, Enabled: true, Profile: p.ID, EntryID: entry, ExitID: exit}, false},
		{"disable ignores ids", Request{Cmd: CmdSetMultihop, Enabled: false, Profile: p.ID, EntryID: entry, ExitID: entry}, false},
	}
	id := int64(1)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.req.ID = id
			id++
			h.send(c.req)
			r := h.await()
			if c.wantErr && r.Ok {
				t.Errorf("expected an error, got ok with data %s", r.Data)
			}
			if !c.wantErr && !r.Ok {
				t.Errorf("expected ok, got error %q", r.Error)
			}
		})
	}
}

// TestSetMultihopReportsAndRemembersSelection: a valid enable echoes the selection
// in State; a following disable keeps the endpoints recorded (so the UI can
// re-enable the last pick) with enabled flipped off.
func TestSetMultihopReportsAndRemembersSelection(t *testing.T) {
	h := newHarness(t)
	p := twoNodeProfile(t, h)
	entry := p.Servers[0].ID
	exit := p.Servers[1].ID

	h.send(Request{ID: 1, Cmd: CmdSetMultihop, Enabled: true, Profile: p.ID, EntryID: entry, ExitID: exit})
	var on State
	h.dataInto(h.await(), &on)
	if on.Multihop == nil {
		t.Fatal("enabled response carried no multihop object")
	}
	if !on.Multihop.Enabled || on.Multihop.EntryID != entry || on.Multihop.ExitID != exit {
		t.Errorf("multihop = %+v, want enabled with entry=%s exit=%s", *on.Multihop, entry, exit)
	}

	h.send(Request{ID: 2, Cmd: CmdSetMultihop, Enabled: false, Profile: p.ID, EntryID: entry, ExitID: exit})
	var off State
	h.dataInto(h.await(), &off)
	if off.Multihop == nil {
		t.Fatal("disabled-but-remembered response cleared the multihop object")
	}
	if off.Multihop.Enabled {
		t.Error("multihop still reports enabled after disable")
	}
	if off.Multihop.EntryID != entry || off.Multihop.ExitID != exit {
		t.Error("disable dropped the remembered entry/exit selection")
	}
}

// TestSetMultihopConnectBuildsChain: with multihop armed, the next connect's config
// resolves the stored IDs into a real chain — the selector's exit outbound carries
// detour=<entry tag>.
func TestSetMultihopConnectBuildsChain(t *testing.T) {
	h := newHarness(t)
	p := twoNodeProfile(t, h)
	entry := p.Servers[0].ID
	exit := p.Servers[1].ID

	h.send(Request{ID: 1, Cmd: CmdSetMultihop, Enabled: true, Profile: p.ID, EntryID: entry, ExitID: exit})
	h.await()
	if h.runner.starts() != 0 {
		t.Fatalf("arming multihop while idle started a process (starts=%d)", h.runner.starts())
	}

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	exitTag, detour := selectedOutboundDetour(t, h.runner.startCfgs()[0])
	if exitTag != "Exit" {
		t.Errorf("selector default = %q, want the exit tag %q", exitTag, "Exit")
	}
	if detour != "Entry" {
		t.Errorf("exit outbound detour = %q, want the entry tag %q", detour, "Entry")
	}
}

// TestSetMultihopDefaultsOff: without arming, a connect's config is a plain single
// hop — the selected outbound carries no detour.
func TestSetMultihopDefaultsOff(t *testing.T) {
	h := newHarness(t)
	p := twoNodeProfile(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	if _, detour := selectedOutboundDetour(t, h.runner.startCfgs()[0]); detour != "" {
		t.Errorf("selected outbound carries detour %q with multihop off; it must be opt-in", detour)
	}
}

// TestSetMultihopLiveHotSwapsInChain: arming while connected restarts sing-box once
// and the new config carries the chain, matching the kill switch's live re-apply.
func TestSetMultihopLiveHotSwapsInChain(t *testing.T) {
	h := newHarness(t)
	p := twoNodeProfile(t, h)
	entry := p.Servers[0].ID
	exit := p.Servers[1].ID

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetMultihop, Enabled: true, Profile: p.ID, EntryID: entry, ExitID: exit})
	var st State
	h.dataInto(h.await(), &st)
	if st.Multihop == nil || !st.Multihop.Enabled {
		t.Fatal("response did not report multihop armed")
	}

	h.waitStarts(2)
	cfgs := h.runner.startCfgs()
	if _, before := selectedOutboundDetour(t, cfgs[0]); before != "" {
		t.Error("initial config already carried a detour")
	}
	if _, after := selectedOutboundDetour(t, cfgs[len(cfgs)-1]); after != "Entry" {
		t.Errorf("hot-swapped config detour = %q, want the entry tag %q", after, "Entry")
	}
}

// TestSetMultihopUnchangedDoesNotRestart: re-sending the current (off) value must
// not bounce a live tunnel.
func TestSetMultihopUnchangedDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	p := twoNodeProfile(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetMultihop, Enabled: false, Profile: p.ID}) // already off
	h.await()
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (a no-op toggle must not restart)", h.runner.starts())
	}
}

// TestSetMultihopPersistsAcrossRestart: the selection round-trips through the
// settings file and loads into a fresh daemon's state.
func TestSetMultihopPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	p := twoNodeProfile(t, h)
	entry := p.Servers[0].ID
	exit := p.Servers[1].ID
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetMultihop, Enabled: true, Profile: p.ID, EntryID: entry, ExitID: exit})
	h.await()

	h2 := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)

	loaded := h2.daemon.snapshotState().Multihop
	if loaded == nil || !loaded.Enabled || loaded.EntryID != entry || loaded.ExitID != exit {
		t.Errorf("multihop did not survive the restart: %+v", loaded)
	}
}
