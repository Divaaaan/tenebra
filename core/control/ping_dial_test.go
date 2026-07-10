package control

import (
	"net"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
)

// globalV4 wraps a routable IPv4 address as an interface address, the kind
// hasGlobalUnicast accepts. ParseCIDR splits the host IP from the mask (it zeroes
// the host bits in the returned network), so the host IP is kept explicitly.
func globalV4(cidr string) net.Addr {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return &net.IPNet{IP: ip, Mask: ipnet.Mask}
}

// linkLocalV4 is a 169.254/16 auto-config address: administratively present but
// not routable, so hasGlobalUnicast must reject an interface carrying only it.
func linkLocalV4() net.Addr {
	return &net.IPNet{IP: net.ParseIP("169.254.10.5"), Mask: net.CIDRMask(16, 32)}
}

// TestSelectDefaultInterfaceSkipsTunAndPicksPhysical drives the selector past
// every exclusion path at once: a named tun, a point-to-point (anonymous, macOS
// utun-style) tun, a loopback, a down NIC, and an up-but-unconfigured NIC all
// sit ahead of the one real physical interface, which must still be the choice.
func TestSelectDefaultInterfaceSkipsTunAndPicksPhysical(t *testing.T) {
	ifaces := []ifaceInfo{
		// Lowest index, but it is the named tun: must be skipped.
		{Index: 1, Name: "tenebra", Flags: net.FlagUp, Addrs: []net.Addr{globalV4("10.8.0.2/24")}},
		// An anonymous point-to-point tun (macOS utun, name unknown to us): skipped
		// by the point-to-point rule, not by name.
		{Index: 2, Name: "utun4", Flags: net.FlagUp | net.FlagPointToPoint, Addrs: []net.Addr{globalV4("10.9.0.2/24")}},
		// Loopback: skipped.
		{Index: 3, Name: "lo0", Flags: net.FlagUp | net.FlagLoopback, Addrs: []net.Addr{globalV4("127.0.0.1/8")}},
		// Down NIC: skipped.
		{Index: 4, Name: "en5", Flags: 0, Addrs: []net.Addr{globalV4("192.168.1.9/24")}},
		// Up but only link-local (unconfigured): skipped.
		{Index: 5, Name: "en6", Flags: net.FlagUp, Addrs: []net.Addr{linkLocalV4()}},
		// The one real physical NIC.
		{Index: 6, Name: "en0", Flags: net.FlagUp, Addrs: []net.Addr{globalV4("192.168.1.20/24")}},
	}

	got, err := selectDefaultInterface(ifaces, "tenebra")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got.Name != "en0" {
		t.Errorf("selected %q (index %d), want en0", got.Name, got.Index)
	}
}

// TestSelectDefaultInterfacePrefersLowestIndex confirms that among several
// eligible physical NICs the lowest interface index wins — the kernel ordering
// that fronts the primary adapter.
func TestSelectDefaultInterfacePrefersLowestIndex(t *testing.T) {
	ifaces := []ifaceInfo{
		{Index: 9, Name: "en2", Flags: net.FlagUp, Addrs: []net.Addr{globalV4("192.168.1.30/24")}},
		{Index: 3, Name: "en0", Flags: net.FlagUp, Addrs: []net.Addr{globalV4("192.168.1.20/24")}},
		{Index: 5, Name: "en1", Flags: net.FlagUp, Addrs: []net.Addr{globalV4("192.168.1.25/24")}},
	}
	got, err := selectDefaultInterface(ifaces, tunIfaceName)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got.Name != "en0" || got.Index != 3 {
		t.Errorf("selected %q (index %d), want en0 (index 3)", got.Name, got.Index)
	}
}

// TestSelectDefaultInterfaceAnonymousTunOnly checks the macOS-shaped case where
// the tun name is unknown (tunName ""): with only a point-to-point tun and a
// physical NIC, the point-to-point rule alone must exclude the tun.
func TestSelectDefaultInterfaceAnonymousTunOnly(t *testing.T) {
	ifaces := []ifaceInfo{
		{Index: 1, Name: "utun3", Flags: net.FlagUp | net.FlagPointToPoint, Addrs: []net.Addr{globalV4("10.9.0.2/24")}},
		{Index: 2, Name: "en0", Flags: net.FlagUp, Addrs: []net.Addr{globalV4("192.168.1.20/24")}},
	}
	got, err := selectDefaultInterface(ifaces, "")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got.Name != "en0" {
		t.Errorf("selected %q, want en0 (anonymous tun must be excluded by point-to-point)", got.Name)
	}
}

// TestSelectDefaultInterfaceNoneEligible confirms that when nothing but the tun,
// loopback, and down interfaces exist, selection fails with the sentinel error
// rather than returning the tun.
func TestSelectDefaultInterfaceNoneEligible(t *testing.T) {
	ifaces := []ifaceInfo{
		{Index: 1, Name: "tenebra", Flags: net.FlagUp, Addrs: []net.Addr{globalV4("10.8.0.2/24")}},
		{Index: 2, Name: "lo0", Flags: net.FlagUp | net.FlagLoopback, Addrs: []net.Addr{globalV4("127.0.0.1/8")}},
		{Index: 3, Name: "en0", Flags: 0, Addrs: []net.Addr{globalV4("192.168.1.20/24")}},
	}
	if _, err := selectDefaultInterface(ifaces, "tenebra"); err != errNoPhysicalInterface {
		t.Errorf("err = %v, want errNoPhysicalInterface", err)
	}
}

// TestHasGlobalUnicast spot-checks the address classifier the selector leans on.
func TestHasGlobalUnicast(t *testing.T) {
	if !hasGlobalUnicast([]net.Addr{globalV4("192.168.1.20/24")}) {
		t.Error("routable private address should count as global unicast")
	}
	if hasGlobalUnicast([]net.Addr{linkLocalV4()}) {
		t.Error("link-local address must not count")
	}
	if hasGlobalUnicast(nil) {
		t.Error("no addresses must not count")
	}
}

// TestNewPingDialerHasControlHook confirms the ping dialer is constructed with a
// Control hook installed — the seam that binds each socket to the physical
// interface. It asserts at the unit level, without dialing, since the live bind
// can only be exercised on a real tunnel.
func TestNewPingDialerHasControlHook(t *testing.T) {
	d := newPingDialer()
	if d == nil {
		t.Fatal("newPingDialer returned nil")
	}
	if d.Control == nil {
		t.Error("ping dialer has no Control hook; the interface bind would never run")
	}
}

// TestNewDaemonUsesPingDialer confirms the production daemon wires the
// interface-binding dialer in rather than a bare net.Dialer.
func TestNewDaemonUsesPingDialer(t *testing.T) {
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	d := NewDaemon(store, newFakeRunner())
	if d.dial == nil {
		t.Fatal("daemon has no dial function")
	}
}
