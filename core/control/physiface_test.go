package control

import (
	"errors"
	"net"
	"testing"

	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/core/tunguard"
)

// realIfaceName returns a non-tunnel, non-loopback interface this machine
// actually has, so the index lookup is checked against the OS rather than a
// fixture. It must skip tunnels: the machine these tests run on carries other
// VPNs, and handing one of those in as "the physical uplink" would assert the
// very confusion this code exists to avoid.
func realIfaceName(t *testing.T) (string, int) {
	t.Helper()
	list, err := net.Interfaces()
	if err != nil {
		t.Skip("cannot read interfaces on this machine")
	}
	for _, i := range list {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		if tunguard.IsTunnelIface(tunguard.Iface{Name: i.Name}) {
			continue
		}
		return i.Name, i.Index
	}
	t.Skip("no physical interface to test against on this machine")
	return "", 0
}

// TestPhysicalIfaceIndexPicksTheUplink is the case that matters: a machine with
// a tunnel over a physical link must pin the bypass to the link, never to the
// tunnel — filtering the tunnel's own adapter is what broke YouTube inside it.
func TestPhysicalIfaceIndexPicksTheUplink(t *testing.T) {
	name, want := realIfaceName(t)
	d, _ := daemonForConflictTest(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{
			{Name: name, HasDefaultRoute: true, RouteMetric: 25},
			{Name: "tenebra", IsTunnel: true, HasDefaultRoute: true, RouteMetric: 0},
		}, nil
	})

	if got := d.physicalIfaceIndex(); got != want {
		t.Errorf("index = %d, want %d (%s)", got, want, name)
	}
}

// TestPhysicalIfaceIndexIgnoresAnotherVPNsAdapter is the live failure.
//
// The Windows adapter cannot classify interfaces — net.Interfaces exposes no
// driver description — so every Iface it returns has IsTunnel false, including
// another VPN's tun. Trusting that field alone picked the other VPN's adapter as
// "the physical uplink" and pinned the bypass to the one interface it must stay
// off: YouTube died the moment the app started its bypass.
func TestPhysicalIfaceIndexIgnoresAnotherVPNsAdapter(t *testing.T) {
	name, want := realIfaceName(t)
	d, _ := daemonForConflictTest(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{
			// Exactly what the Windows probe produces: IsTunnel unset on both, and
			// the other VPN holding the better metric.
			{Name: "vpnfix", HasDefaultRoute: true, RouteMetric: 0},
			{Name: name, HasDefaultRoute: true, RouteMetric: 25},
		}, nil
	})

	if got := d.physicalIfaceIndex(); got != want {
		t.Errorf("index = %d, want %d (%s) — the bypass would be pinned to the tunnel", got, want, name)
	}
}

// TestPhysicalIfaceIndexIgnoresOurOwnTun: the adapter we are about to raise is
// not an uplink either, whatever metric it ends up with.
func TestPhysicalIfaceIndexIgnoresOurOwnTun(t *testing.T) {
	own := singbox.DefaultTUNName()
	if own == "" {
		t.Skip("platform lets the kernel name the tun; nothing to exclude by name")
	}
	name, want := realIfaceName(t)
	d, _ := daemonForConflictTest(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{
			{Name: own, HasDefaultRoute: true, RouteMetric: 0},
			{Name: name, HasDefaultRoute: true, RouteMetric: 25},
		}, nil
	})

	if got := d.physicalIfaceIndex(); got != want {
		t.Errorf("index = %d, want %d (%s)", got, want, name)
	}
}

// TestPhysicalIfaceIndexPrefersTheLowerMetric: two uplinks (wired and wireless,
// say) resolve the same way the stack itself resolves them.
func TestPhysicalIfaceIndexPrefersTheLowerMetric(t *testing.T) {
	name, want := realIfaceName(t)
	d, _ := daemonForConflictTest(t)
	d.SetInterfaceProbe(func() ([]tunguard.Iface, error) {
		return []tunguard.Iface{
			{Name: "SomeOtherUplink", HasDefaultRoute: true, RouteMetric: 50},
			{Name: name, HasDefaultRoute: true, RouteMetric: 5},
		}, nil
	})

	if got := d.physicalIfaceIndex(); got != want {
		t.Errorf("index = %d, want %d (%s)", got, want, name)
	}
}

// TestPhysicalIfaceIndexRefusesToGuess covers every unclear case. A wrong pin is
// worse than none: the bypass then covers nothing while still reporting that it
// started, and the user only finds out when a video will not play.
func TestPhysicalIfaceIndexRefusesToGuess(t *testing.T) {
	name, _ := realIfaceName(t)
	cases := []struct {
		what  string
		probe func() ([]tunguard.Iface, error)
	}{
		{"no probe installed", nil},
		{"probe failed", func() ([]tunguard.Iface, error) { return nil, errors.New("route table unavailable") }},
		{"only a tunnel has a default route", func() ([]tunguard.Iface, error) {
			return []tunguard.Iface{{Name: "tenebra", IsTunnel: true, HasDefaultRoute: true}}, nil
		}},
		{"no default route at all", func() ([]tunguard.Iface, error) {
			return []tunguard.Iface{{Name: name}}, nil
		}},
		{"two uplinks tied on metric", func() ([]tunguard.Iface, error) {
			return []tunguard.Iface{
				{Name: name, HasDefaultRoute: true, RouteMetric: 25},
				{Name: "SecondUplink", HasDefaultRoute: true, RouteMetric: 25},
			}, nil
		}},
		{"the winner is not a known interface", func() ([]tunguard.Iface, error) {
			return []tunguard.Iface{{Name: "NoSuchAdapter9c1f", HasDefaultRoute: true, RouteMetric: 1}}, nil
		}},
	}

	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			d, _ := daemonForConflictTest(t)
			if c.probe != nil {
				d.SetInterfaceProbe(c.probe)
			}
			if got := d.physicalIfaceIndex(); got != 0 {
				t.Errorf("index = %d, want 0 (%s)", got, c.what)
			}
		})
	}
}
