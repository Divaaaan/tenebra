package control

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/singbox"
)

// connectedWithTunWatch brings a connection up with the interface watch running
// against a fake presence answer the test controls. present starts true, so the
// tunnel comes up normally; flipping it to false is the failure being modelled.
func connectedWithTunWatch(t *testing.T) (*harness, *atomic.Bool) {
	t.Helper()
	// Where the kernel names the device there is no name to poll, and the watch
	// stays inert on purpose (macOS utunN — see watchTunInterface). Nothing below
	// can be exercised there, so the assertions would only be reporting the
	// documented design as a failure. Keyed off the same value the watch itself
	// reads rather than off GOOS, so the skip cannot drift away from the rule it
	// stands for.
	if singbox.DefaultTUNName() == "" {
		t.Skip("the platform lets the kernel name the tun device; the watch is inert by design")
	}
	h := newHarness(t)
	var present atomic.Bool
	var looks atomic.Int32
	present.Store(true)
	// Set before the connect: the watch goroutine only starts with the connection,
	// so there is no reader to race with here, and an atomic carries the later flip.
	h.daemon.ifacePresent = func(string) bool { looks.Add(1); return present.Load() }
	h.daemon.tunWatchInterval = 20 * time.Millisecond

	p := seedMultiProto(t, h)
	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	// Wait for the watch to see the interface once. Flipping it to absent before
	// that would model an adapter that never came up — a different case, which
	// the watch deliberately stays inert for — and made this test flaky.
	if !eventually(func() bool { return looks.Load() > 0 }) {
		t.Fatal("the interface watch never looked at the adapter")
	}
	return h, &present
}

// TestTunnelDropsWhenItsInterfaceVanishes is the lie this watch exists for.
//
// On a live machine the adapter disappeared about twenty seconds into every
// session while sing-box kept running. The daemon watched only the process, so
// it went on reporting a healthy connection over an interface that no longer
// existed — and every diagnosis after that started from a false premise.
func TestTunnelDropsWhenItsInterfaceVanishes(t *testing.T) {
	h, present := connectedWithTunWatch(t)

	before := h.runner.stops()
	present.Store(false)

	h.awaitLogContaining("disappeared")
	// The orphaned process must not be left running: it holds the config, the
	// clash API and the routes of a tunnel that carries nothing.
	if !eventually(func() bool { return h.runner.stops() > before }) {
		t.Fatal("the tunnel process was left running after its interface vanished")
	}
	// Stopping it is what a real runner turns into an exit, and the exit is what
	// settles the state — the same path a process death already takes.
	h.runner.exit(errors.New("sing-box stopped"))
	ev := h.awaitState(StateError)
	if msg, _ := ev["error"].(string); msg == "" {
		t.Error("the error state carries no reason")
	}
}

// eventually polls cond for up to two seconds.
func eventually(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// TestTunWatchRidesOutAFlicker: an adapter can blink while the stack
// reconfigures. Tearing a working session down for that would be its own bug.
func TestTunWatchRidesOutAFlicker(t *testing.T) {
	h := newHarness(t)
	// Absent for exactly one answer once armed, present ever after — a flicker,
	// whatever the timing. Arming a one-shot rather than sleeping between two
	// stores keeps this deterministic no matter where the watch is in its beat.
	var missOnce atomic.Bool
	h.daemon.ifacePresent = func(string) bool { return !missOnce.CompareAndSwap(true, false) }
	h.daemon.tunWatchInterval = 20 * time.Millisecond

	p := seedMultiProto(t, h)
	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	before := h.runner.stops()

	missOnce.Store(true)
	time.Sleep(400 * time.Millisecond)
	if got := h.daemon.snapshotState().State; got != StateConnected {
		t.Errorf("state = %q after a brief flicker, want connected", got)
	}
	if h.runner.stops() != before {
		t.Error("a flicker stopped the tunnel process")
	}
}

// TestTunWatchLeavesAHealthyTunnelAlone guards the ordinary path: a present
// interface must produce no state change at all.
func TestTunWatchLeavesAHealthyTunnelAlone(t *testing.T) {
	h, _ := connectedWithTunWatch(t)

	time.Sleep(300 * time.Millisecond)
	if got := h.daemon.snapshotState().State; got != StateConnected {
		t.Errorf("state = %q with the interface present, want connected", got)
	}
}

// TestTunWatchInertInSystemProxyMode: proxy mode raises no interface, so an
// absent one is normal and must not be read as a dead tunnel.
func TestTunWatchInertInSystemProxyMode(t *testing.T) {
	h := newHarness(t)
	h.daemon.ifacePresent = func(string) bool { return false }
	h.daemon.tunWatchInterval = 20 * time.Millisecond
	h.daemon.mu.Lock()
	h.daemon.tun.Mode = singbox.ModeSystemProxy
	h.daemon.mu.Unlock()

	p := seedMultiProto(t, h)
	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	time.Sleep(300 * time.Millisecond)
	if got := h.daemon.snapshotState().State; got != StateConnected {
		t.Errorf("state = %q in system-proxy mode, want connected", got)
	}
}

// TestTunWatchDisabledByZeroInterval keeps the escape hatch honest: platforms
// where the kernel names the device can turn the watch off entirely.
func TestTunWatchDisabledByZeroInterval(t *testing.T) {
	h := newHarness(t)
	h.daemon.ifacePresent = func(string) bool { return false }
	h.daemon.tunWatchInterval = 0

	p := seedMultiProto(t, h)
	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	time.Sleep(200 * time.Millisecond)
	if got := h.daemon.snapshotState().State; got != StateConnected {
		t.Errorf("state = %q with the watch disabled, want connected", got)
	}
}

// TestDefaultIfacePresentReadsRealInterfaces: the production answer must say yes
// to an interface this machine actually has and no to one it does not. Without
// this the watch could report every tunnel dead the moment it starts.
func TestDefaultIfacePresentReadsRealInterfaces(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil || len(ifaces) == 0 {
		t.Skip("no interfaces to read on this machine")
	}
	if !defaultIfacePresent(ifaces[0].Name) {
		t.Errorf("interface %q exists but was reported absent", ifaces[0].Name)
	}
	if defaultIfacePresent("tenebra-no-such-adapter-9c1f") {
		t.Error("a made-up interface name was reported present")
	}
}
