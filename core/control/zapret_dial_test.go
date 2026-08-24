package control

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/tunguard"
)

// noPhysicalInterfaces is an interface list with nothing the selector can pin
// to: a tun, a loopback and a down NIC. It is the shape of a machine where the
// bind cannot be done at all.
//
// The tun is flagged point-to-point as well as named, because the name is not
// enough on every platform: macOS lets the kernel name the device, so
// tunIfaceName is "" there and only the point-to-point rule keeps it out.
func noPhysicalInterfaces() ([]ifaceInfo, error) {
	return []ifaceInfo{
		{Index: 1, Name: tunIfaceName, Flags: net.FlagUp | net.FlagPointToPoint, Addrs: []net.Addr{globalV4("10.8.0.2/24")}},
		{Index: 2, Name: "lo", Flags: net.FlagUp | net.FlagLoopback, Addrs: []net.Addr{globalV4("127.0.0.1/8")}},
		{Index: 3, Name: "eth0", Flags: 0, Addrs: []net.Addr{globalV4("192.168.1.10/24")}},
	}, nil
}

// captureDebugLogs is captureLogs with the level lowered first: the line saying
// where a pick measures is debug, the level a support session turns on.
func captureDebugLogs(t *testing.T, d *Daemon) *[]LogEvent {
	t.Helper()
	if !d.SetLogLevel(LogDebug) {
		t.Fatal("could not lower the log level to debug")
	}
	return captureLogs(t, d)
}

// hasLogLine reports whether any captured line carries every fragment.
func hasLogLine(events []LogEvent, fragments ...string) bool {
	for _, ev := range events {
		all := true
		for _, f := range fragments {
			if !strings.Contains(ev.Msg, f) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// TestZapretRunnerProbesThroughThePinnedDialer is the wiring the fix turns on:
// the runner the daemon builds must open its probe connections through a dialer
// of ours. Left unset, Probe dials by the routing table — which, with the tunnel
// up, means the strategy pick measures the tunnel, the baseline scores full
// marks and no strategy can ever beat it.
func TestZapretRunnerProbesThroughThePinnedDialer(t *testing.T) {
	d, _ := newTestDaemon(t)
	r := d.newZapretRunnerFor(t.TempDir(), true)
	if r.Dial == nil {
		t.Fatal("the bypass runner got no dialer: its probes go wherever routing sends them")
	}
}

// TestZapretPickBindsToTheInterfaceItPins is the agreement the whole path turns
// on: the socket the pick measures through leaves by the same interface the
// packet filter is confined to.
//
// The fixture is a machine with another VPN running — which the machine this
// ships to has. Its adapter holds the default route at the better metric, and is
// placed at the lower interface index, the case that goes wrong. The route says
// "tunnel, skip it" and the filter is pinned to the real NIC — but the
// socket-level list carries no route and no driver description, so a selector
// that only ranks interface indexes takes the other VPN's adapter. The filter
// would then sit on the uplink while the measurement went down somebody else's
// tunnel, where every target answers: baseline full marks, no strategy able to
// beat it, "nothing pierced the block".
//
// The last assertion is the point: the fallback selector really would have gone
// the other way on this fixture, so it is the pin, not the selector, that holds
// the two halves together.
func TestZapretPickBindsToTheInterfaceItPins(t *testing.T) {
	name, idx := realIfaceName(t)
	if idx < 2 {
		t.Skip("no room for an adapter at a lower index than this machine's NIC")
	}
	// Named the way OpenVPN's TAP adapter arrives on Windows: nothing in the name
	// says "tunnel", which is exactly why the name heuristic cannot be what saves
	// this case.
	const foreign = "Ethernet 7"

	d, _ := daemonForConflictTest(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{
			{Name: foreign, IsTunnel: true, HasDefault4: true, Metric4: 0},
			{Name: name, HasDefault4: true, Metric4: 25},
		}, nil
	})
	sockets := []ifaceInfo{
		{Index: idx - 1, Name: foreign, Flags: net.FlagUp, Addrs: []net.Addr{globalV4("10.7.0.2/24")}},
		{Index: idx, Name: name, Flags: net.FlagUp, Addrs: []net.Addr{globalV4("192.168.1.20/24")}},
	}
	d.probeIfaces = func() ([]ifaceInfo, error) { return sockets, nil }

	pin, bind := d.pinAndProbeBind()
	if pin != idx {
		t.Fatalf("filter pinned to index %d, want %d (%s)", pin, idx, name)
	}
	if !bind.bound {
		t.Fatalf("the probe was left unbound while the filter is pinned to index %d: %s", pin, bind.note)
	}
	if bind.iface.Index != pin {
		t.Errorf("the probe binds to index %d while the filter sits on index %d — "+
			"the pick would score every strategy on a link the filter never touches",
			bind.iface.Index, pin)
	}

	fallback, err := selectDefaultInterface(sockets, tunIfaceName)
	if err != nil {
		t.Fatalf("fallback selector: %v", err)
	}
	if fallback.Index != idx-1 {
		t.Fatalf("the fallback selector chose index %d; this fixture no longer reproduces "+
			"the disagreement it exists to pin", fallback.Index)
	}
}

// TestZapretRunnerLogsWhereItMeasures: the pin and the bind both reach the log,
// because a run that reports "nothing pierced the block" is otherwise
// indistinguishable from a run that measured the wrong link.
func TestZapretRunnerLogsWhereItMeasures(t *testing.T) {
	name, idx := realIfaceName(t)
	d, _ := daemonForConflictTest(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{{Name: name, HasDefault4: true, Metric4: 25}}, nil
	})
	d.probeIfaces = func() ([]ifaceInfo, error) {
		return []ifaceInfo{
			{Index: idx, Name: name, Flags: net.FlagUp, Addrs: []net.Addr{globalV4("192.168.1.20/24")}},
		}, nil
	}
	lines := captureDebugLogs(t, d)

	r := d.newZapretRunnerFor(t.TempDir(), true)
	if r.PinIfaceIndex != idx {
		t.Fatalf("runner pinned to index %d, want %d", r.PinIfaceIndex, idx)
	}
	if !hasLogLine(*lines, "index=", name) {
		t.Errorf("nothing in the log says which interface the pick measures on: %v", *lines)
	}
}

// TestZapretPickSaysSoWhenItCannotBindToThePin is the explicit-degradation
// requirement. The filter IS confined to an interface and the probe could not be
// put on it — the one case where the measurement is known to be about a
// different link. It must not pass silently: it degrades to a routed dial (a
// measurement beats no measurement) and says so at a level the user's log keeps.
func TestZapretPickSaysSoWhenItCannotBindToThePin(t *testing.T) {
	name, idx := realIfaceName(t)
	d, _ := daemonForConflictTest(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{{Name: name, HasDefault4: true, Metric4: 25}}, nil
	})
	// The adapter the route table named is not in the socket-level list at all.
	d.probeIfaces = func() ([]ifaceInfo, error) {
		return []ifaceInfo{
			{Index: idx + 1000, Name: "SomethingElse", Flags: net.FlagUp, Addrs: []net.Addr{globalV4("192.168.9.4/24")}},
		}, nil
	}

	pin, bind := d.pinAndProbeBind()
	if pin != idx {
		t.Fatalf("filter pinned to index %d, want %d", pin, idx)
	}
	if bind.bound {
		t.Fatalf("the probe claims a bind to an interface the host does not list: %+v", bind.iface)
	}
	if bind.note == "" {
		t.Error("the degradation carries no explanation to log")
	}

	lines := captureDebugLogs(t, d)
	d.newZapretRunnerFor(t.TempDir(), true)
	if !hasLogLine(*lines, "привязать не вышло") {
		t.Errorf("a pick that could not bind to its own pin said nothing: %v", *lines)
	}
}

// TestZapretProbeBindFallsBackWhenNothingIsPinned covers the other half of the
// deal. A zero pin means the filter is not confined to anything either, so there
// is no pin to agree with; the bind falls back to the adapter list rather than
// giving up, and the note says which of the two ways it got there.
func TestZapretProbeBindFallsBackWhenNothingIsPinned(t *testing.T) {
	good := func() ([]ifaceInfo, error) {
		return []ifaceInfo{
			{Index: 4, Name: "en0", Flags: net.FlagUp, Addrs: []net.Addr{globalV4("192.168.1.20/24")}},
		}, nil
	}
	bind := resolveZapretProbeBind(0, good)
	if !bind.bound || bind.iface.Index != 4 {
		t.Errorf("with no pin the fallback did not take the one physical NIC: %+v", bind)
	}
	if bind.note == "" {
		t.Error("the fallback carries no explanation to log")
	}

	bind = resolveZapretProbeBind(0, noPhysicalInterfaces)
	if bind.bound {
		t.Errorf("a machine with nothing bindable reported a bind: %+v", bind.iface)
	}
	if bind.note == "" {
		t.Error("an unbindable machine carries no explanation to log")
	}
}

// TestNewDaemonPinsTheBypassPick confirms production wires the adapter source in
// rather than leaving the pick's probes on plain routing.
func TestNewDaemonPinsTheBypassPick(t *testing.T) {
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	d := NewDaemon(store, newFakeRunner())
	if d.probeIfaces == nil {
		t.Fatal("daemon cannot enumerate adapters for the bypass pick; every probe would follow the tun")
	}
	if _, err := d.probeIfaces(); err != nil {
		t.Errorf("the production adapter source failed: %v", err)
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
	bind := resolveZapretProbeBind(0, noPhysicalInterfaces)
	if err := zapretProbeDialer(bind, nil).Control("tcp", "1.2.3.4:443", nil); err != nil {
		t.Errorf("the bypass pick refused to dial without a physical interface: %v — "+
			"a machine that cannot be pinned now measures nothing instead of what it measured before", err)
	}
}

// TestZapretProbeBindSurvivesAnUnreadableInterfaceList covers the other failure
// the host can hand back: the adapter list itself erroring out. That must
// degrade to a routed dial too, not take the pick down with it.
func TestZapretProbeBindSurvivesAnUnreadableInterfaceList(t *testing.T) {
	broken := func() ([]ifaceInfo, error) { return nil, errors.New("adapter list unavailable") }
	bind := resolveZapretProbeBind(7, broken)
	if bind.bound {
		t.Fatal("a bind was reported off an adapter list that could not be read")
	}
	if !strings.Contains(bind.note, "не читается") {
		t.Errorf("the note does not say the adapter list failed: %q", bind.note)
	}
	if err := zapretProbeDialer(bind, nil).Control("tcp", "1.2.3.4:443", nil); err != nil {
		t.Errorf("an unreadable interface list failed the dial: %v", err)
	}
}

// unreachableSocket is a syscall.RawConn whose Control hook always fails, which
// is how every platform's bindSocketToInterface reports that it could not touch
// the socket. It stands in for the case a real machine reaches by other routes —
// a socket closed underneath the dialer, a setsockopt the OS refuses.
type unreachableSocket struct{ err error }

func (u unreachableSocket) Control(func(uintptr)) error    { return u.err }
func (u unreachableSocket) Read(func(uintptr) bool) error  { return u.err }
func (u unreachableSocket) Write(func(uintptr) bool) error { return u.err }

// TestZapretProbeDialReportsAFailedBindAndDialsAnyway holds the two halves of
// the degradation together. A bind that fails must not fail the dial — a routed
// measurement beats none — and it must not vanish either: swallowed, a failed
// bind produced the same verdict, and the same log, as the bug the bind was
// added to fix.
//
// On a platform with no bind primitive bindSocketToInterface is a no-op and
// cannot fail; the test establishes which case it is in rather than assuming.
func TestZapretProbeDialReportsAFailedBindAndDialsAnyway(t *testing.T) {
	socket := unreachableSocket{err: errors.New("socket gone")}
	iface := ifaceInfo{Index: 9, Name: "en0"}
	fails := bindSocketToInterface(socket, iface, "tcp") != nil

	var reported []error
	dialer := zapretProbeDialer(zapretProbeBind{iface: iface, bound: true},
		func(err error) { reported = append(reported, err) })

	if err := dialer.Control("tcp", "1.2.3.4:443", socket); err != nil {
		t.Fatalf("a failed bind took the dial down with it: %v", err)
	}
	if !fails {
		t.Skip("this platform has no bind primitive; there is nothing that can fail")
	}
	if len(reported) != 1 {
		t.Fatalf("a failed bind was reported %d times, want 1: the pick would measure the "+
			"routed path with no diagnosis", len(reported))
	}
	if !strings.Contains(reported[0].Error(), iface.Name) {
		t.Errorf("the reported error does not name the interface: %v", reported[0])
	}
}

// TestZapretProbeDialSkipsTheBindWhenThereIsNothingToBindTo: an unbound decision
// must not reach the socket at all, so the routed fallback works on a machine
// where the bind would have failed anyway.
func TestZapretProbeDialSkipsTheBindWhenThereIsNothingToBindTo(t *testing.T) {
	socket := unreachableSocket{err: errors.New("socket gone")}
	var reported []error
	dialer := zapretProbeDialer(resolveZapretProbeBind(0, noPhysicalInterfaces),
		func(err error) { reported = append(reported, err) })

	if err := dialer.Control("tcp", "1.2.3.4:443", socket); err != nil {
		t.Fatalf("an unbound dial failed: %v", err)
	}
	if len(reported) != 0 {
		t.Errorf("an unbound dial reported a bind failure: %v", reported)
	}
}
