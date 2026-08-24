package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/core/tunguard"
)

// daemonForConflictTest builds a daemon over a temp store with one connectable
// profile, returning it and the profile id.
func daemonForConflictTest(t *testing.T) (*Daemon, string) {
	t.Helper()
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	node := model.Node{
		Protocol: model.Trojan,
		Name:     "n1",
		Server:   "node.example.test",
		Port:     443,
		Password: "pw",
	}
	p, err := profile.NewProfile("P", profile.SourceManual, "", []model.Node{node})
	if err != nil {
		t.Fatalf("new profile: %v", err)
	}
	if err := store.Add(p); err != nil {
		t.Fatalf("add profile: %v", err)
	}
	return NewDaemon(store, newFakeRunner()), p.ID
}

// foreignTunnel is another VPN holding the default route at a metric that beats
// the physical uplink — the layout that takes a machine offline when a second
// tun joins it.
func foreignTunnel() []tunguard.Iface {
	return []tunguard.Iface{
		{Name: "Ethernet", HasDefault4: true, Metric4: 25},
		{Name: "tun0", IsTunnel: true, HasDefault4: true, Metric4: 0},
	}
}

func TestConnectRefusedWhenAnotherTunnelOwnsTheRoute(t *testing.T) {
	d, pid := daemonForConflictTest(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) { return foreignTunnel(), nil })

	resp := d.handleConnect(context.Background(), Request{ID: 1, Cmd: CmdConnect, Profile: pid})
	if resp.Ok {
		t.Fatal("connect succeeded while another VPN owns the default route")
	}
	// The error has to name the interface: from inside the app the symptom is
	// "no internet", and an unnamed refusal is not actionable.
	if !strings.Contains(resp.Error, "tun0") {
		t.Errorf("error does not name the offender: %q", resp.Error)
	}
}

func TestConnectProceedsWithExplicitOverride(t *testing.T) {
	d, pid := daemonForConflictTest(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) { return foreignTunnel(), nil })

	resp := d.handleConnect(context.Background(), Request{
		ID: 1, Cmd: CmdConnect, Profile: pid, AllowTunConflict: true,
	})
	if !resp.Ok {
		t.Fatalf("override did not let the connect through: %q", resp.Error)
	}
}

// System-proxy mode creates no tun and installs no route, so it cannot collide
// with anything; blocking it would be pure obstruction.
func TestConnectNotGuardedInSystemProxyMode(t *testing.T) {
	d, pid := daemonForConflictTest(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) { return foreignTunnel(), nil })
	d.mu.Lock()
	d.tun.Mode = singbox.ModeSystemProxy
	d.mu.Unlock()

	resp := d.handleConnect(context.Background(), Request{ID: 1, Cmd: CmdConnect, Profile: pid})
	if !resp.Ok {
		t.Fatalf("system-proxy connect was blocked by the tun guard: %q", resp.Error)
	}
}

// A probe that cannot read the route table knows nothing. Turning "unknown" into
// "refuse" would strand the user with an app that will not connect for a reason
// it cannot name — the guard prevents a specific diagnosable failure, it does not
// gate connectivity on its own reliability.
func TestConnectProceedsWhenTheProbeFails(t *testing.T) {
	d, pid := daemonForConflictTest(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return nil, errors.New("route table unavailable")
	})

	resp := d.handleConnect(context.Background(), Request{ID: 1, Cmd: CmdConnect, Profile: pid})
	if !resp.Ok {
		t.Fatalf("a failed probe blocked the connect: %q", resp.Error)
	}
}

// No probe wired (macOS/Linux today, and every unit test that never raises a
// real tun) must behave exactly as before the guard existed.
func TestConnectUnaffectedWithoutAProbe(t *testing.T) {
	d, pid := daemonForConflictTest(t)

	resp := d.handleConnect(context.Background(), Request{ID: 1, Cmd: CmdConnect, Profile: pid})
	if !resp.Ok {
		t.Fatalf("connect blocked with no probe installed: %q", resp.Error)
	}
}

// Our own tun must never block a reconnect: the interface we are about to
// replace is not a conflict.
func TestConnectIgnoresOurOwnTun(t *testing.T) {
	d, pid := daemonForConflictTest(t)
	own := singbox.DefaultTUNName()
	if own == "" {
		t.Skip("platform lets the kernel name the tun; nothing to exclude by name")
	}
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{
			{Name: "Ethernet", HasDefault4: true, Metric4: 25},
			{Name: own, IsTunnel: true, HasDefault4: true, Metric4: 0},
		}, nil
	})

	resp := d.handleConnect(context.Background(), Request{ID: 1, Cmd: CmdConnect, Profile: pid})
	if !resp.Ok {
		t.Fatalf("our own tun blocked the reconnect: %q", resp.Error)
	}
}

// TestAutoconnectRefusesWhenAnotherTunnelOwnsTheRoute is the case the guard was
// written for and the one it was not wired into: a machine starting up with
// another VPN's service already running. The hand-pressed connect was guarded
// from the first day; autoconnect — how the app starts on most days, and the
// only path that runs before anyone is watching — raised its tun regardless.
func TestAutoconnectRefusesWhenAnotherTunnelOwnsTheRoute(t *testing.T) {
	dir := t.TempDir()
	if err := settingsAt(t, dir).Save(persistedSettings{Autoconnect: true, LastProfile: "multi"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	h := newHarness(t)
	seedMultiProto(t, h)
	h.daemon.SetSettings(settingsAt(t, dir))
	h.daemon.SetInterfaceProbe(func() ([]tunguard.Iface, error) { return foreignTunnel(), nil })

	if h.daemon.AutoconnectOnStart() {
		t.Fatal("autoconnect launched an attempt while another VPN owned the default route")
	}
	// The refusal has to name the offender in the log: nobody is looking at the
	// screen when the daemon starts, so the log is the only account of why the
	// tunnel is not up.
	h.awaitLogContains("tun0")
	if h.runner.starts() != 0 {
		t.Errorf("starts = %d, want 0 — no tun may be raised over another VPN's route", h.runner.starts())
	}
	if got := h.daemon.snapshotState().State; got != StateIdle {
		t.Errorf("state = %q, want idle", got)
	}
}

// TestAutoconnectProceedsWhenTheRouteIsFree keeps the guard from turning into a
// blanket block on the start path: with only the machine's own uplink holding a
// default route there is nothing to collide with.
func TestAutoconnectProceedsWhenTheRouteIsFree(t *testing.T) {
	dir := t.TempDir()
	if err := settingsAt(t, dir).Save(persistedSettings{Autoconnect: true, LastProfile: "multi"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	h := newHarness(t)
	seedMultiProto(t, h)
	h.daemon.SetSettings(settingsAt(t, dir))
	h.daemon.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{{Name: "Ethernet", HasDefault4: true, Metric4: 25}}, nil
	})

	if !h.daemon.AutoconnectOnStart() {
		t.Fatal("autoconnect did not launch with the default route free")
	}
	h.awaitState(StateConnected)
}

// TestConnectHonoursACustomInterfaceName is the ownNames path through the control
// layer (issue 5): a user-set TunOptions.InterfaceName must be what the guard
// treats as ours. Our own tun holds the default route at metric 0 by
// construction, so if the guard does not recognise it as ours it counts as a
// metric-0 uplink, zeroes the bar every foreign tunnel is compared against, and
// masks the real conflict. A hardcoded brand name would get this wrong the moment
// the interface is named anything else.
func TestConnectHonoursACustomInterfaceName(t *testing.T) {
	d, pid := daemonForConflictTest(t)
	d.mu.Lock()
	d.tun.InterfaceName = "corp-gw"
	d.mu.Unlock()
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{
			{Name: "Ethernet", HasDefault4: true, Metric4: 25},
			// Our own tun under the custom name, holding the route at metric 0.
			{Name: "corp-gw", HasDefault4: true, Metric4: 0},
			// A foreign tunnel that beats the uplink and must still be caught.
			{Name: "tun0", IsTunnel: true, HasDefault4: true, Metric4: 5},
		}, nil
	})

	resp := d.handleConnect(context.Background(), Request{ID: 1, Cmd: CmdConnect, Profile: pid})
	if resp.Ok {
		t.Fatal("connect succeeded while another tunnel owns the default route")
	}
	if !strings.Contains(resp.Error, "tun0") {
		t.Errorf("error does not name the offender: %q", resp.Error)
	}
	// Our own custom-named tun must not be what it complains about.
	if strings.Contains(resp.Error, "corp-gw") {
		t.Errorf("guard flagged our own tun under its custom name: %q", resp.Error)
	}
}

// TestConnectWithCustomNameIgnoresOnlyOurOwnTun: with nothing but our own
// custom-named tun holding the route (a plain reconnect), the guard must let the
// connect through — the interface it is about to replace is not a conflict.
func TestConnectWithCustomNameIgnoresOnlyOurOwnTun(t *testing.T) {
	d, pid := daemonForConflictTest(t)
	d.mu.Lock()
	d.tun.InterfaceName = "corp-gw"
	d.mu.Unlock()
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{
			{Name: "Ethernet", HasDefault4: true, Metric4: 25},
			{Name: "corp-gw 2", IsTunnel: true, HasDefault4: true, Metric4: 0},
		}, nil
	})

	resp := d.handleConnect(context.Background(), Request{ID: 1, Cmd: CmdConnect, Profile: pid})
	if !resp.Ok {
		t.Fatalf("our own custom-named tun blocked the reconnect: %q", resp.Error)
	}
}
