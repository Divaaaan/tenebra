package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/singbox"
)

// These tests cover the kill switch and the tun-stack option end to end at the
// protocol level: recording and reporting the choices, arming strict_route in
// the built config, hot-swapping a live tunnel in place (same node, no fallback
// walk), relaunching a tunnel whose process died while the switch was armed,
// and persisting both preferences across a daemon restart.

// tunFromConfig extracts strict_route and the stack from the (single) tun
// inbound of a built config.
func tunFromConfig(t *testing.T, cfgJSON []byte) (strictRoute bool, stack string) {
	t.Helper()
	var cfg struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	for _, in := range cfg.Inbounds {
		if in["type"] == "tun" {
			sr, _ := in["strict_route"].(bool)
			st, _ := in["stack"].(string)
			return sr, st
		}
	}
	t.Fatal("no tun inbound in config")
	return false, ""
}

// awaitLogContains drains log events until one carries the substring.
func (h *harness) awaitLogContains(sub string) {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev["event"] != EventLog {
				continue
			}
			if msg, _ := ev["msg"].(string); strings.Contains(msg, sub) {
				return
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for log containing %q", sub)
		}
	}
}

// TestSetKillSwitchIdleAppliesOnNextConnect: arming while idle is recorded,
// reported, and lands as strict_route in the next connect's config.
func TestSetKillSwitchIdleAppliesOnNextConnect(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.KillSwitch {
		t.Fatalf("set_kill_switch response kill_switch = false, want true")
	}
	if h.runner.starts() != 0 {
		t.Fatalf("arming while idle started a process (starts=%d)", h.runner.starts())
	}

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	strict, _ := tunFromConfig(t, h.runner.startCfgs()[0])
	if !strict {
		t.Error("config built with kill switch armed lacks strict_route")
	}
}

// TestKillSwitchDefaultsOff: without arming, the built config must not carry
// strict_route — the rough-connect trade-off is strictly opt-in.
func TestKillSwitchDefaultsOff(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	strict, stack := tunFromConfig(t, h.runner.startCfgs()[0])
	if strict {
		t.Error("strict_route on by default; the kill switch must be opt-in")
	}
	if stack != "system" {
		t.Errorf("default stack = %q, want system", stack)
	}
}

// TestSetKillSwitchLiveHotSwapsSameNode: arming while connected restarts
// sing-box once, pinned to the node already in use, and comes back connected
// with strict_route in the new config.
func TestSetKillSwitchLiveHotSwapsSameNode(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)
	node := connected["node"].(string)

	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.KillSwitch {
		t.Fatal("response kill_switch = false, want true")
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
	if strict, _ := tunFromConfig(t, cfgs[0]); strict {
		t.Error("initial config already had strict_route")
	}
	if strict, _ := tunFromConfig(t, cfgs[1]); !strict {
		t.Error("hot-swapped config lacks strict_route")
	}
	if tagBefore, tagAfter := connectedNodeTag(t, cfgs[0]), connectedNodeTag(t, cfgs[1]); tagBefore != tagAfter {
		t.Errorf("hot swap moved the selector: %q -> %q; it must pin the current node", tagBefore, tagAfter)
	}
}

// TestSetKillSwitchUnchangedDoesNotRestart: re-sending the current value must
// not bounce a live tunnel.
func TestSetKillSwitchUnchangedDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: false}) // already off
	h.await()
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (no-op toggle must not restart)", h.runner.starts())
	}
}

// TestSetTunRejectsUnknownStack: the stack is validated before anything is
// recorded, so junk can never reach sing-box.
func TestSetTunRejectsUnknownStack(t *testing.T) {
	h := newHarness(t)

	h.send(Request{ID: 1, Cmd: CmdSetTun, Stack: "warp-drive"})
	r := h.await()
	if r.Ok {
		t.Fatal("set_tun accepted an unknown stack")
	}
	if !strings.Contains(r.Error, "unknown stack") {
		t.Errorf("error = %q, want it to name the unknown stack", r.Error)
	}
}

// TestSetTunIdleAppliesOnNextConnect: a stack chosen while idle is reported in
// the state and used by the next connect's config.
func TestSetTunIdleAppliesOnNextConnect(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetTun, Stack: singbox.StackGvisor})
	var st State
	h.dataInto(h.await(), &st)
	if st.TunStack != "gvisor" {
		t.Fatalf("tun_stack = %q, want gvisor", st.TunStack)
	}

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	if _, stack := tunFromConfig(t, h.runner.startCfgs()[0]); stack != "gvisor" {
		t.Errorf("config stack = %q, want gvisor", stack)
	}
}

// TestSetTunLiveHotSwapsSameNode: switching the stack under a live tunnel
// restarts once on the same node with the new stack.
func TestSetTunLiveHotSwapsSameNode(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetTun, Stack: singbox.StackMixed})
	h.await()
	re := h.awaitState(StateConnected)
	if re["node"] != connected["node"] {
		t.Errorf("reconnected node = %v, want %v", re["node"], connected["node"])
	}

	cfgs := h.runner.startCfgs()
	if len(cfgs) != 2 {
		t.Fatalf("starts = %d, want 2", len(cfgs))
	}
	if _, stack := tunFromConfig(t, cfgs[1]); stack != "mixed" {
		t.Errorf("hot-swapped config stack = %q, want mixed", stack)
	}
	if connectedNodeTag(t, cfgs[0]) != connectedNodeTag(t, cfgs[1]) {
		t.Error("hot swap moved the selector; it must pin the current node")
	}
}

// TestSetTunSameStackDoesNotRestart: choosing the stack already in effect is a
// no-op for a live tunnel.
func TestSetTunSameStackDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetTun, Stack: singbox.StackSystem})
	h.await()
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (same stack must not restart)", h.runner.starts())
	}
}

// TestKillSwitchRelaunchesDeadTunnel: with the switch armed, an unexpected
// process exit is answered with a relaunch pinned to the same node, and the
// state comes back connected rather than error.
func TestKillSwitchRelaunchesDeadTunnel(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)

	h.runner.exit(errors.New("boom"))
	h.awaitLogContains("kill switch: tunnel process died")
	re := h.awaitState(StateConnected)
	if re["node"] != connected["node"] {
		t.Errorf("relaunched node = %v, want %v", re["node"], connected["node"])
	}

	cfgs := h.runner.startCfgs()
	if len(cfgs) != 2 {
		t.Fatalf("starts = %d, want 2 (connect + relaunch)", len(cfgs))
	}
	if strict, _ := tunFromConfig(t, cfgs[1]); !strict {
		t.Error("relaunched config lacks strict_route")
	}
	if connectedNodeTag(t, cfgs[0]) != connectedNodeTag(t, cfgs[1]) {
		t.Error("relaunch moved the selector; it must pin the node that was up")
	}
}

// TestNoRelaunchWhenDisarmed: without the kill switch, a process death remains
// a plain error state — the pre-kill-switch behaviour.
func TestNoRelaunchWhenDisarmed(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.runner.exit(errors.New("boom"))
	errState := h.awaitState(StateError)
	if errState["error"] != "boom" {
		t.Errorf("error = %v, want boom", errState["error"])
	}
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (no relaunch when disarmed)", h.runner.starts())
	}
}

// TestKillSwitchRelaunchBudget: a tunnel that keeps dying gets maxRelaunches
// restarts and then an error state; an explicit reconnect opens a fresh budget.
func TestKillSwitchRelaunchBudget(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	for i := 0; i < maxRelaunches; i++ {
		h.runner.exit(errors.New("boom"))
		h.awaitState(StateConnected) // each death within budget is answered
	}
	// Back-to-back deaths never clear the reset window, so the budget still runs
	// out and the daemon gives up (see the honest wording in killSwitchRelaunch).
	h.runner.exit(errors.New("boom"))
	h.awaitLogContains("giving up on automatic restarts")
	h.awaitState(StateError)
	if got, want := h.runner.starts(), 1+maxRelaunches; got != want {
		t.Errorf("starts = %d, want %d (initial + budgeted relaunches)", got, want)
	}

	// A user reconnect resets the budget: the next death relaunches again.
	h.send(Request{ID: 3, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	h.runner.exit(errors.New("boom"))
	h.awaitState(StateConnected)
}

// TestReapplyDefersWhenNodeVanished: if the connected node is gone from the
// store by the time a setting changes, the live tunnel is left running on the
// old options and the change is logged as deferred — never a torn-down tunnel
// with nothing to replace it.
func TestReapplyDefersWhenNodeVanished(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	// Connect's own teardown always issues one (idempotent) Stop before
	// starting; everything after this baseline must leave the runner alone.
	stopsAfterConnect := h.runner.stops()

	// Drop the connected node (the walk lands on the first, vless-id).
	p.Servers = p.Servers[1:]
	if err := h.store.Update(p); err != nil {
		t.Fatalf("update profile: %v", err)
	}

	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.KillSwitch {
		t.Fatal("kill switch not recorded when the re-apply is deferred")
	}
	h.awaitLogContains("re-apply")
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1 (deferred re-apply must not restart)", h.runner.starts())
	}
	if got := h.runner.stops(); got != stopsAfterConnect {
		t.Errorf("stops = %d, want %d (the live tunnel must be left alone)", got, stopsAfterConnect)
	}
}

// TestKillSwitchAndTunPersistAcrossRestart: both preferences round-trip through
// the settings file and load into a fresh daemon's state; a corrupt stack value
// falls back to the default instead of reaching sing-box.
func TestKillSwitchAndTunPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdSetTun, Stack: singbox.StackGvisor})
	h.await()

	// A "restarted" daemon over the same directory loads both back.
	h2 := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)
	loaded := h2.daemon.snapshotState()
	if !loaded.KillSwitch {
		t.Error("kill switch did not survive the restart")
	}
	if loaded.TunStack != "gvisor" {
		t.Errorf("tun stack = %q, want gvisor after restart", loaded.TunStack)
	}

	// A hand-edited stack the builder doesn't know keeps the default.
	if err := st2.Save(persistedSettings{TunStack: "bogus", KillSwitch: true}); err != nil {
		t.Fatalf("save bogus settings: %v", err)
	}
	h3 := newHarness(t)
	st3, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h3.daemon.SetSettings(st3)
	if got := h3.daemon.snapshotState().TunStack; got != "system" {
		t.Errorf("tun stack = %q, want the system default for a bogus persisted value", got)
	}
}

// waitFor polls cond until it is true, failing the test after 3s. It backs the
// assertions that watch an asynchronous relaunch/reconcile settle.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// TestSetKillSwitchDuringConnectingReconciles: a set_kill_switch that lands while
// a connect is still in its warmup+probe window is applied to the tunnel that
// comes up, not merely recorded. The fallback loop pins its options snapshot at
// the start of the connect, so without the connecting-window reconcile the live
// tunnel would run without strict_route while the state reports armed — core and
// UI would disagree with the wire. The reconcile hot-swaps once the connect
// settles, so the final live config carries strict_route and the reported state
// matches reality.
func TestSetKillSwitchDuringConnectingReconciles(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	// Park the first connect inside its probe so the toggle below is guaranteed to
	// land while the state is still "connecting" (its config already built without
	// strict_route). Later probes — including the reconcile's — run unblocked.
	release := make(chan struct{})
	probing := make(chan struct{}, 1)
	h.runner.onProbe = func(_ context.Context, n int) {
		if n != 1 {
			return
		}
		select {
		case probing <- struct{}{}:
		default:
		}
		<-release
	}

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnecting)
	<-probing // first connect is inside its probe; config[0] is built (no strict_route)

	// Arm the kill switch mid-connect. reapplyLive no-ops while connecting, so this
	// is only recorded here — the reconcile must be what applies it live.
	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: true})
	var armed State
	h.dataInto(h.await(), &armed)
	if !armed.KillSwitch {
		t.Fatal("set_kill_switch response kill_switch = false, want true")
	}

	close(release) // let the first connect finish; the reconcile then hot-swaps

	// The reconcile starts a second process, pinned to the same node; its config
	// must carry the strict_route the toggle asked for.
	h.waitStarts(2)
	cfgs := h.runner.startCfgs()
	if strict, _ := tunFromConfig(t, cfgs[0]); strict {
		t.Error("first (connecting-window) config already had strict_route")
	}
	if strict, _ := tunFromConfig(t, cfgs[len(cfgs)-1]); !strict {
		t.Error("reconciled config lacks strict_route; the mid-connect toggle was not applied to the live tunnel")
	}
	if connectedNodeTag(t, cfgs[0]) != connectedNodeTag(t, cfgs[len(cfgs)-1]) {
		t.Error("reconcile moved the selector; it must pin the node that came up")
	}

	// The live state settles back to connected and genuinely armed — report and
	// reality now agree.
	waitFor(t, func() bool {
		st := h.daemon.snapshotState()
		return st.State == StateConnected && st.KillSwitch && h.runner.starts() == 2
	}, "reconcile to settle connected and armed")
}

// TestKillSwitchDisconnectBeatsRelaunch: an explicit disconnect that lands while a
// kill-switch relaunch is in flight wins — the tunnel must not come back after the
// user asked for it to be down. The relaunch goroutine is parked at its entry so
// the disconnect deterministically claims connMu first; when the relaunch resumes
// it finds the generation moved and yields instead of resurrecting the tunnel.
func TestKillSwitchDisconnectBeatsRelaunch(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	relaunching := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.daemon.beforeReconnect = func() {
		once.Do(func() { close(relaunching) })
		<-release
	}

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	h.waitStarts(1)

	// The process dies with the switch armed: a relaunch is spawned, then parks.
	h.runner.exit(errors.New("boom"))
	h.awaitLogContains("kill switch: tunnel process died")
	<-relaunching
	startsBefore := h.runner.starts()

	// The user disconnects while the relaunch is parked.
	h.send(Request{ID: 3, Cmd: CmdDisconnect})
	var st State
	h.dataInto(h.await(), &st)
	if st.State != StateIdle {
		t.Fatalf("disconnect state = %q, want idle", st.State)
	}
	h.awaitState(StateIdle)

	// Release the relaunch: it must observe the bumped generation and abort, not
	// start a tunnel back up.
	close(release)
	time.Sleep(50 * time.Millisecond) // give the goroutine a chance to (wrongly) act
	if got := h.runner.starts(); got != startsBefore {
		t.Errorf("relaunch started a tunnel after disconnect (starts %d -> %d); disconnect must win", startsBefore, got)
	}
	if got := h.daemon.snapshotState().State; got != StateIdle {
		t.Errorf("state after disconnect+relaunch = %q, want idle (the tunnel must not resurrect)", got)
	}
}

// TestKillSwitchCloseWaitsForRelaunch: Close must not let a kill-switch relaunch
// outlive the daemon and leak a sing-box process. With the relaunch parked in
// flight, Close blocks until it unwinds; the relaunch then observes the
// generation Close bumped and aborts without starting anything, and every started
// process has been stopped.
func TestKillSwitchCloseWaitsForRelaunch(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	relaunching := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.daemon.beforeReconnect = func() {
		once.Do(func() { close(relaunching) })
		<-release
	}

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	h.waitStarts(1)

	h.runner.exit(errors.New("boom"))
	h.awaitLogContains("kill switch: tunnel process died")
	<-relaunching // relaunch parked at its gate, tracked so Close waits for it
	startsBefore := h.runner.starts()

	// Close must block while the relaunch goroutine is still in flight.
	closed := make(chan error, 1)
	go func() { closed <- h.daemon.Close() }()
	select {
	case <-closed:
		t.Fatal("Close returned while an in-flight relaunch was still parked")
	case <-time.After(100 * time.Millisecond):
	}

	// Release it: on the generation Close bumped, it aborts without starting a
	// tunnel, and Close then returns.
	close(release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return after the relaunch unwound; the goroutine leaked")
	}

	if got := h.runner.starts(); got != startsBefore {
		t.Errorf("relaunch started a tunnel across Close (starts %d -> %d)", startsBefore, got)
	}
	if stops, starts := h.runner.stops(), h.runner.starts(); stops < starts {
		t.Errorf("stops(%d) < starts(%d): a sing-box process leaked past Close", stops, starts)
	}
}

// TestKillSwitchRelaunchBudgetRefundedByUptime: a tunnel that keeps dropping but
// stays connected past the reset window between drops is not a crash-loop, so the
// relaunch budget is refunded each time and never runs out — even across far more
// deaths than maxRelaunches. Without the time-gated refund the sixth death would
// degrade to error and stop auto-restarting. A fake clock supplies the uptime.
func TestKillSwitchRelaunchBudgetRefundedByUptime(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	var clkMu sync.Mutex
	now := time.Unix(1_000_000, 0)
	h.daemon.now = func() time.Time { clkMu.Lock(); defer clkMu.Unlock(); return now }
	advance := func(d time.Duration) { clkMu.Lock(); now = now.Add(d); clkMu.Unlock() }

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	// Far more deaths than the budget. Each current tunnel has been up well past
	// the reset window before it dies (we jump the clock forward first), so every
	// death reads as a recovery and refunds the budget rather than spending it.
	for i := 0; i < maxRelaunches+3; i++ {
		advance(2 * defaultRelaunchReset)
		h.runner.exit(errors.New("boom"))
		h.awaitState(StateConnected) // relaunched, not degraded to error
	}
}

// TestKillSwitchRelaunchProfileGoneErrors: if the connected profile has vanished
// from the store by the time its process dies, the armed relaunch can't rebuild a
// config, so it gives up honestly — logging the reason and degrading to error
// rather than silently leaving the tunnel down while claiming to be armed.
func TestKillSwitchRelaunchProfileGoneErrors(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	h.waitStarts(1)

	// Drop the profile straight from the store — not via remove_profile, which
	// would tear the tunnel down first — so the relaunch's own lookup is what fails.
	if err := h.store.Remove(p.ID); err != nil {
		t.Fatalf("remove profile: %v", err)
	}

	h.runner.exit(errors.New("boom"))
	h.awaitLogContains("cannot restart, profile no longer stored")
	errState := h.awaitState(StateError)
	if errState["error"] == nil || errState["error"] == "" {
		t.Errorf("error state missing message: %+v", errState)
	}
	if got := h.runner.starts(); got != 1 {
		t.Errorf("starts = %d, want 1 (no relaunch when the profile is gone)", got)
	}
}
