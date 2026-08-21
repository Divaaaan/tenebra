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
	if got := FreeTunAddresses(local).V4; got != "172.19.0.1/30" {
		t.Errorf("address = %q, want the default", got)
	}
}

// TestFreeTunAddressMovesOffAnotherVPN is the bug this exists for: Hiddify hands
// its own tun 172.19.0.1, and two adapters cannot hold one address — the second
// to start fails to configure and its sing-box exits, while the app reports a
// healthy tunnel over nothing.
func TestFreeTunAddressMovesOffAnotherVPN(t *testing.T) {
	local := []net.Addr{cidr(t, "192.168.31.126/24"), cidr(t, "172.19.0.1/28")}

	got := FreeTunAddresses(local).V4

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

	if got := FreeTunAddresses(local).V4; strings.HasPrefix(got, "172.19.") {
		t.Errorf("address = %q, want it clear of an occupied /30", got)
	}
}

// TestFreeTunAddressFallsBackRatherThanRefusing: a machine with every candidate
// taken is unusual, and refusing to connect would be a worse answer than trying
// the address that has always worked.
func TestFreeTunAddressFallsBackRatherThanRefusing(t *testing.T) {
	var local []net.Addr
	for _, c := range tunAddrCandidates {
		local = append(local, cidr(t, c.V4), cidr(t, c.V6))
	}

	if got := FreeTunAddresses(local).V4; got != tunAddrCandidates[0].V4 {
		t.Errorf("address = %q, want the default as a last resort", got)
	}
}

// TestFreeTunAddressMovesWhenOnlyTheIPv6Collides is the failure that actually
// happened. The neighbour's IPv4 was a /28 that did not overlap ours, but it
// carried the same ULA — and Windows refused the whole interface with
// "configure tun interface: set ipv6 address: The object already exists",
// killing sing-box three seconds into every connect. A picker that only weighs
// IPv4 hands back an address pair that fails exactly as before.
func TestFreeTunAddressMovesWhenOnlyTheIPv6Collides(t *testing.T) {
	local := []net.Addr{cidr(t, "10.44.0.1/24"), cidr(t, "fdfe:dcba:9876::1/126")}

	got := FreeTunAddresses(local)

	if got.V6 == tunAddrCandidates[0].V6 {
		t.Errorf("v6 = %q, want it clear of the neighbour's ULA", got.V6)
	}
	// And the pair must stay a pair: an IPv4 from one candidate with an IPv6 from
	// another is two half-checked addresses.
	for _, c := range tunAddrCandidates {
		if c.V4 == got.V4 && c.V6 == got.V6 {
			return
		}
	}
	t.Errorf("chosen pair %+v is not one of the candidates", got)
}

// TestBuildUsesTheChosenAddress: picking an address is pointless if the config
// still carries the constant.
func TestBuildUsesTheChosenAddress(t *testing.T) {
	in := tunInbound(TunOptions{Address: "172.28.0.1/30", Address6: "fdfe:dcba:9877::1/126", MTU: 9000, Stack: StackSystem}, false)
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
