package control

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
)

// These tests exercise the connected-tunnel health watchdog: it fails over to
// another node after a run of failed probes, stays put on a healthy node, yields
// to a racing user command, stops on disconnect, and honours the toggle. The probe
// is injected (like d.classify), so the walk is driven without a live network.

// scriptedProbe is an injectable healthProbe whose per-call verdict a test
// controls. verdict receives the 1-based call number and returns the error (or
// nil) that call should report; calls are counted so a test can wait for the
// watchdog to have ticked.
type scriptedProbe struct {
	mu      sync.Mutex
	calls   int
	verdict func(call int) error
}

func (s *scriptedProbe) fn(context.Context) error {
	s.mu.Lock()
	s.calls++
	n := s.calls
	v := s.verdict
	s.mu.Unlock()
	return v(n)
}

func (s *scriptedProbe) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// tuneHealth installs fast, deterministic watchdog timings and the injected probe.
// It must be called before the connect that starts the lifecycle, so the watchdog
// goroutine captures these values rather than the production defaults.
func (h *harness) tuneHealth(interval, probeTimeout time.Duration, threshold int, probe func(context.Context) error) {
	h.daemon.healthInterval = interval
	h.daemon.healthProbeTimeout = probeTimeout
	h.daemon.healthFailThreshold = threshold
	h.daemon.healthProbe = probe
}

// TestHealthWatchFailsOverAfterThreshold: with the active node failing its health
// probe threshold times in a row, the watchdog reconnects — on its own — to a
// different node, emitting a health_reconnecting state on the way and excluding the
// degraded node from the walk.
func TestHealthWatchFailsOverAfterThreshold(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	// Fail the first three probes (the vless node's watchdog), then report healthy —
	// so the node the failover lands on is not itself failed straight back over.
	probe := &scriptedProbe{verdict: func(call int) error {
		if call <= 3 {
			return errors.New("probe: node down")
		}
		return nil
	}}
	h.tuneHealth(5*time.Millisecond, 100*time.Millisecond, 3, probe.fn)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	if c := h.awaitState(StateConnected); c["node"] != "vless-id" {
		t.Fatalf("initial connect landed on %v, want vless-id", c["node"])
	}

	// The watchdog trips and announces the health-driven switch, naming the node it
	// is leaving, before the reconnect walks to another exit.
	if hr := h.awaitState(StateHealthReconnecting); hr["node"] != "vless-id" {
		t.Errorf("health_reconnecting named %v, want the degraded vless-id", hr["node"])
	}
	c := h.awaitState(StateConnected)
	if c["node"] != "hy2-id" {
		t.Fatalf("failover landed on %v, want hy2-id (vless excluded, next by preference)", c["node"])
	}
	if got := h.runner.starts(); got != 2 {
		t.Errorf("starts = %d, want 2 (initial + one failover)", got)
	}
	if id, _ := h.daemon.lastGood.Get(p.ID); id != "hy2-id" {
		t.Errorf("last-good = %q, want hy2-id after failover", id)
	}

	// The node it moved to probes healthy, so it must not churn onward.
	time.Sleep(40 * time.Millisecond)
	if got := h.runner.starts(); got != 2 {
		t.Errorf("starts = %d after settling, want a stable 2 (no churn on a healthy node)", got)
	}
	if st := h.daemon.snapshotState(); st.State != StateConnected || st.Node != "hy2-id" {
		t.Errorf("state = %q on %q, want connected on hy2-id", st.State, st.Node)
	}
}

// TestHealthWatchIgnoresHealthyNode: a node that keeps passing its probe is never
// reconnected, no matter how many times the watchdog ticks.
func TestHealthWatchIgnoresHealthyNode(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	probe := &scriptedProbe{verdict: func(int) error { return nil }} // always healthy
	h.tuneHealth(5*time.Millisecond, 100*time.Millisecond, 3, probe.fn)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	if c := h.awaitState(StateConnected); c["node"] != "vless-id" {
		t.Fatalf("connected on %v, want vless-id", c["node"])
	}

	// Let the watchdog probe several times over.
	waitFor(t, func() bool { return probe.count() >= 5 }, "the watchdog to probe a healthy node repeatedly")

	if got := h.runner.starts(); got != 1 {
		t.Errorf("starts = %d, want 1 (a healthy node is never failed over)", got)
	}
	if st := h.daemon.snapshotState(); st.State != StateConnected || st.Node != "vless-id" {
		t.Errorf("state = %q on %q, want connected on vless-id", st.State, st.Node)
	}
}

// TestHealthWatchStopsOnDisconnect: an explicit disconnect drains the watchdog —
// no probe fires once the tunnel is down.
func TestHealthWatchStopsOnDisconnect(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	probe := &scriptedProbe{verdict: func(int) error { return nil }} // healthy: never fails over
	h.tuneHealth(5*time.Millisecond, 100*time.Millisecond, 3, probe.fn)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	waitFor(t, func() bool { return probe.count() >= 3 }, "the watchdog to start probing")

	h.send(Request{ID: 2, Cmd: CmdDisconnect})
	h.await() // teardown drains the watchdog before the response returns
	h.awaitState(StateIdle)

	// After the disconnect response the watchdog goroutine has been waited out, so
	// the probe count is frozen: no tick may fire against a torn-down tunnel.
	settled := probe.count()
	time.Sleep(40 * time.Millisecond)
	if got := probe.count(); got != settled {
		t.Errorf("watchdog kept probing after disconnect: count %d -> %d", settled, got)
	}
}

// TestHealthWatchYieldsToUserCommand: a user disconnect that lands while the
// failover reconnect is on its way to connMu wins; the parked failover observes
// the moved generation and yields without starting a tunnel or forcing a state.
func TestHealthWatchYieldsToUserCommand(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	probe := &scriptedProbe{verdict: func(int) error { return errors.New("probe: node down") }}
	h.tuneHealth(5*time.Millisecond, 100*time.Millisecond, 3, probe.fn)

	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.daemon.beforeReconnect = func() {
		once.Do(func() { close(parked) })
		<-release
	}

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	// The watchdog trips and the failover reconnect parks before claiming connMu.
	<-parked
	starts := h.runner.starts()

	// The user disconnects while the failover is parked.
	h.send(Request{ID: 2, Cmd: CmdDisconnect})
	h.await()
	h.awaitState(StateIdle)

	// Release the failover: it must see the bumped generation and yield.
	close(release)
	time.Sleep(50 * time.Millisecond) // give the goroutine a chance to (wrongly) act
	if got := h.runner.starts(); got != starts {
		t.Errorf("failover started a tunnel over the user's disconnect (starts %d -> %d)", starts, got)
	}
	if st := h.daemon.snapshotState(); st.State != StateIdle {
		t.Errorf("state = %q, want idle — the user's disconnect must stand", st.State)
	}
}

// TestHealthWatchDisabledDoesNotProbe: with the toggle off the watchdog neither
// probes nor fails over, even against a node that would fail every probe.
func TestHealthWatchDisabledDoesNotProbe(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	probe := &scriptedProbe{verdict: func(int) error { return errors.New("probe: node down") }}
	h.tuneHealth(5*time.Millisecond, 100*time.Millisecond, 3, probe.fn)

	// Disarm the watchdog before connecting.
	h.send(Request{ID: 1, Cmd: CmdSetAutoFailover, On: false})
	h.await()

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	if c := h.awaitState(StateConnected); c["node"] != "vless-id" {
		t.Fatalf("connected on %v, want vless-id", c["node"])
	}

	// Ten intervals with the toggle off: no probe is ever run and nothing fails over.
	time.Sleep(50 * time.Millisecond)
	if got := probe.count(); got != 0 {
		t.Errorf("disabled watchdog probed %d times, want 0", got)
	}
	if got := h.runner.starts(); got != 1 {
		t.Errorf("starts = %d, want 1 (no failover while disabled)", got)
	}
	if st := h.daemon.snapshotState(); st.State != StateConnected || st.Node != "vless-id" {
		t.Errorf("state = %q on %q, want connected on vless-id", st.State, st.Node)
	}
}

// TestHealthWatchNoAlternativeStaysConnected: a single-node profile has nowhere to
// fail over to, so a degraded node is logged and left running rather than churned.
func TestHealthWatchNoAlternativeStaysConnected(t *testing.T) {
	h := newHarness(t)
	p := h.addProfile([]model.Node{vlessNode("Solo", "solo.example.11")})

	probe := &scriptedProbe{verdict: func(int) error { return errors.New("probe: node down") }}
	h.tuneHealth(5*time.Millisecond, 100*time.Millisecond, 3, probe.fn)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.awaitLogContains("no other node to fail over to")

	// The tunnel stays up on its one node — no reconnect, no error state.
	time.Sleep(40 * time.Millisecond)
	if got := h.runner.starts(); got != 1 {
		t.Errorf("starts = %d, want 1 (single-node profile can't fail over)", got)
	}
	if st := h.daemon.snapshotState(); st.State != StateConnected {
		t.Errorf("state = %q, want connected (a degraded single node is kept, not dropped)", st.State)
	}
}

// TestSetAutoFailoverReportsAndPersists: the toggle defaults on, round-trips
// through the response and status, and survives a daemon restart in both
// directions — mirroring the autoconnect toggle.
func TestSetAutoFailoverReportsAndPersists(t *testing.T) {
	dir := t.TempDir()

	// A fresh daemon reports the watchdog on before any settings are loaded.
	h := newHarness(t)
	if !h.daemon.snapshotState().AutoFailover {
		t.Fatal("auto_failover should default on")
	}
	h.daemon.SetSettings(settingsAt(t, dir))
	if !h.daemon.snapshotState().AutoFailover {
		t.Fatal("auto_failover should still be on after loading empty settings")
	}

	// Disarming round-trips and persists.
	h.send(Request{ID: 1, Cmd: CmdSetAutoFailover, On: false})
	var off State
	h.dataInto(h.await(), &off)
	if off.AutoFailover {
		t.Fatal("set_auto_failover off still reports on")
	}
	h.send(Request{ID: 2, Cmd: CmdStatus})
	var st State
	h.dataInto(h.await(), &st)
	if st.AutoFailover {
		t.Fatal("status reports auto_failover on after disarming")
	}
	h2 := newHarness(t)
	h2.daemon.SetSettings(settingsAt(t, dir))
	if h2.daemon.snapshotState().AutoFailover {
		t.Error("disarmed auto_failover did not survive the restart")
	}

	// Re-arming persists too.
	h.send(Request{ID: 3, Cmd: CmdSetAutoFailover, On: true})
	h.await()
	h3 := newHarness(t)
	h3.daemon.SetSettings(settingsAt(t, dir))
	if !h3.daemon.snapshotState().AutoFailover {
		t.Error("re-armed auto_failover did not survive the restart")
	}
}

// TestDefaultHealthProbeUsesRunner: the production probe reports the runner's
// clash-API delay verdict — an error when the outbound is not usable, nil once it
// is.
func TestDefaultHealthProbeUsesRunner(t *testing.T) {
	h := newHarness(t)

	// Before any Start the fake runner treats the outbound as unusable.
	if err := h.daemon.defaultHealthProbe(context.Background()); err == nil {
		t.Error("default health probe should fail before the outbound is up")
	}
	// After a Start with no forced failures, the delay test passes.
	if err := h.runner.Start(context.Background(), []byte("{}")); err != nil {
		t.Fatalf("fake start: %v", err)
	}
	if err := h.daemon.defaultHealthProbe(context.Background()); err != nil {
		t.Errorf("default health probe should pass through a healthy outbound, got %v", err)
	}
}
