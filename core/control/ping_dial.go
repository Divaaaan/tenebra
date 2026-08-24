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

// resolvePhysical picks the interface an off-tunnel dial has to leave through.
// list is the interface source: hostInterfaces in production, a synthetic list
// in tests. Resolution happens per dial rather than once at construction so a
// mid-session NIC change (Wi-Fi <-> Ethernet, a VPN reroute) is picked up by the
// next probe.
func resolvePhysical(list func() ([]ifaceInfo, error)) (ifaceInfo, error) {
	ifaces, err := list()
	if err != nil {
		return ifaceInfo{}, err
	}
	return selectDefaultInterface(ifaces, tunIfaceName)
}

// newPingDialer builds the dialer the ping path uses. Its Control hook runs
// after the socket is created but before connect: it resolves the physical
// default interface and binds the socket to it (bindSocketToInterface, per
// platform), forcing the probe out the real NIC even when the tunnel owns the
// default route. A resolution or bind failure fails that dial, which pingOne
// reports as an unreachable node — see errNoPhysicalInterface for why that beats
// a routed fallback.
func newPingDialer() *net.Dialer { return pingDialer(hostInterfaces) }

// pingDialer is newPingDialer with the interface source injected, so the strict
// failure the ping path wants is testable without the host's live adapter list.
func pingDialer(list func() ([]ifaceInfo, error)) *net.Dialer {
	return &net.Dialer{
		Control: func(network, address string, c syscall.RawConn) error {
			iface, err := resolvePhysical(list)
			if err != nil {
				return err
			}
			return bindSocketToInterface(c, iface, network)
		},
	}
}

// newZapretProbeDialer builds the dialer the DPI-bypass strategy PICK measures
// on. Same bind as the ping path, for the reason the pick exists at all: it asks
// whether a strategy gets traffic past the censor on the DIRECT path, and an
// unbound socket asks that of the tunnel instead whenever one is up. Every
// target then answers, the baseline scores full marks, no strategy can beat it,
// and the run reports that nothing pierced the block (see zapret.Best) — which
// is what the automatic re-pick after a bypass failure did for as long as the
// user was connected, i.e. every time it was needed.
//
// It differs from the ping dialer in one place: failing to resolve or bind the
// physical interface degrades to an ordinary routed dial rather than failing the
// dial. The two paths want opposite things from that case. A ping that falls
// back onto the tun reports a fabricated ~1ms RTT, so refusing is the honest
// answer; a pick that fails every probe reports "no strategy pierced the block"
// on a machine whose uplink the selector merely does not recognise (a PPP/WWAN
// modem is point-to-point and skipped), where the routed dial was the correct
// measurement all along. Degrading leaves such a machine exactly where it is
// today instead of regressing it.
func newZapretProbeDialer() *net.Dialer { return zapretProbeDialer(hostInterfaces) }

// zapretProbeDialer is newZapretProbeDialer with the interface source injected,
// so the degradation above is testable without the host's live adapter list.
func zapretProbeDialer(list func() ([]ifaceInfo, error)) *net.Dialer {
	return &net.Dialer{
		Control: func(network, address string, c syscall.RawConn) error {
			iface, err := resolvePhysical(list)
			if err != nil {
				return nil // nothing to pin to; dial by routing, as this always did
			}
			// A bind that fails is swallowed for the same reason a missing
			// interface is: the routed dial is the behaviour this path had before
			// it was pinned, and it beats refusing to measure anything.
			_ = bindSocketToInterface(c, iface, network)
			return nil
		},
	}
}
