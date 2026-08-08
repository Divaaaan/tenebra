package control

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// These tests cover the DPI-bypass toggle end to end at the protocol level:
// recording, reporting and persisting the choice, the engine's process lifecycle
// (which deliberately sits outside the connection's generation protocol, so a node
// switch must not disturb it), and the two failure modes that must never take the
// tunnel with them — an engine that won't start and one that dies mid-session.

// testDPIPort is the loopback socks port the fake engine claims. It matches the
// port handed to SetDPIRunner, which is what must reach the config builder.
const testDPIPort = 2081

// testDPIArgs is a plausible argv for the engine; the daemon passes it through
// verbatim, so its content only has to be recognisable in assertions.
var testDPIArgs = []string{"--ip", "127.0.0.1", "--port", "2081"}

// fakeDPIRunner is an in-memory dpiRunner for tests. It records Start/Stop with
// the argv it was handed, can fail a start or simulate the engine dying, and
// honours the same contract as the real runner: Done blocks before the first
// Start, fires once per process, and the runner is reusable across cycles.
type fakeDPIRunner struct {
	mu sync.Mutex

	startN int
	stopN  int
	args   [][]string
	logs   []string

	// startErr, when set, makes Start fail.
	startErr error

	// onStart/onStop, when set, run (with the lock released) after the call has
	// been recorded. Tests use them to capture what the sing-box side of the world
	// looked like at that instant, which is how the start-before / stop-after
	// ordering around a hot swap is asserted.
	onStart func()
	onStop  func()

	// done is the channel for the current "process", recreated on each Start.
	// Before any Start it is the never-firing channel so Done blocks.
	done chan error
}

func newFakeDPIRunner() *fakeDPIRunner { return &fakeDPIRunner{done: make(chan error)} }

func (f *fakeDPIRunner) Start(ctx context.Context, args []string) error {
	f.mu.Lock()
	if f.startErr != nil {
		err := f.startErr
		f.mu.Unlock()
		return err
	}
	f.startN++
	f.args = append(f.args, append([]string(nil), args...))
	f.done = make(chan error, 1)
	hook := f.onStart
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (f *fakeDPIRunner) Stop() error {
	f.mu.Lock()
	f.stopN++
	hook := f.onStop
	// A real Stop kills the process, so its exit reaches Done; mirror that, without
	// blocking when an exit is already queued.
	select {
	case f.done <- nil:
	default:
	}
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (f *fakeDPIRunner) Done() <-chan error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done
}

func (f *fakeDPIRunner) Logs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.logs))
	copy(out, f.logs)
	return out
}

// exit simulates the engine process terminating with err.
func (f *fakeDPIRunner) exit(err error) {
	f.mu.Lock()
	ch := f.done
	f.mu.Unlock()
	ch <- err
}

func (f *fakeDPIRunner) starts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startN
}

func (f *fakeDPIRunner) stops() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopN
}

// lastArgs returns the argv of the most recent Start, or nil when none happened.
func (f *fakeDPIRunner) lastArgs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.args) == 0 {
		return nil
	}
	return append([]string(nil), f.args[len(f.args)-1]...)
}

func (f *fakeDPIRunner) setStartErr(err error) {
	f.mu.Lock()
	f.startErr = err
	f.mu.Unlock()
}

func (f *fakeDPIRunner) setHooks(onStart, onStop func()) {
	f.mu.Lock()
	f.onStart, f.onStop = onStart, onStop
	f.mu.Unlock()
}

// readyFakeDPIRunner is a fake engine that also answers the optional readiness
// check — the real one does, and it is what keeps the daemon from routing traffic
// into a port some other process happened to grab.
type readyFakeDPIRunner struct {
	*fakeDPIRunner

	readyMu  sync.Mutex
	readyErr error
	waits    int
	addr     string
}

func (f *readyFakeDPIRunner) WaitReady(ctx context.Context, addr string, within time.Duration) error {
	f.readyMu.Lock()
	defer f.readyMu.Unlock()
	f.waits++
	f.addr = addr
	return f.readyErr
}

func (f *readyFakeDPIRunner) setReadyErr(err error) {
	f.readyMu.Lock()
	f.readyErr = err
	f.readyMu.Unlock()
}

func (f *readyFakeDPIRunner) readyCalls() (int, string) {
	f.readyMu.Lock()
	defer f.readyMu.Unlock()
	return f.waits, f.addr
}

// useDPIEngine installs a fake bypass engine on the harness daemon and returns
// it. Without this the daemon has no engine at all, which is the "unsupported
// platform" case the nil-runner tests exercise.
func (h *harness) useDPIEngine() *fakeDPIRunner {
	h.t.Helper()
	f := newFakeDPIRunner()
	h.daemon.SetDPIRunner(f, testDPIPort, testDPIArgs)
	return f
}

// useReadyDPIEngine installs a fake engine that implements the readiness check.
func (h *harness) useReadyDPIEngine() *readyFakeDPIRunner {
	h.t.Helper()
	f := &readyFakeDPIRunner{fakeDPIRunner: newFakeDPIRunner()}
	h.daemon.SetDPIRunner(f, testDPIPort, testDPIArgs)
	return f
}

// TestSetDPIBypassIdleStartsEngine: arming while idle records the choice, brings
// the engine up (so the user learns straight away whether it runs) and reports
// both the flag and the live status, without touching sing-box.
func TestSetDPIBypassIdleStartsEngine(t *testing.T) {
	h := newHarness(t)
	dpi := h.useDPIEngine()

	h.send(Request{ID: 1, Cmd: CmdSetDPIBypass, On: true})
	var st State
	h.dataInto(h.await(), &st)

	if !st.DPIBypass {
		t.Error("set_dpi_bypass response dpi_bypass = false, want true")
	}
	if st.DPIStatus != DPIStatusRunning {
		t.Errorf("dpi_status = %q, want %q", st.DPIStatus, DPIStatusRunning)
	}
	if dpi.starts() != 1 {
		t.Errorf("engine starts = %d, want 1", dpi.starts())
	}
	if got := dpi.lastArgs(); len(got) != len(testDPIArgs) {
		t.Errorf("engine argv = %v, want %v", got, testDPIArgs)
	}
	if h.runner.starts() != 0 {
		t.Errorf("arming while idle started sing-box (starts=%d)", h.runner.starts())
	}
}

// TestSetDPIBypassWithoutEngineErrors: on a platform with no engine (or a build
// whose binary is missing) the command must fail honestly rather than record a
// preference nothing can honour — arming would leave the config pointing at a
// socks port nobody listens on. Nothing may panic on the nil runner.
func TestSetDPIBypassWithoutEngineErrors(t *testing.T) {
	h := newHarness(t) // no SetDPIRunner: the daemon has no engine

	h.send(Request{ID: 1, Cmd: CmdSetDPIBypass, On: true})
	resp := h.await()
	if resp.Ok {
		t.Fatal("set_dpi_bypass succeeded without an engine; want an error")
	}
	if resp.Error == "" {
		t.Error("error response carries no message")
	}

	if st := h.daemon.snapshotState(); st.DPIBypass {
		t.Error("a rejected set_dpi_bypass still recorded the preference")
	}

	// Disarming must keep working without an engine: a settings file carried over
	// from a machine that had one has to be clearable.
	h.send(Request{ID: 2, Cmd: CmdSetDPIBypass, On: false})
	if resp := h.await(); !resp.Ok {
		t.Errorf("disarming without an engine failed: %s", resp.Error)
	}
}

// TestSetDPIBypassUnchangedDoesNotRestart: re-sending the current value must not
// bounce a live tunnel nor respawn the engine.
func TestSetDPIBypassUnchangedDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	dpi := h.useDPIEngine()
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetDPIBypass, On: false}) // already off
	h.await()
	if h.runner.starts() != 1 {
		t.Errorf("sing-box starts = %d, want 1 (a no-op toggle must not restart)", h.runner.starts())
	}
	if dpi.starts() != 0 {
		t.Errorf("engine starts = %d, want 0 (off stays off)", dpi.starts())
	}

	h.send(Request{ID: 3, Cmd: CmdSetDPIBypass, On: true})
	h.await()
	h.waitStarts(2) // the hot swap
	h.awaitState(StateConnected)

	h.send(Request{ID: 4, Cmd: CmdSetDPIBypass, On: true}) // already on
	h.await()
	if h.runner.starts() != 2 {
		t.Errorf("sing-box starts = %d, want 2 (a no-op toggle must not restart)", h.runner.starts())
	}
	if dpi.starts() != 1 {
		t.Errorf("engine starts = %d, want 1 (a no-op toggle must not respawn it)", dpi.starts())
	}
}

// configBypassPort returns the port of the config's DPI-bypass outbound, or 0
// when the config carries none. It is how a test tells a config built with the
// bypass from one built without it, and it doubles as the check that the port the
// engine listens on is the port sing-box dials.
func configBypassPort(t *testing.T, cfgJSON []byte) int {
	t.Helper()
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	for _, o := range cfg.Outbounds {
		if o["tag"] != "dpi" {
			continue
		}
		port, _ := o["server_port"].(float64)
		return int(port)
	}
	return 0
}

// TestSetDPIBypassLiveOrdersEngineAroundTheHotSwap locks the ordering the config
// depends on: arming starts the engine BEFORE sing-box is rebuilt (the new config
// already routes into its socks port), and disarming tears the bypass-carrying
// tunnel down FIRST and stops the engine after, so no live config is ever left
// pointing at a port that has gone away.
func TestSetDPIBypassLiveOrdersEngineAroundTheHotSwap(t *testing.T) {
	h := newHarness(t)
	dpi := h.useDPIEngine()
	p := seedMultiProto(t, h)

	// The rebuild is asynchronous (a connect returns once its loop is launched), so
	// the ordering is asserted against the synchronous half of it: the teardown that
	// stops the running sing-box before the new one is built.
	var mu sync.Mutex
	singboxStartsAtEngineStart, singboxStopsAtEngineStop := -1, -1
	dpi.setHooks(
		func() { mu.Lock(); singboxStartsAtEngineStart = h.runner.starts(); mu.Unlock() },
		func() { mu.Lock(); singboxStopsAtEngineStop = h.runner.stops(); mu.Unlock() },
	)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetDPIBypass, On: true})
	var armed State
	h.dataInto(h.await(), &armed)
	if !armed.DPIBypass || armed.DPIStatus != DPIStatusRunning {
		t.Fatalf("armed state = {%v %q}, want {true %q}", armed.DPIBypass, armed.DPIStatus, DPIStatusRunning)
	}
	h.waitStarts(2)
	re := h.awaitState(StateConnected)
	if re["node"] != connected["node"] {
		t.Errorf("hot swap moved the node: %v -> %v", connected["node"], re["node"])
	}

	h.send(Request{ID: 3, Cmd: CmdSetDPIBypass, On: false})
	var off State
	h.dataInto(h.await(), &off)
	if off.DPIBypass {
		t.Error("disarmed state still reports dpi_bypass")
	}
	if off.DPIStatus != DPIStatusOff {
		t.Errorf("disarmed dpi_status = %q, want empty", off.DPIStatus)
	}
	h.waitStarts(3)

	if dpi.stops() == 0 {
		t.Fatal("disarming left the engine running")
	}
	mu.Lock()
	gotStart, gotStop := singboxStartsAtEngineStart, singboxStopsAtEngineStop
	mu.Unlock()
	if gotStart != 1 {
		t.Errorf("engine started after %d sing-box starts, want 1 (it must be listening before the arming hot swap)", gotStart)
	}
	// Three teardowns have run by then: the connect's own, the arming hot swap's,
	// and the disarming one that took the bypass-carrying tunnel down.
	if gotStop < 3 {
		t.Errorf("engine stopped after %d sing-box teardowns, want at least 3 (the tunnel routing into it must be down first)", gotStop)
	}

	cfgs := h.runner.startCfgs()
	if len(cfgs) < 3 {
		t.Fatalf("sing-box configs = %d, want 3", len(cfgs))
	}
	if got := configBypassPort(t, cfgs[0]); got != 0 {
		t.Errorf("initial config already routes into the bypass (port %d)", got)
	}
	if got := configBypassPort(t, cfgs[1]); got != testDPIPort {
		t.Errorf("armed config bypass port = %d, want %d", got, testDPIPort)
	}
	if got := configBypassPort(t, cfgs[2]); got != 0 {
		t.Errorf("disarmed config still routes into the bypass (port %d)", got)
	}
}

// TestDPIBypassSurvivesNodeSwitch is the reason the engine lives outside the
// connection's generation protocol: switching exits tears sing-box down and starts
// a fresh one, and the bypass engine must ride straight through it — one process,
// never restarted, still running.
func TestDPIBypassSurvivesNodeSwitch(t *testing.T) {
	h := newHarness(t)
	dpi := h.useDPIEngine()
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetDPIBypass, On: true})
	h.await()

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID, Node: p.Servers[0].ID})
	h.await()
	h.awaitState(StateConnected)

	h.send(Request{ID: 3, Cmd: CmdConnect, Profile: p.ID, Node: p.Servers[1].ID})
	h.await()
	h.awaitState(StateConnected)
	h.waitStarts(2)

	if dpi.starts() != 1 {
		t.Errorf("engine starts = %d, want 1: a node switch must not respawn it", dpi.starts())
	}
	if dpi.stops() != 0 {
		t.Errorf("engine stops = %d, want 0: a node switch must not take it down", dpi.stops())
	}
	if st := h.daemon.snapshotState(); st.DPIStatus != DPIStatusRunning {
		t.Errorf("dpi_status = %q after a node switch, want %q", st.DPIStatus, DPIStatusRunning)
	}
}

// TestDPIBypassEngineDeathDoesNotDropTunnel: the engine dying is reported as a
// failed status plus a log line, and that is all — the tunnel it was reshaping a
// share of is untouched, and nothing relaunches sing-box.
func TestDPIBypassEngineDeathDoesNotDropTunnel(t *testing.T) {
	h := newHarness(t)
	dpi := h.useDPIEngine()
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetDPIBypass, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	dpi.exit(errors.New("engine crashed"))
	h.awaitLogContains("dpi bypass")

	waitFor(t, func() bool {
		return h.daemon.snapshotState().DPIStatus == DPIStatusFailed
	}, "dpi_status to reach failed")

	st := h.daemon.snapshotState()
	if st.State != StateConnected {
		t.Errorf("state = %q after the engine died, want the tunnel left connected", st.State)
	}
	if !st.DPIBypass {
		t.Error("a dead engine cleared the user's preference; it must stay armed")
	}
	// Give a stray relaunch a chance to show up before declaring the tunnel calm.
	time.Sleep(20 * time.Millisecond)
	if h.runner.starts() != 1 {
		t.Errorf("sing-box starts = %d, want 1: the engine's death must not bounce the tunnel", h.runner.starts())
	}
}

// TestDPIBypassStartFailureConnectsWithoutIt: an engine that cannot be spawned
// must not hold the tunnel hostage. The connect proceeds with the bypass folded
// out of the config (its socks port would be dead), the status reports failed and
// the user gets a log line — never a silent half-armed state.
func TestDPIBypassStartFailureConnectsWithoutIt(t *testing.T) {
	h := newHarness(t)
	dpi := h.useDPIEngine()
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetDPIBypass, On: true})
	h.await()

	dpi.setStartErr(errors.New("binary not found"))
	// Take the engine down again so the connect below has to spawn a fresh one and
	// hits the failure.
	h.daemon.stopDPIBypass()

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	// The failure is logged from inside the connect, so drain for it before waiting
	// on the connected state — awaitState discards the events it walks past.
	h.awaitLogContains("dpi bypass")
	waitFor(t, func() bool {
		return h.daemon.snapshotState().State == StateConnected
	}, "the tunnel to come up without the bypass")

	st := h.daemon.snapshotState()
	if st.DPIStatus != DPIStatusFailed {
		t.Errorf("dpi_status = %q after a failed spawn, want %q", st.DPIStatus, DPIStatusFailed)
	}
	if !st.DPIBypass {
		t.Error("a failed spawn cleared the user's preference; it must stay armed")
	}
	if got := configBypassPort(t, h.runner.startCfgs()[0]); got != 0 {
		t.Errorf("config routes into the bypass (port %d) although the engine never started", got)
	}
}

// TestPrepareDPIBypassFoldsPortAndFlag covers the single point where the engine
// meets the config builder: with the engine up, the armed flag survives and the
// port it listens on is handed to the builder; with the engine unable to start,
// the flag is folded out so the config never routes into a dead port.
func TestPrepareDPIBypassFoldsPortAndFlag(t *testing.T) {
	h := newHarness(t)
	dpi := h.useDPIEngine()

	ro, tun := h.daemon.prepareDPIBypass(routing.Options{DPIBypass: true}, singbox.TunOptions{})
	if !ro.DPIBypass {
		t.Error("prepare dropped the bypass flag with a healthy engine")
	}
	if tun.DPIPort != testDPIPort {
		t.Errorf("tun.DPIPort = %d, want %d", tun.DPIPort, testDPIPort)
	}
	if dpi.starts() != 1 {
		t.Errorf("engine starts = %d, want 1", dpi.starts())
	}

	// Disarmed: nothing is started and the options are untouched.
	h.daemon.stopDPIBypass()
	ro, tun = h.daemon.prepareDPIBypass(routing.Options{}, singbox.TunOptions{})
	if ro.DPIBypass || tun.DPIPort != 0 {
		t.Errorf("prepare touched the options while disarmed: bypass=%v port=%d", ro.DPIBypass, tun.DPIPort)
	}
	if dpi.starts() != 1 {
		t.Errorf("engine starts = %d, want 1: a disarmed prepare must not spawn it", dpi.starts())
	}

	// Armed but unspawnable: the flag is folded out rather than pointed at nothing.
	dpi.setStartErr(errors.New("binary not found"))
	ro, tun = h.daemon.prepareDPIBypass(routing.Options{DPIBypass: true}, singbox.TunOptions{})
	if ro.DPIBypass {
		t.Error("prepare kept the bypass flag although the engine could not start")
	}
	if tun.DPIPort != 0 {
		t.Errorf("tun.DPIPort = %d, want 0 for a config built without the bypass", tun.DPIPort)
	}
}

// TestPrepareDPIBypassWithoutEngineFoldsOut: a preference loaded from a settings
// file written on a platform that had an engine must degrade to a plain config
// here, not to a config routing into a port nothing serves.
func TestPrepareDPIBypassWithoutEngineFoldsOut(t *testing.T) {
	h := newHarness(t) // no engine installed

	ro, tun := h.daemon.prepareDPIBypass(routing.Options{DPIBypass: true}, singbox.TunOptions{})
	if ro.DPIBypass {
		t.Error("prepare kept the bypass flag with no engine at all")
	}
	if tun.DPIPort != 0 {
		t.Errorf("tun.DPIPort = %d, want 0", tun.DPIPort)
	}
}

// TestSetDPIBypassDuringConnectingReconciles: a set_dpi_bypass that lands while a
// connect is still in its warmup+probe window is applied to the tunnel that comes
// up, not merely recorded. The fallback loop pins its options at the start of the
// connect, so the config that came up carries no bypass while the state reports
// one; the connecting-window reconcile hot-swaps once and closes the gap.
func TestSetDPIBypassDuringConnectingReconciles(t *testing.T) {
	h := newHarness(t)
	h.useDPIEngine()
	p := seedMultiProto(t, h)

	// Park the first connect inside its probe so the toggle below is guaranteed to
	// land while the state is still connecting.
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
	<-probing

	h.send(Request{ID: 2, Cmd: CmdSetDPIBypass, On: true})
	var armed State
	h.dataInto(h.await(), &armed)
	if !armed.DPIBypass {
		t.Fatal("set_dpi_bypass response dpi_bypass = false, want true")
	}

	close(release)
	h.waitStarts(2)

	cfgs := h.runner.startCfgs()
	if got := configBypassPort(t, cfgs[0]); got != 0 {
		t.Errorf("connecting-window config already routed into the bypass (port %d)", got)
	}
	if got := configBypassPort(t, cfgs[len(cfgs)-1]); got != testDPIPort {
		t.Errorf("reconciled config bypass port = %d, want %d; the mid-connect toggle never reached the tunnel", got, testDPIPort)
	}
	if connectedNodeTag(t, cfgs[0]) != connectedNodeTag(t, cfgs[len(cfgs)-1]) {
		t.Error("reconcile moved the selector; it must pin the node that came up")
	}
	waitFor(t, func() bool {
		st := h.daemon.snapshotState()
		return st.State == StateConnected && st.DPIBypass && h.runner.starts() == 2
	}, "the reconcile to settle connected with the bypass")
}

// TestDPIBypassFailedEngineDoesNotLoopTheReconcile guards the trap in that
// reconcile: an engine that cannot start is folded out of every config the daemon
// builds, so comparing the bare preference against what the loop built would find
// a difference no hot swap could ever close — and the daemon would rebuild the
// tunnel forever. The comparison is on the effective value, so a broken engine
// settles after exactly one connect.
func TestDPIBypassFailedEngineDoesNotLoopTheReconcile(t *testing.T) {
	h := newHarness(t)
	dpi := h.useDPIEngine()
	dpi.setStartErr(errors.New("binary not found"))
	p := seedMultiProto(t, h)

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
	<-probing

	h.send(Request{ID: 2, Cmd: CmdSetDPIBypass, On: true})
	h.await()
	close(release)

	waitFor(t, func() bool {
		return h.daemon.snapshotState().State == StateConnected
	}, "the tunnel to come up")
	// Long enough for a reconnect loop to show itself: a hot swap takes a probe
	// cycle, and the harness runs those in milliseconds.
	time.Sleep(150 * time.Millisecond)
	if h.runner.starts() != 1 {
		t.Errorf("sing-box starts = %d, want 1: an unstartable engine must not drive a rebuild loop", h.runner.starts())
	}
	if st := h.daemon.snapshotState(); st.DPIStatus != DPIStatusFailed {
		t.Errorf("dpi_status = %q, want %q", st.DPIStatus, DPIStatusFailed)
	}
}

// TestDPIBypassWaitsForTheListener: an engine that can prove its listener is
// serving is asked to, at exactly the address the config makes sing-box dial —
// the port is only picked before the process binds it, so "spawned" is not
// "listening", and it may not even be our listener.
func TestDPIBypassWaitsForTheListener(t *testing.T) {
	h := newHarness(t)
	dpi := h.useReadyDPIEngine()

	h.send(Request{ID: 1, Cmd: CmdSetDPIBypass, On: true})
	var st State
	h.dataInto(h.await(), &st)
	if st.DPIStatus != DPIStatusRunning {
		t.Fatalf("dpi_status = %q, want %q", st.DPIStatus, DPIStatusRunning)
	}

	waits, addr := dpi.readyCalls()
	if waits != 1 {
		t.Errorf("readiness checks = %d, want 1", waits)
	}
	if want := "127.0.0.1:2081"; addr != want {
		t.Errorf("readiness checked %q, want %q (the address the config dials)", addr, want)
	}
}

// TestDPIBypassUnreadyListenerFoldsOut: a listener that never answers (or is not
// ours — another process can grab the port between picking it and binding it)
// must reap the engine and take the bypass out of the config, never leave traffic
// pointed at an unidentified local port.
func TestDPIBypassUnreadyListenerFoldsOut(t *testing.T) {
	h := newHarness(t)
	dpi := h.useReadyDPIEngine()
	dpi.setReadyErr(errors.New("listener did not answer the SOCKS5 greeting"))

	ro, tun := h.daemon.prepareDPIBypass(routing.Options{DPIBypass: true}, singbox.TunOptions{})
	if ro.DPIBypass {
		t.Error("prepare kept the bypass flag although the listener never came up")
	}
	if tun.DPIPort != 0 {
		t.Errorf("tun.DPIPort = %d, want 0", tun.DPIPort)
	}
	if dpi.stops() == 0 {
		t.Error("a failed readiness check left the engine process behind")
	}
	if st := h.daemon.snapshotState(); st.DPIStatus != DPIStatusFailed {
		t.Errorf("dpi_status = %q, want %q", st.DPIStatus, DPIStatusFailed)
	}
	if h.daemon.dpiUp() {
		t.Error("daemon still considers the engine up after a failed readiness check")
	}
}

// TestSetDPIBypassPersistsAcrossRestart: the toggle round-trips through the
// settings file and loads into a fresh daemon's state.
func TestSetDPIBypassPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	h.useDPIEngine()
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetDPIBypass, On: true})
	h.await()

	// A "restarted" daemon over the same directory loads it back.
	h2 := newHarness(t)
	h2.useDPIEngine()
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)

	loaded := h2.daemon.snapshotState()
	if !loaded.DPIBypass {
		t.Error("dpi bypass did not survive the restart")
	}
	// Loading a preference must not spawn anything: the engine comes up with the
	// first connect (or an explicit toggle), like every other stored setting.
	if loaded.DPIStatus != DPIStatusOff {
		t.Errorf("dpi_status = %q right after loading settings, want empty", loaded.DPIStatus)
	}
}

// TestDPIStatusSurvivesStateTransitions locks the invariant setState depends on:
// the engine's status is daemon-wide runtime state, so a connection transition
// (which builds a fresh State value) must carry it over rather than blank it.
func TestDPIStatusSurvivesStateTransitions(t *testing.T) {
	h := newHarness(t)
	h.useDPIEngine()
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetDPIBypass, On: true})
	h.await()

	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	if st := h.daemon.snapshotState(); st.DPIStatus != DPIStatusRunning {
		t.Errorf("dpi_status = %q after connecting, want %q", st.DPIStatus, DPIStatusRunning)
	}

	h.send(Request{ID: 3, Cmd: CmdDisconnect})
	h.await()
	h.awaitState(StateIdle)
	if st := h.daemon.snapshotState(); st.DPIStatus != DPIStatusRunning {
		t.Errorf("dpi_status = %q after disconnecting, want %q (the engine outlives the tunnel)", st.DPIStatus, DPIStatusRunning)
	}
}

// TestCloseStopsDPIEngine: shutdown must not leave the engine process behind.
func TestCloseStopsDPIEngine(t *testing.T) {
	h := newHarness(t)
	dpi := h.useDPIEngine()
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetDPIBypass, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	if err := h.daemon.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if dpi.stops() == 0 {
		t.Error("Close left the bypass engine running")
	}
	if st := h.daemon.snapshotState(); st.DPIStatus != DPIStatusOff {
		t.Errorf("dpi_status = %q after Close, want empty", st.DPIStatus)
	}
}

// TestSetDPIBypassConcurrentTogglesAreRaceFree hammers the toggle from several
// goroutines while status snapshots read the same state, so -race can prove the
// engine's own mutex and the daemon lock stay untangled.
func TestSetDPIBypassConcurrentTogglesAreRaceFree(t *testing.T) {
	h := newHarness(t)
	h.useDPIEngine()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				h.daemon.Handle(context.Background(), Request{
					ID: int64(i), Cmd: CmdSetDPIBypass, On: n%2 == 0,
				})
				_ = h.daemon.snapshotState()
			}
		}(i)
	}
	wg.Wait()

	// Whatever the last writer chose, the reported status must agree with it.
	st := h.daemon.snapshotState()
	if st.DPIBypass && st.DPIStatus != DPIStatusRunning {
		t.Errorf("armed but dpi_status = %q, want %q", st.DPIStatus, DPIStatusRunning)
	}
	if !st.DPIBypass && st.DPIStatus != DPIStatusOff {
		t.Errorf("disarmed but dpi_status = %q, want empty", st.DPIStatus)
	}
}
