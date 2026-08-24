package control

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
)

// noPhysicalInterfaces is an interface list with nothing the selector can pin
// to: a tun, a loopback and a down NIC. It is the shape of a machine where the
// bind cannot be done at all.
func noPhysicalInterfaces() ([]ifaceInfo, error) {
	return []ifaceInfo{
		{Index: 1, Name: tunIfaceName, Flags: net.FlagUp, Addrs: []net.Addr{globalV4("10.8.0.2/24")}},
		{Index: 2, Name: "lo", Flags: net.FlagUp | net.FlagLoopback, Addrs: []net.Addr{globalV4("127.0.0.1/8")}},
		{Index: 3, Name: "eth0", Flags: 0, Addrs: []net.Addr{globalV4("192.168.1.10/24")}},
	}, nil
}

// TestZapretRunnerProbesThroughThePinnedDialer is the wiring the fix turns on:
// the runner the daemon builds must open its probe connections through the
// daemon's interface-bound dialer. Left unset, Probe dials by the routing table
// — which, with the tunnel up, means the strategy pick measures the tunnel, the
// baseline scores full marks and no strategy can ever beat it.
func TestZapretRunnerProbesThroughThePinnedDialer(t *testing.T) {
	d, _ := newTestDaemon(t)

	dials := 0
	d.zapretDial = func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, errors.New("no network in tests")
	}

	r := d.newZapretRunnerFor(t.TempDir(), true)
	if r.Dial == nil {
		t.Fatal("the bypass runner got no dialer: its probes go wherever routing sends them")
	}
	if _, err := r.Dial(context.Background(), "tcp", "example.invalid:443"); err == nil {
		t.Error("the runner's dialer is not the daemon's")
	}
	if dials != 1 {
		t.Errorf("the daemon's dialer was called %d times, want 1", dials)
	}
}

// TestNewDaemonPinsTheBypassPick confirms production wires a real dialer in
// rather than leaving the runner on plain routing.
func TestNewDaemonPinsTheBypassPick(t *testing.T) {
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	d := NewDaemon(store, newFakeRunner())
	if d.zapretDial == nil {
		t.Fatal("daemon has no dialer for the bypass pick; every probe would follow the tun")
	}
}

// TestPinnedDialersDisagreeOnAnUnbindableMachine pins the one deliberate
// difference between the two pinned dialers. With no interface to bind to, the
// ping path refuses the dial — a routed ping reports the tun's fabricated ~1ms
// and a node marked unreachable is more honest. The bypass pick instead falls
// back to routing: refusing there would report "no strategy pierced the block"
// on a machine whose uplink the selector simply does not recognise, which is a
// regression against the unpinned behaviour rather than a truer answer.
func TestPinnedDialersDisagreeOnAnUnbindableMachine(t *testing.T) {
	if _, err := resolvePhysical(noPhysicalInterfaces); err == nil {
		t.Fatal("the selector accepted a machine with no physical interface")
	}

	// Both hooks return before touching the socket on this path, so a nil
	// RawConn is enough to drive them.
	if err := pingDialer(noPhysicalInterfaces).Control("tcp", "1.2.3.4:443", nil); err == nil {
		t.Error("the ping dialer allowed an unpinned dial; a ping would report the tun's RTT")
	}
	if err := zapretProbeDialer(noPhysicalInterfaces).Control("tcp", "1.2.3.4:443", nil); err != nil {
		t.Errorf("the bypass pick refused to dial without a physical interface: %v — "+
			"a machine that cannot be pinned now measures nothing instead of what it measured before", err)
	}
}

// TestZapretProbeDialerSurvivesAnUnreadableInterfaceList covers the other
// failure the host can hand back: the adapter list itself erroring out. That
// must degrade to a routed dial too, not take the pick down with it.
func TestZapretProbeDialerSurvivesAnUnreadableInterfaceList(t *testing.T) {
	broken := func() ([]ifaceInfo, error) { return nil, errors.New("adapter list unavailable") }
	if err := zapretProbeDialer(broken).Control("tcp", "1.2.3.4:443", nil); err != nil {
		t.Errorf("an unreadable interface list failed the dial: %v", err)
	}
}
