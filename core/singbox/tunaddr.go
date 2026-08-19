package singbox

import (
	"net"
	"strings"
)

// tunAddrCandidates are the /30s the tun claims, in order of preference.
//
// The first is the historical default and stays first so nothing moves on a
// machine where it is free. The rest exist because that address is not ours
// alone: Hiddify hands its own tun 172.19.0.1 as well, and two adapters cannot
// hold one address — the second to start fails to configure itself and its
// sing-box dies seconds later, while the app that launched it goes on reporting
// "connected". Seen on the author's machine, and it is the likeliest way this
// app meets any other VPN a user already has installed.
//
// The alternatives avoid the ranges those clients are known to take (172.19 and
// 172.20 for sing-box front-ends, 10.x for WireGuard clients, 172.16/172.17 for
// Docker) and stay inside RFC 1918 space that a home LAN almost never uses.
var tunAddrCandidates = []string{
	"172.19.0.1/30",
	"172.28.0.1/30",
	"172.29.0.1/30",
	"172.30.0.1/30",
	"172.31.0.1/30",
}

// FreeTunAddress returns the first candidate /30 that does not collide with an
// address already assigned on this machine, or the default when every candidate
// is taken.
//
// taken is the set of local interface addresses (any form net.Interface.Addrs
// returns). A collision is judged by network overlap rather than by exact
// address, because a neighbour holding 172.19.0.2 in the same /30 is just as
// fatal as one holding 172.19.0.1.
//
// Falling back to the default rather than failing is deliberate: a machine with
// all five taken is doing something unusual, and refusing to connect over an
// address guess would be a worse answer than trying the address that has always
// worked.
func FreeTunAddress(taken []net.Addr) string {
	nets := make([]*net.IPNet, 0, len(taken))
	for _, a := range taken {
		switch v := a.(type) {
		case *net.IPNet:
			if v.IP.To4() != nil {
				nets = append(nets, v)
			}
		case *net.IPAddr:
			if ip := v.IP.To4(); ip != nil {
				nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)})
			}
		}
	}

	for _, cand := range tunAddrCandidates {
		ip, ipnet, err := net.ParseCIDR(cand)
		if err != nil {
			continue
		}
		if !overlapsAny(ip, ipnet, nets) {
			return cand
		}
	}
	return tunAddrCandidates[0]
}

// overlapsAny reports whether a candidate address or its network touches any of
// the networks already present.
func overlapsAny(ip net.IP, ipnet *net.IPNet, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) || ipnet.Contains(n.IP) {
			return true
		}
	}
	return false
}

// LocalAddrs returns every address assigned to this machine's interfaces, for
// FreeTunAddress. Errors are swallowed: an interface that cannot be queried
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

// tunAddressFor returns the address the tun inbound should carry: the caller's
// explicit choice, or the historical default.
func tunAddressFor(t TunOptions) string {
	if strings.TrimSpace(t.Address) != "" {
		return t.Address
	}
	return tunAddrCandidates[0]
}
