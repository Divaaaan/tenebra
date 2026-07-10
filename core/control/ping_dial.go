package control

import (
	"errors"
	"net"
	"syscall"
)

// errNoPhysicalInterface means the selector found no routable physical NIC to
// pin a ping to. It fails the dial rather than falling back to a routed dial: a
// routed dial while the tunnel is up is the very thing we are avoiding (it lands
// on the tun and reports a bogus ~1ms), so a node marked unreachable is a more
// honest outcome than a meaningless RTT.
var errNoPhysicalInterface = errors.New("control: no physical default interface for ping")

// ifaceInfo is the subset of a network interface selectDefaultInterface reads,
// pulled out so the selection logic is unit-testable with synthetic interfaces
// instead of the host's live adapter list (which a test cannot control).
type ifaceInfo struct {
	Index int
	Name  string
	Flags net.Flags
	Addrs []net.Addr
}

// hostInterfaces snapshots the host's interfaces into ifaceInfo values. It is
// the production seam over net.Interfaces()/Interface.Addrs(); tests exercise
// selectDefaultInterface directly with synthetic input and never call it.
func hostInterfaces() ([]ifaceInfo, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]ifaceInfo, 0, len(ifs))
	for _, in := range ifs {
		addrs, err := in.Addrs()
		if err != nil {
			// An interface whose addresses we can't read can't be a chosen exit
			// (hasGlobalUnicast would reject it anyway); skip it rather than abort
			// the whole snapshot for one flaky adapter.
			continue
		}
		out = append(out, ifaceInfo{Index: in.Index, Name: in.Name, Flags: in.Flags, Addrs: addrs})
	}
	return out, nil
}

// hasGlobalUnicast reports whether an interface carries at least one routable
// unicast address — the signal that it is actually carrying traffic, not merely
// administratively up. Loopback and link-local addresses (a plugged-in but
// unconfigured NIC) don't count, so such interfaces are never chosen.
func hasGlobalUnicast(addrs []net.Addr) bool {
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.IsGlobalUnicast() && !ip.IsLinkLocalUnicast() {
			return true
		}
	}
	return false
}

// selectDefaultInterface picks the physical interface a ping should leave
// through: the primary routable NIC, deliberately never the tenebra tun. A ping
// dialed while the tunnel owns the default route otherwise lands on the tun and
// measures the loopback into sing-box (~1ms), not the real server, so the whole
// point is to steer the probe onto the physical link and read the true RTT.
//
// It excludes, in order:
//   - the tun by name, when the platform knows it (macOS's utun is anonymous —
//     the kernel names it — so tunName is "" there and the point-to-point rule
//     below carries the exclusion);
//   - loopback and administratively-down interfaces;
//   - point-to-point interfaces, which is what keeps the macOS utun (and tun/tap
//     devices in general) out when its exact name is unknown. The trade-off is a
//     genuine point-to-point uplink (a PPP/WWAN modem) would also be skipped;
//     that is rare on the desktops this ships to and is noted for the live pass;
//   - interfaces with no global-unicast address (unconfigured links).
//
// Among the survivors it takes the lowest interface index — the kernel's
// ordering, which puts the primary NIC first. This does NOT consult the routing
// table, so on a host with several routable NICs it may not match the kernel's
// exact default route; tightening that against a live tunnel is the follow-up
// flagged in the change notes.
func selectDefaultInterface(ifaces []ifaceInfo, tunName string) (ifaceInfo, error) {
	best := ifaceInfo{Index: -1}
	for _, in := range ifaces {
		if tunName != "" && in.Name == tunName {
			continue
		}
		if in.Flags&net.FlagUp == 0 {
			continue
		}
		if in.Flags&net.FlagLoopback != 0 {
			continue
		}
		if in.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		if !hasGlobalUnicast(in.Addrs) {
			continue
		}
		if best.Index == -1 || in.Index < best.Index {
			best = in
		}
	}
	if best.Index == -1 {
		return ifaceInfo{}, errNoPhysicalInterface
	}
	return best, nil
}

// newPingDialer builds the dialer the ping path uses. Its Control hook runs
// after the socket is created but before connect: it resolves the physical
// default interface and binds the socket to it (bindSocketToInterface, per
// platform), forcing the probe out the real NIC even when the tunnel owns the
// default route. Resolution happens per dial rather than once at construction so
// a mid-session NIC change (Wi-Fi <-> Ethernet, a VPN reroute) is picked up on
// the next ping. A resolution or bind failure fails that dial, which pingOne
// reports as an unreachable node — see errNoPhysicalInterface for why that beats
// a routed fallback.
func newPingDialer() *net.Dialer {
	return &net.Dialer{
		Control: func(network, address string, c syscall.RawConn) error {
			ifaces, err := hostInterfaces()
			if err != nil {
				return err
			}
			iface, err := selectDefaultInterface(ifaces, tunIfaceName)
			if err != nil {
				return err
			}
			return bindSocketToInterface(c, iface, network)
		},
	}
}
