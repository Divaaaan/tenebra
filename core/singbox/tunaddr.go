package singbox

import (
	"net"
	"strings"
)

// TunAddrPair is one candidate the tun can carry: an IPv4 /30 and the IPv6 /126
// that goes with it. They travel together because the tun needs both — auto_route
// claims a default route per address family, and dropping the IPv6 one would let
// native IPv6 traffic egress around the tunnel.
type TunAddrPair struct {
	V4 string
	V6 string
}

// tunAddrCandidates are the address pairs the tun claims, in order of preference.
//
// The first pair is the historical default and stays first so nothing moves on a
// machine where it is free. The rest exist because neither address is ours alone:
// Hiddify hands its own tun 172.19.0.1 *and* the same fdfe:dcba:9876::1, and two
// adapters cannot hold one address. Whichever starts second dies — on Windows
// with "configure tun interface: set ipv6 address: The object already exists" —
// while the app that launched it goes on reporting a healthy tunnel.
//
// Both families have to move together: fixing only IPv4 leaves the IPv6 collision
// in place, and the failure looks identical. (That mistake was made once here;
// the log line above is what corrected it.)
var tunAddrCandidates = []TunAddrPair{
	{V4: "172.19.0.1/30", V6: "fdfe:dcba:9876::1/126"},
	{V4: "172.28.0.1/30", V6: "fdfe:dcba:9877::1/126"},
	{V4: "172.29.0.1/30", V6: "fdfe:dcba:9878::1/126"},
	{V4: "172.30.0.1/30", V6: "fdfe:dcba:9879::1/126"},
	{V4: "172.31.0.1/30", V6: "fdfe:dcba:987a::1/126"},
}

// FreeTunAddresses returns the first candidate pair that collides with nothing
// already assigned on this machine, or the default when every candidate is taken.
//
// taken is the set of local interface addresses. A collision is judged by network
// overlap rather than exact address: a neighbour holding .2 of the same /30 is as
// fatal as one holding .1, and the same goes for the IPv6 /126.
//
// Falling back rather than failing is deliberate. A machine with all five pairs
// taken is doing something unusual, and refusing to connect over an address guess
// would be a worse answer than trying the one that has always worked.
func FreeTunAddresses(taken []net.Addr) TunAddrPair {
	nets := toNets(taken)
	for _, cand := range tunAddrCandidates {
		if !cidrCollides(cand.V4, nets) && !cidrCollides(cand.V6, nets) {
			return cand
		}
	}
	return tunAddrCandidates[0]
}

// toNets normalises whatever net.Interface.Addrs returned into networks.
func toNets(addrs []net.Addr) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(addrs))
	for _, a := range addrs {
		switch v := a.(type) {
		case *net.IPNet:
			out = append(out, v)
		case *net.IPAddr:
			bits := 32
			if v.IP.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: v.IP, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return out
}

// cidrCollides reports whether a candidate CIDR touches any network in use.
func cidrCollides(cidr string, nets []*net.IPNet) bool {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return true // unparseable: treat as unusable rather than risk emitting it
	}
	for _, n := range nets {
		if n.Contains(ip) || ipnet.Contains(n.IP) {
			return true
		}
	}
	return false
}

// LocalAddrs returns every address assigned to this machine's interfaces, for
// FreeTunAddresses. Errors are swallowed: an interface that cannot be queried
// simply contributes nothing, which at worst leaves a candidate looking free.
func LocalAddrs() []net.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.Addr
	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		out = append(out, addrs...)
	}
	return out
}

// tunAddressesFor returns the addresses the tun inbound should carry: the
// caller's explicit choice, or the historical default.
func tunAddressesFor(t TunOptions) []string {
	v4 := strings.TrimSpace(t.Address)
	v6 := strings.TrimSpace(t.Address6)
	if v4 == "" {
		v4 = tunAddrCandidates[0].V4
	}
	if v6 == "" {
		v6 = tunAddrCandidates[0].V6
	}
	return []string{v4, v6}
}
