package singbox

import (
	"net"
	"strings"
	"testing"
)

func cidr(t *testing.T, s string) net.Addr {
	t.Helper()
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return &net.IPNet{IP: ip, Mask: n.Mask}
}

// TestFreeTunAddressKeepsTheDefaultWhenNothingHoldsIt: the historical address
// stays put on an ordinary machine, so nothing moves for users who never had a
// conflict.
func TestFreeTunAddressKeepsTheDefaultWhenNothingHoldsIt(t *testing.T) {
	local := []net.Addr{cidr(t, "192.168.31.126/24"), cidr(t, "10.8.0.2/24")}
	if got := FreeTunAddress(local); got != "172.19.0.1/30" {
		t.Errorf("address = %q, want the default", got)
	}
}

// TestFreeTunAddressMovesOffAnotherVPN is the bug this exists for: Hiddify hands
// its own tun 172.19.0.1, and two adapters cannot hold one address — the second
// to start fails to configure and its sing-box exits, while the app reports a
// healthy tunnel over nothing.
func TestFreeTunAddressMovesOffAnotherVPN(t *testing.T) {
	local := []net.Addr{cidr(t, "192.168.31.126/24"), cidr(t, "172.19.0.1/28")}

	got := FreeTunAddress(local)

	if strings.HasPrefix(got, "172.19.") {
		t.Fatalf("address = %q, want one clear of the neighbour", got)
	}
	if got == "" {
		t.Fatal("no address chosen")
	}
}

// TestFreeTunAddressAvoidsANeighbourInTheSameSubnet: a neighbour holding .2 of
// the same /30 is as fatal as one holding .1, so overlap is judged by network,
// not by exact address.
func TestFreeTunAddressAvoidsANeighbourInTheSameSubnet(t *testing.T) {
	local := []net.Addr{cidr(t, "172.19.0.2/30")}

	if got := FreeTunAddress(local); strings.HasPrefix(got, "172.19.") {
		t.Errorf("address = %q, want it clear of an occupied /30", got)
	}
}

// TestFreeTunAddressFallsBackRatherThanRefusing: a machine with every candidate
// taken is unusual, and refusing to connect would be a worse answer than trying
// the address that has always worked.
func TestFreeTunAddressFallsBackRatherThanRefusing(t *testing.T) {
	var local []net.Addr
	for _, c := range tunAddrCandidates {
		local = append(local, cidr(t, c))
	}

	if got := FreeTunAddress(local); got != tunAddrCandidates[0] {
		t.Errorf("address = %q, want the default as a last resort", got)
	}
}

// TestBuildUsesTheChosenAddress: picking an address is pointless if the config
// still carries the constant.
func TestBuildUsesTheChosenAddress(t *testing.T) {
	in := tunInbound(TunOptions{Address: "172.28.0.1/30", MTU: 9000, Stack: StackSystem}, false)
	addrs, ok := in["address"].([]string)
	if !ok || len(addrs) == 0 {
		t.Fatalf("no address in the tun inbound: %+v", in)
	}
	if addrs[0] != "172.28.0.1/30" {
		t.Errorf("tun address = %q, want the chosen one", addrs[0])
	}
}

// TestBuildKeepsTheDefaultAddressWhenUnset: an unset option must still produce a
// working tun rather than an empty address sing-box would reject.
func TestBuildKeepsTheDefaultAddressWhenUnset(t *testing.T) {
	in := tunInbound(TunOptions{MTU: 9000, Stack: StackSystem}, false)
	addrs := in["address"].([]string)
	if addrs[0] != "172.19.0.1/30" {
		t.Errorf("tun address = %q, want the default", addrs[0])
	}
}
