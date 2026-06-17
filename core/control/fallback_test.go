package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

// These tests exercise the connect fallback loop with the fake runner: which
// candidate it lands on, how it walks past blocked protocols, exhaustion, that a
// success persists last-good and leads next time, and that a mid-loop disconnect
// cancels cleanly without leaking goroutines or emitting stale state.

// seedMultiProto adds a manual profile with one VLESS, one Hysteria2 and one
// AmneziaWG server (in that input order) to the harness store and returns it.
// DefaultOrder (VLESS, Hysteria2, AmneziaWG) therefore matches input order, so
// the walk visits vless -> hy2 -> wg unless last-good says otherwise.
func seedMultiProto(t *testing.T, h *harness) profile.Profile {
	t.Helper()
	servers := []profile.Server{
		srv("vless-id", model.VLESS, "Reality-FRA"),
		srv("hy2-id", model.Hysteria2, "Hysteria-AMS"),
		awg("wg-id", "Amnezia-NL"),
	}
	p := profile.Profile{
		ID:      "multi",
		Name:    "Multi",
		Source:  profile.SourceManual,
		Servers: servers,
	}
	if err := h.store.Add(p); err != nil {
		t.Fatalf("add multi-proto profile: %v", err)
	}
	return p
}

// connectedNodeTag returns the selector "default" tag from the config the runner
// was started with at index i, so a test can assert which node a given attempt
// selected.
func connectedNodeTag(t *testing.T, cfgJSON []byte) string {
	t.Helper()
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	for _, o := range cfg.Outbounds {
		if o["type"] == "selector" {
			def, _ := o["default"].(string)
			return def
		}
	}
	t.Fatal("no selector in config")
	return ""
}

// TestFallbackConnectsFirstCandidate: with the tunnel healthy, connect lands on
// the preferred protocol (VLESS) on the first try, probes once, and records it as
// last-good.
func TestFallbackConnectsFirstCandidate(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	r := h.await()
	var st State
	h.dataInto(r, &st)
	if st.State != StateConnecting {
		t.Fatalf("connect response = %q, want connecting", st.State)
	}

	h.awaitState(StateConnecting)
	connected := h.awaitState(StateConnected)
	if connected["node"] != "vless-id" {
		t.Errorf("connected node = %v, want vless-id", connected["node"])
	}
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (first candidate succeeds)", h.runner.starts())
	}
	if got := h.runner.probes(); got < 1 {
		t.Errorf("probes = %d, want >=1", got)
	}
	// The single config started must select the VLESS node's tag.
	if tag := connectedNodeTag(t, h.runner.startCfgs()[0]); tag != serverTags(p)["vless-id"] {
		t.Errorf("started config selected %q, want vless tag %q", tag, serverTags(p)["vless-id"])
	}
	if id, ok := h.daemon.lastGood.Get(p.ID); !ok || id != "vless-id" {
		t.Errorf("last-good = %q,%v, want vless-id,true", id, ok)
	}
}

// TestFallbackWalksPastBlocked: the first two candidates' probes fail (blocked),
// so the loop restarts on the next protocol and succeeds on the third. It must
// start three processes, stop the two failed ones, and land connected on the WG
// node, logging a "trying next" line for each skip.
func TestFallbackWalksPastBlocked(t *testing.T) {
	h := newHarness(t)
	h.runner.failStarts = 2 // first two candidates (vless, hy2) blocked, third up
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	connected := h.awaitState(StateConnected)
	if connected["node"] != "wg-id" {
		t.Errorf("connected node = %v, want wg-id (after two blocked)", connected["node"])
	}
	if h.runner.starts() != 3 {
		t.Errorf("starts = %d, want 3 (restart per candidate)", h.runner.starts())
	}
	if h.runner.stops() < 2 {
		t.Errorf("stops = %d, want >=2 (each failed candidate stopped)", h.runner.stops())
	}
	// The three configs must select vless, hy2, wg tags in that order.
	cfgs := h.runner.startCfgs()
	if len(cfgs) != 3 {
		t.Fatalf("got %d started configs, want 3", len(cfgs))
	}
	wantTags := []string{serverTags(p)["vless-id"], serverTags(p)["hy2-id"], serverTags(p)["wg-id"]}
	for i, want := range wantTags {
		if got := connectedNodeTag(t, cfgs[i]); got != want {
			t.Errorf("config %d selected %q, want %q", i, got, want)
		}
	}
	if id, _ := h.daemon.lastGood.Get(p.ID); id != "wg-id" {
		t.Errorf("last-good = %q, want wg-id", id)
	}
}

// TestFallbackExhaustionErrors: every probe fails, so the walk exhausts and the
// daemon reports an error state. It must have tried every renderable candidate.
func TestFallbackExhaustionErrors(t *testing.T) {
	h := newHarness(t)
	h.runner.probeErr = errors.New("blocked") // every probe fails
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	ev := h.awaitState(StateError)
	if ev["error"] == nil || ev["error"] == "" {
		t.Errorf("error state missing message: %+v", ev)
	}
	if h.runner.starts() != 3 {
		t.Errorf("starts = %d, want 3 (one per candidate before exhaustion)", h.runner.starts())
	}
	// Nothing should be recorded as last-good when everything failed.
	if id, ok := h.daemon.lastGood.Get(p.ID); ok {
		t.Errorf("last-good = %q set despite total failure", id)
	}
}

// TestFallbackProcessExitSkips: a candidate whose process exits before a probe
// succeeds is treated as failed and the loop moves on. The first candidate's
// first probe fails and its process then dies; the loop's retry path observes the
// exit and advances, and the second candidate succeeds. This exercises the
// early-exit branch distinct from a plain probe timeout.
func TestFallbackProcessExitSkips(t *testing.T) {
	h := newHarness(t)
	h.runner.failStarts = 1 // first candidate blocked; its probe also fails
	// When the first candidate's first probe fails, kill the process so the retry
	// select observes the exit (the early-exit branch) rather than just timing out.
	h.runner.onProbe = func(ctx context.Context, n int) {
		if n == 1 {
			h.runner.exit(errors.New("exit status 1"))
		}
	}
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	connected := h.awaitState(StateConnected)
	if connected["node"] != "hy2-id" {
		t.Errorf("connected node = %v, want hy2-id (first process died)", connected["node"])
	}
	if h.runner.starts() < 2 {
		t.Errorf("starts = %d, want >=2", h.runner.starts())
	}
}

// TestFallbackSuccessLeadsNextConnect: after the loop succeeds on a non-preferred
// node (because the preferred is blocked), a second connect must try that
// last-good node first — proving Success persisted last-good and the machine
// honours it on the next walk.
func TestFallbackSuccessLeadsNextConnect(t *testing.T) {
	h := newHarness(t)
	h.runner.failStarts = 1 // first connect: VLESS blocked, Hysteria2 succeeds
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	first := h.awaitState(StateConnected)
	if first["node"] != "hy2-id" {
		t.Fatalf("first connect node = %v, want hy2-id", first["node"])
	}

	h.send(Request{ID: 2, Cmd: CmdDisconnect})
	h.await()
	h.awaitState(StateIdle)

	// Second connect: tunnel healthy now. Last-good (hy2) must lead, so the very
	// first config started selects the Hysteria2 tag, not VLESS.
	h.runner.setFailStarts(0)
	startsBefore := h.runner.starts()
	h.send(Request{ID: 3, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	second := h.awaitState(StateConnected)
	if second["node"] != "hy2-id" {
		t.Errorf("second connect node = %v, want hy2-id (last-good leads)", second["node"])
	}
	cfgs := h.runner.startCfgs()
	firstSecondCfg := cfgs[startsBefore] // first config of the second connect
	if got := connectedNodeTag(t, firstSecondCfg); got != serverTags(p)["hy2-id"] {
		t.Errorf("second connect first config selected %q, want hy2 tag %q", got, serverTags(p)["hy2-id"])
	}
}

// TestDisconnectMidLoopCancels: a disconnect while the loop is still probing must
// cancel it cleanly — the runner is stopped, the state returns to idle, and no
// connected/error state leaks out afterwards. The probe hook blocks until the
// test releases it, holding the loop in-flight across the disconnect.
func TestDisconnectMidLoopCancels(t *testing.T) {
	h := newHarness(t)

	release := make(chan struct{})
	probing := make(chan struct{}, 1)
	h.runner.onProbe = func(ctx context.Context, n int) {
		select {
		case probing <- struct{}{}:
		default:
		}
		// Block until released or the loop's ctx is cancelled by disconnect.
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnecting)

	// Wait until the loop is actually inside a probe, then disconnect.
	select {
	case <-probing:
	case <-time.After(3 * time.Second):
		t.Fatal("probe never started")
	}

	h.send(Request{ID: 2, Cmd: CmdDisconnect})
	dr := h.await()
	var st State
	h.dataInto(dr, &st)
	if st.State != StateIdle {
		t.Errorf("disconnect state = %q, want idle", st.State)
	}
	close(release) // let any parked probe unwind

	h.awaitState(StateIdle)

	// After idle, no further connected/error state may arrive. Drain briefly and
	// assert the daemon's state stays idle.
	time.Sleep(50 * time.Millisecond)
	if got := h.daemon.snapshotState().State; got != StateIdle {
		t.Errorf("state after cancelled loop = %q, want idle", got)
	}
	if h.runner.stops() < 1 {
		t.Error("runner not stopped on mid-loop disconnect")
	}
}
