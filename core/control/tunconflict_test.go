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
		{Name: "Ethernet", HasDefaultRoute: true, RouteMetric: 25},
		{Name: "tun0", IsTunnel: true, HasDefaultRoute: true, RouteMetric: 0},
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
			{Name: "Ethernet", HasDefaultRoute: true, RouteMetric: 25},
			{Name: own, IsTunnel: true, HasDefaultRoute: true, RouteMetric: 0},
		}, nil
	})

	resp := d.handleConnect(context.Background(), Request{ID: 1, Cmd: CmdConnect, Profile: pid})
	if !resp.Ok {
		t.Fatalf("our own tun blocked the reconnect: %q", resp.Error)
	}
}
