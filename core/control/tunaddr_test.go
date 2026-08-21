package control

import (
	"net"
	"strings"
	"testing"
)

func addr(t *testing.T, s string) net.Addr {
	t.Helper()
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return &net.IPNet{IP: ip, Mask: n.Mask}
}

// TestConnectMovesTheTunOffAnOccupiedAddress: another VPN holding 172.19.0.1 —
// Hiddify does exactly this — used to make our sing-box exit seconds after
// starting, with the daemon still reporting a healthy tunnel. The connect now
// picks an address the machine does not already have.
func TestConnectMovesTheTunOffAnOccupiedAddress(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.localAddrs = func() []net.Addr {
		return []net.Addr{addr(t, "192.168.31.126/24"), addr(t, "172.19.0.1/28")}
	}

	d.pickFreeTunAddress()

	got := d.snapshotTun().Address
	if got == "" {
		t.Fatal("no tun address chosen")
	}
	if strings.HasPrefix(got, "172.19.") {
		t.Errorf("tun address = %q, want it clear of the other VPN", got)
	}
}

// TestConnectKeepsTheDefaultAddressWhenFree: on an ordinary machine nothing
// moves, so a working setup is not disturbed by this logic existing.
func TestConnectKeepsTheDefaultAddressWhenFree(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.localAddrs = func() []net.Addr { return []net.Addr{addr(t, "192.168.31.126/24")} }

	d.pickFreeTunAddress()

	if got := d.snapshotTun().Address; got != "172.19.0.1/30" {
		t.Errorf("tun address = %q, want the default", got)
	}
}
