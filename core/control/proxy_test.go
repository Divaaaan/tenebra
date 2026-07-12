package control

import (
	"errors"
	"sync"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

// fakeProxyController is an in-memory systemProxyController for tests. It records
// Enable/Disable calls and returns a scripted Get result, so the guard sequencing
// (arm once, disarm once, disarm on crash/close, never touch a proxy we didn't
// set) is exercised without touching the real registry or networksetup — the
// isolation the task calls for. Safe for concurrent use: the daemon's connect
// goroutines call it while the test asserts.
type fakeProxyController struct {
	mu         sync.Mutex
	enableN    int
	disableN   int
	lastEnable string
	enableErr  error
	disableErr error
	getState   proxyState
	getErr     error
}

func (f *fakeProxyController) Enable(hostport string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enableN++
	f.lastEnable = hostport
	return f.enableErr
}

func (f *fakeProxyController) Disable() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disableN++
	return f.disableErr
}

func (f *fakeProxyController) Get() (proxyState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getState, f.getErr
}

func (f *fakeProxyController) enables() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enableN
}

func (f *fakeProxyController) disables() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disableN
}

func (f *fakeProxyController) lastHostPort() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastEnable
}

// useFakeProxy swaps the harness daemon's real system-proxy controller for a fake
// before any connect runs, so a system-proxy test never writes the host's real OS
// proxy. Called before the first command, its write is published to the serve
// goroutine by the pipe handoff of that command.
func (h *harness) useFakeProxy() *fakeProxyController {
	f := &fakeProxyController{}
	h.daemon.proxy = f
	return f
}

// bareDaemonWithProxy builds a minimal daemon (no server) with a fake proxy for
// the guard-primitive tests, which drive armSystemProxy/disarmSystemProxy and the
// startup reconcile directly rather than through the connect lifecycle.
func bareDaemonWithProxy(t *testing.T) (*Daemon, *fakeProxyController) {
	t.Helper()
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	d := NewDaemon(store, newFakeRunner())
	f := &fakeProxyController{}
	d.proxy = f
	return d, f
}

// TestSystemProxyArmDisarmIdempotent pins the core guard invariant: arming twice
// writes the OS proxy once, and disarming twice clears it once. The daemon drives
// these from repeatable paths (a hot-swap re-promotes, a teardown fires on every
// transition), so a non-idempotent guard would thrash the registry.
func TestSystemProxyArmDisarmIdempotent(t *testing.T) {
	d, f := bareDaemonWithProxy(t)

	d.armSystemProxy("127.0.0.1:2080")
	d.armSystemProxy("127.0.0.1:2080") // already armed: must be a no-op
	if f.enables() != 1 {
		t.Errorf("enables = %d, want 1 (arm is idempotent)", f.enables())
	}
	if got := f.lastHostPort(); got != "127.0.0.1:2080" {
		t.Errorf("armed at %q, want 127.0.0.1:2080", got)
	}

	d.disarmSystemProxy()
	d.disarmSystemProxy() // already clear: must be a no-op
	if f.disables() != 1 {
		t.Errorf("disables = %d, want 1 (disarm is idempotent)", f.disables())
	}
}

// TestSystemProxyDisarmWithoutArmIsNoop: disarming when we never armed must not
// touch the OS proxy — the guard only ever clears a proxy it set itself, so a
// tun-mode teardown (which still calls disarm) leaves a user's own proxy alone.
func TestSystemProxyDisarmWithoutArmIsNoop(t *testing.T) {
	d, f := bareDaemonWithProxy(t)
	d.disarmSystemProxy()
	if f.disables() != 0 {
		t.Errorf("disarm without a prior arm called Disable %d times, want 0", f.disables())
	}
}

// TestSystemProxyArmFailureLeavesDisarmed: a failed Enable must leave the guard
// disarmed, so a later teardown does not wrongly believe it owns (and then clear)
// a proxy that was never set. The tunnel is up but unprotected — a visible, safe
// failure, not a corrupt half-state.
func TestSystemProxyArmFailureLeavesDisarmed(t *testing.T) {
	d, f := bareDaemonWithProxy(t)
	f.enableErr = errors.New("registry write denied")

	d.armSystemProxy("127.0.0.1:2080")
	if f.enables() != 1 {
		t.Errorf("enables = %d, want 1 attempt", f.enables())
	}
	d.disarmSystemProxy()
	if f.disables() != 0 {
		t.Errorf("disarm after a failed arm called Disable %d times, want 0 (nothing was set)", f.disables())
	}
}

// TestReconcileClearsOurStaleProxy: the startup backstop clears a proxy a previous
// run left pointing at exactly our loopback address (the SIGKILL/power-loss case).
func TestReconcileClearsOurStaleProxy(t *testing.T) {
	d, f := bareDaemonWithProxy(t) // d.tun.MixedPort defaults to DefaultMixedPort (2080)
	f.getState = proxyState{Enabled: true, Server: "127.0.0.1:2080"}

	cleared, err := d.ReconcileSystemProxyAtStartup()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !cleared || f.disables() != 1 {
		t.Errorf("cleared=%v disables=%d, want true/1 for a stale proxy at our address", cleared, f.disables())
	}
}

// TestReconcileLeavesForeignProxy: the reconcile must never clear a proxy tenebra
// did not set — a corporate remote proxy, another local tool on a different port,
// or a merely-disabled entry. This is the safety bound on a routine that turns the
// user's connectivity off.
func TestReconcileLeavesForeignProxy(t *testing.T) {
	cases := []struct {
		name string
		st   proxyState
	}{
		{"remote corporate proxy", proxyState{Enabled: true, Server: "10.0.0.1:8080"}},
		{"another local tool, different port", proxyState{Enabled: true, Server: "127.0.0.1:7890"}},
		{"our address but already disabled", proxyState{Enabled: false, Server: "127.0.0.1:2080"}},
		{"nothing set", proxyState{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, f := bareDaemonWithProxy(t)
			f.getState = c.st

			cleared, err := d.ReconcileSystemProxyAtStartup()
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if cleared || f.disables() != 0 {
				t.Errorf("cleared=%v disables=%d, want false/0 — must not touch a proxy we didn't set", cleared, f.disables())
			}
		})
	}
}

// TestSystemProxyArmsOnConnectDisarmsOnDisconnect drives the full lifecycle: a
// system-proxy connect points the OS at the loopback mixed inbound, and a
// disconnect clears it.
func TestSystemProxyArmsOnConnectDisarmsOnDisconnect(t *testing.T) {
	h := newHarness(t)
	f := h.useFakeProxy()
	p := h.addProfile([]model.Node{vlessNode("A", "a.example.aa")})

	h.send(Request{ID: 1, Cmd: CmdSetProxyMode, ProxyMode: "system-proxy"})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	if f.enables() != 1 {
		t.Fatalf("enables = %d after connect, want 1", f.enables())
	}
	if got := f.lastHostPort(); got != "127.0.0.1:2080" {
		t.Errorf("armed at %q, want 127.0.0.1:2080", got)
	}

	h.send(Request{ID: 3, Cmd: CmdDisconnect})
	h.await()
	if f.disables() < 1 {
		t.Errorf("disables = %d after disconnect, want >= 1", f.disables())
	}
}

// TestSystemProxyDisarmsOnProcessDeath is the crash guard: if sing-box dies
// unexpectedly with the kill switch off, the guard must clear the OS proxy so the
// machine is not left pointing at a dead local proxy with no internet.
func TestSystemProxyDisarmsOnProcessDeath(t *testing.T) {
	h := newHarness(t)
	f := h.useFakeProxy()
	p := h.addProfile([]model.Node{vlessNode("A", "a.example.aa")})

	h.send(Request{ID: 1, Cmd: CmdSetProxyMode, ProxyMode: "system-proxy"})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	if f.enables() != 1 {
		t.Fatalf("proxy not armed on connect (enables=%d)", f.enables())
	}

	h.runner.exit(errors.New("sing-box crashed"))
	h.awaitState(StateError)
	if f.disables() < 1 {
		t.Errorf("disables = %d after a process crash, want >= 1 — a dead proxy must be cleared", f.disables())
	}
}

// TestSystemProxyDisarmsOnClose: daemon shutdown (the deferred cleanup path) must
// clear an armed proxy so a normal or signalled exit never strands the OS.
func TestSystemProxyDisarmsOnClose(t *testing.T) {
	h := newHarness(t)
	f := h.useFakeProxy()
	p := h.addProfile([]model.Node{vlessNode("A", "a.example.aa")})

	h.send(Request{ID: 1, Cmd: CmdSetProxyMode, ProxyMode: "system-proxy"})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	if f.enables() != 1 {
		t.Fatalf("proxy not armed on connect (enables=%d)", f.enables())
	}

	if err := h.daemon.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if f.disables() < 1 {
		t.Errorf("disables = %d after Close, want >= 1", f.disables())
	}
}
