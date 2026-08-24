package control

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

// errSelectRefused stands in for a clash API that will not take a selection (an
// older sing-box, a controller that is not answering).
var errSelectRefused = errors.New("fake: selector refused")

// These tests cover changing the exit on a tunnel that is already up: that it
// steers the running sing-box instead of rebuilding one, that it falls back to
// the ordinary reconnect whenever it cannot (or when the new exit turns out to
// carry nothing), and the hysteresis that keeps the automatic version from
// walking the user around the node list.

// tagFor returns the outbound tag the running config gave a profile server, read
// from the daemon's own record of what it started — the same source the switch
// path uses, so an assertion cannot pass by re-deriving the tag differently.
func tagFor(t *testing.T, d *Daemon, nodeID string) string {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.live == nil {
		t.Fatal("no live config recorded for the running tunnel")
	}
	tag := d.live.tagOf[nodeID]
	if tag == "" {
		t.Fatalf("no outbound tag recorded for node %q", nodeID)
	}
	return tag
}

// waitSwitchedTo blocks until the daemon reports being connected on nodeID.
func waitSwitchedTo(t *testing.T, d *Daemon, nodeID string) {
	t.Helper()
	waitFor(t, func() bool {
		st := d.snapshotState()
		return st.State == StateConnected && st.Node == nodeID
	}, "the tunnel to report the new exit")
}

// TestConnectToAnotherNodeSteersTheLiveTunnel: with a tunnel up, asking for a
// different node moves the exit through the running process — no second sing-box
// start, no dip through connecting, and the reply already says connected.
func TestConnectToAnotherNodeSteersTheLiveTunnel(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	if c := h.awaitState(StateConnected); c["node"] != "vless-id" {
		t.Fatalf("initial connect landed on %v, want vless-id", c["node"])
	}
	startsBefore := h.runner.starts()
	stopsBefore := h.runner.stops()
	wantTag := tagFor(t, h.daemon, "hy2-id")

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID, Node: "hy2-id"})
	r := h.await()

	var st State
	h.dataInto(r, &st)
	if st.State != StateConnected {
		t.Errorf("switch reply state = %q, want connected (nothing was reconnected)", st.State)
	}
	if st.Node != "hy2-id" {
		t.Errorf("switch reply node = %q, want hy2-id", st.Node)
	}
	if got := h.runner.starts(); got != startsBefore {
		t.Errorf("runner starts = %d, want %d — the tunnel was rebuilt instead of steered", got, startsBefore)
	}
	if got := h.runner.stops(); got != stopsBefore {
		t.Errorf("runner stops = %d, want %d — the tunnel was torn down", got, stopsBefore)
	}

	// The selector, not the process, is what moved.
	calls := h.runner.selectCalls()
	last := calls[len(calls)-1]
	if last.group != proxySelectorTag || last.tag != wantTag {
		t.Errorf("last select = %+v, want group %q tag %q", last, proxySelectorTag, wantTag)
	}
	waitSwitchedTo(t, h.daemon, "hy2-id")
	if id, _ := h.daemon.lastGood.Get(p.ID); id != "hy2-id" {
		t.Errorf("last-good = %q, want hy2-id after the switch", id)
	}
}

// TestSwitchFallsBackToReconnectWhenTheSelectorRefuses: a runner that cannot
// steer (an old sing-box, a clash API that is not answering) must not leave the
// user stuck on the old exit — the node change degrades to the reconnect that
// every node change used to be.
func TestSwitchFallsBackToReconnectWhenTheSelectorRefuses(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.runner.mu.Lock()
	h.runner.selectErr = errSelectRefused
	h.runner.mu.Unlock()

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID, Node: "hy2-id"})
	r := h.await()

	var st State
	h.dataInto(r, &st)
	if st.State != StateConnecting {
		t.Errorf("reply state = %q, want connecting — the switch failed, so a reconnect is honest", st.State)
	}
	h.awaitLogContaining("could not steer the live tunnel")
	h.waitStarts(2)
	if c := h.awaitState(StateConnected); c["node"] != "hy2-id" {
		t.Errorf("reconnect landed on %v, want hy2-id", c["node"])
	}
}

// TestSwitchRevertsAndReconnectsWhenTheNewExitCarriesNothing: pointing the
// selector at a node always "succeeds" — the API answers for any tag that exists,
// including one whose server died an hour ago. So the switch is confirmed by a
// probe, and a confirmation that fails puts the working exit back before handing
// the caller a reconnect, rather than leaving traffic in a hole.
func TestSwitchRevertsAndReconnectsWhenTheNewExitCarriesNothing(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	fromTag := tagFor(t, h.daemon, "vless-id")
	toTag := tagFor(t, h.daemon, "hy2-id")

	// Nothing carries traffic from here on: the confirmation after the switch fails.
	h.runner.mu.Lock()
	h.runner.probeErr = errSelectRefused
	h.runner.mu.Unlock()

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID, Node: "hy2-id"})
	r := h.await()

	var st State
	h.dataInto(r, &st)
	if st.State != StateConnecting {
		t.Errorf("reply state = %q, want connecting — an unconfirmed switch falls back", st.State)
	}

	// The selector was moved and then moved back, in that order, before the
	// reconnect was allowed to start.
	var moved, reverted bool
	for _, c := range h.runner.selectCalls() {
		if c.tag == toTag {
			moved = true
			continue
		}
		if moved && c.tag == fromTag {
			reverted = true
		}
	}
	if !moved {
		t.Error("the selector was never pointed at the requested node")
	}
	if !reverted {
		t.Errorf("the selector was left on %q; the exit that was working must be put back", toTag)
	}
	h.waitStarts(2)
}

// TestLiveSwitchTargetRefusesWhatTheRunningConfigCannotReach: the decision is made
// against the process that is running, not against the stored profile. A node the
// running config never rendered — added by a refresh since the connect, or left
// out of the selector the way multihop leaves everything but the exit out — is not
// steerable, and saying so is what routes the request to a reconnect.
func TestLiveSwitchTargetRefusesWhatTheRunningConfigCannotReach(t *testing.T) {
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	d := NewDaemon(store, newFakeRunner())
	d.generation = 7
	d.state = State{State: StateConnected, Profile: "prof", Node: "a"}
	d.live = &liveConfig{
		gen:       7,
		profileID: "prof",
		tagOf:     map[string]string{"a": "alpha", "b": "beta", "c": "gamma"},
		sel:       selectorShape{Default: "alpha", Members: []string{"alpha", "beta"}},
	}

	if tag, _, _, ok := d.liveSwitchTarget("prof", "b"); !ok || tag != "beta" {
		t.Errorf("switching to a selector member = (%q,%v), want (beta,true)", tag, ok)
	}
	if _, _, _, ok := d.liveSwitchTarget("prof", "c"); ok {
		t.Error("a node outside the selector must not be steerable")
	}
	if _, _, _, ok := d.liveSwitchTarget("prof", "unknown"); ok {
		t.Error("a node with no outbound in the running config must not be steerable")
	}
	if _, _, _, ok := d.liveSwitchTarget("other", "b"); ok {
		t.Error("a different profile must not be steerable through this config")
	}
	if _, _, _, ok := d.liveSwitchTarget("prof", "a"); ok {
		t.Error("switching to the node already running is not a switch")
	}

	// A reconnect already in flight bumped the generation past the running config.
	d.generation = 8
	if _, _, _, ok := d.liveSwitchTarget("prof", "b"); ok {
		t.Error("a superseded config must not be steerable")
	}
}

// switchDaemon builds a bare daemon that believes it is connected on nodeID with a
// steerable three-node selector, for the hysteresis tests.
func switchDaemon(t *testing.T, nodeID string) *Daemon {
	t.Helper()
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	d := NewDaemon(store, newFakeRunner())
	d.generation = 1
	d.state = State{State: StateConnected, Profile: "prof", Node: nodeID}
	d.live = &liveConfig{
		gen:       1,
		profileID: "prof",
		tagOf:     map[string]string{"a": "alpha", "b": "beta", "c": "gamma"},
		sel:       selectorShape{Default: "alpha", Members: []string{"alpha", "beta", "gamma"}},
	}
	return d
}

// TestAutoSwitchHysteresis walks the gate the automatic switch runs through: it
// allows the first move, refuses a second one inside the cooldown, refuses further
// ones once the window budget is spent, and recovers that budget once the window
// has slid past.
func TestAutoSwitchHysteresis(t *testing.T) {
	d := switchDaemon(t, "a")
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }

	if !d.allowAutoSwitch(1, "prof", "a") {
		t.Fatal("the first automatic switch must be allowed")
	}
	d.recordAutoSwitch()

	// Straight after a move, nothing else may move: one switch has to be given the
	// chance to prove itself, or a broken uplink (where every exit fails) churns
	// the exit every time the watchdog reaches its threshold.
	now = now.Add(d.autoSwitchCooldown - time.Second)
	if d.allowAutoSwitch(1, "prof", "b") {
		t.Error("a second switch inside the cooldown must be refused")
	}

	// Past the cooldown it is allowed again, up to the per-window cap.
	now = now.Add(2 * time.Second)
	for i := 2; i <= d.maxAutoSwitches; i++ {
		if !d.allowAutoSwitch(1, "prof", "b") {
			t.Fatalf("switch %d of %d must be allowed", i, d.maxAutoSwitches)
		}
		d.recordAutoSwitch()
		now = now.Add(d.autoSwitchCooldown + time.Second)
	}
	if d.allowAutoSwitch(1, "prof", "c") {
		t.Errorf("switch %d must be refused: the window budget is spent", d.maxAutoSwitches+1)
	}

	// The window slides, so a session that has been quiet gets its budget back
	// without anything having to reset it.
	now = now.Add(d.autoSwitchWindow)
	if !d.allowAutoSwitch(1, "prof", "c") {
		t.Error("the budget must recover once the window has passed")
	}
}

// TestAutoSwitchNeedsASteerableTunnel: the gate refuses outright when there is no
// running config to steer, so the caller falls back to a reconnect instead of
// spending its budget on a switch that cannot happen.
func TestAutoSwitchNeedsASteerableTunnel(t *testing.T) {
	d := switchDaemon(t, "a")
	d.live = nil
	if d.allowAutoSwitch(1, "prof", "a") {
		t.Error("nothing to steer must refuse the switch")
	}

	d = switchDaemon(t, "a")
	if d.allowAutoSwitch(2, "prof", "a") {
		t.Error("a stale generation must refuse the switch")
	}
}

// TestScanSkipsRecentlyDegradedNodes: a node that just ran out of health probes is
// passed over for degradedRetryAfter. Without that, two flapping exits hand the
// user back and forth — one degrades, the scan picks the other, that degrades, and
// the scan picks the first one straight back.
func TestScanSkipsRecentlyDegradedNodes(t *testing.T) {
	d := switchDaemon(t, "b")
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }

	p := profile.Profile{ID: "prof", Name: "P", Source: profile.SourceManual, Servers: []profile.Server{
		srv("a", model.VLESS, "Alpha"),
		srv("b", model.VLESS, "Beta"),
		srv("c", model.VLESS, "Gamma"),
	}}

	// "a" degraded a minute ago; "b" is the exit being left now.
	d.degradedAt = map[string]time.Time{"a": now.Add(-time.Minute)}
	cutoff := now.Add(-d.degradedRetryAfter)

	got := d.scanCandidates(d.live, p, "b", cutoff)
	if len(got) != 1 || got[0].nodeID != "c" {
		t.Fatalf("candidates = %+v, want only c (a degraded recently, b is the one being left)", got)
	}

	// Once the memory has expired, the node is a candidate again — a node that was
	// bad ten minutes ago is not bad forever.
	cutoff = now.Add(-time.Second)
	got = d.scanCandidates(d.live, p, "b", cutoff)
	if len(got) != 2 {
		t.Fatalf("candidates = %+v, want both a and c once the memory expired", got)
	}
}

// TestSwitchScanIsCappedAndLeadsWithLastGood: a degradation must not turn into a
// minute of probing on a large subscription, and the exit that was working earlier
// in the session is the one worth measuring first.
func TestSwitchScanIsCappedAndLeadsWithLastGood(t *testing.T) {
	d := switchDaemon(t, "a")
	d.switchScanLimit = 2
	d.lastGood.Set("prof", "c")

	p := profile.Profile{ID: "prof", Name: "P", Source: profile.SourceManual, Servers: []profile.Server{
		srv("a", model.VLESS, "Alpha"),
		srv("b", model.VLESS, "Beta"),
		srv("c", model.VLESS, "Gamma"),
	}}

	got := d.scanCandidates(d.live, p, "a", d.now().Add(-d.degradedRetryAfter))
	if len(got) != 2 {
		t.Fatalf("candidates = %d, want the scan capped at 2", len(got))
	}
	if got[0].nodeID != "c" {
		t.Errorf("first candidate = %q, want the last-good c measured first", got[0].nodeID)
	}
}

// TestConnectPinsTheSelectorToTheNodeItStarted: sing-box restores a selector from
// its cache file before applying the config's default, so once anything has moved
// the selector a fresh process can come up on a node the config does not name.
// Every start therefore pins the selector to its own default before it believes
// the probe.
func TestConnectPinsTheSelectorToTheNodeItStarted(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	want := connectedNodeTag(t, h.runner.startCfgs()[0])
	calls := h.runner.selectCalls()
	if len(calls) == 0 {
		t.Fatal("the connect never pinned the selector")
	}
	if calls[0].group != proxySelectorTag || calls[0].tag != want {
		t.Errorf("first select = %+v, want group %q pinned to the config default %q",
			calls[0], proxySelectorTag, want)
	}
}

// TestBuiltSelectorKeepsExistingConnections: the selector must carry
// interrupt_exist_connections=false, and carry it explicitly. It is what makes a
// live exit change seamless — the download or call already in flight finishes on
// the exit it was dialled through, and only new connections take the new node —
// and leaving it to sing-box's default would put the guarantee outside this repo.
func TestBuiltSelectorKeepsExistingConnections(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(h.runner.startCfgs()[0], &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	for _, o := range cfg.Outbounds {
		if o["type"] != "selector" {
			continue
		}
		v, present := o["interrupt_exist_connections"]
		if !present {
			t.Fatal("the selector does not state interrupt_exist_connections")
		}
		if v != false {
			t.Errorf("interrupt_exist_connections = %v, want false", v)
		}
		return
	}
	t.Fatal("no selector in the generated config")
}
