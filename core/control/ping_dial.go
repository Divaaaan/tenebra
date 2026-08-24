package control

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"

	"github.com/Divaaaan/tenebra/core/tunguard"
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

// selectDefaultInterface picks the physical interface a probe should leave
// through: the primary routable NIC, deliberately never a tunnel — ours or
// anybody else's. A probe dialed while a tunnel owns the default route otherwise
// lands on it and measures the loopback into sing-box (~1ms), not the real
// server, so the whole point is to steer the socket onto the physical link.
//
// It excludes, in order:
//   - our own tun, by case-insensitive name PREFIX, when the platform knows the
//     name (macOS's utun is anonymous — the kernel names it — so tunName is ""
//     there and the rules below carry the exclusion). Prefix and not equality
//     because Windows renames an adapter when the name is taken: a tun raised
//     beside one that has not finished going away comes up as "tenebra 2", which
//     an equality test reads as somebody else's NIC and pins the probe straight
//     back onto our own tunnel. tunguard.isOwn and physicalIfaceIndex match the
//     same way, for the same reason;
//   - anything tunguard.IsTunnelIface recognises as a tunnel. Another VPN's
//     adapter is as wrong an answer as our own tun, and nothing in the ordering
//     below keeps it out: interface indexes say nothing about what an adapter is,
//     so on a machine where the foreign one happens to hold the lower index the
//     probe leaves through it. Only the name is available at this level (a
//     socket-level interface carries no driver description), so this catches the
//     adapters that say what they are — "Hiddify Tunnel", "wg0", "NordLynx" — and
//     not the ones that do not, such as OpenVPN's TAP arriving as "Ethernet 2";
//   - loopback and administratively-down interfaces;
//   - point-to-point interfaces, which is what keeps an anonymous macOS utun (and
//     tun/tap devices in general) out when its exact name is unknown. The
//     trade-off is a genuine point-to-point uplink (a PPP/WWAN modem) would also
//     be skipped; that is rare on the desktops this ships to and is noted for the
//     live pass;
//   - interfaces with no global-unicast address (unconfigured links).
//
// Among the survivors it takes the lowest interface index — the kernel's
// ordering, which puts the primary NIC first. This does NOT consult the routing
// table, so a host-only virtual adapter (VMware VMnet, VirtualBox, a Hyper-V
// internal switch) that carries a routable address but no path to the internet
// can still win it. That is why the bypass pick uses this only as a FALLBACK:
// its first choice is the interface index the packet filter is pinned to, which
// physicalIfaceIndex reads off the route table (see resolveZapretProbeBind).
func selectDefaultInterface(ifaces []ifaceInfo, tunName string) (ifaceInfo, error) {
	own := strings.ToLower(tunName)
	best := ifaceInfo{Index: -1}
	for _, in := range ifaces {
		if own != "" && strings.HasPrefix(strings.ToLower(in.Name), own) {
			continue
		}
		if tunguard.IsTunnelIface(tunguard.Iface{Name: in.Name}) {
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

// zapretProbeBind is where the DPI-bypass strategy PICK dials its probes from:
// the interface their sockets are bound to, or the fact that nothing could be
// bound and they leave by ordinary routing.
//
// It exists so the two halves of a pick cannot disagree. The packet filter is
// confined to one interface (zapret.Runner.PinIfaceIndex, chosen off the route
// table by physicalIfaceIndex), and the probe that scores each strategy has to
// leave by that same interface: a probe on any other link measures a path the
// filter does not touch, so every strategy scores exactly what the baseline
// scored, none can beat it, and the run reports that nothing pierced the block.
// Choosing the interface twice, by two different rules, is how that comes back —
// which is what the pick did while the filter was pinned off the route table and
// the probe took the lowest interface index it could find.
//
// note is the one line the daemon logs about the decision. Without it the
// degradation below is invisible: an unbindable probe falls back to a routed
// dial, which with a tunnel up is once again a measurement of the tunnel, and
// the user sees the same "no strategy pierced the block" with nothing in the log
// to tell the two apart.
type zapretProbeBind struct {
	iface ifaceInfo // the interface to bind to; meaningful only when bound
	bound bool      // false: dial by ordinary routing
	note  string
}

// resolveZapretProbeBind decides where a pick's probes leave from, given the
// interface index the filter is pinned to.
//
// pin is the source of truth. A non-zero pin was chosen off the route table with
// tunnels excluded by flag, name and driver description (physicalIfaceIndex);
// looking that same index up here is what makes the two halves of a run agree by
// construction instead of by two selectors happening to land on the same answer.
//
// pin == 0 means the filter is not confined to anything either — it runs on
// every interface — so there is no pin to agree with, and the fallback takes the
// primary routable NIC from the adapter list. That fallback is the weaker of the
// two: it cannot read the route table, so a host-only virtual adapter with no
// path to the internet can win it. It is still better than the alternative, an
// unbound dial, which with a tunnel up measures the tunnel every single time.
//
// Everything that fails degrades to a routed dial rather than a failed one, and
// says so in note. Refusing to measure would report "no strategy pierced the
// block" on a machine whose uplink is merely unrecognised — a regression against
// the unpinned behaviour rather than a truer answer. The ping path makes the
// opposite trade for its own reason; see errNoPhysicalInterface.
func resolveZapretProbeBind(pin int, list func() ([]ifaceInfo, error)) zapretProbeBind {
	if list == nil {
		return zapretProbeBind{note: "адаптеры не перечислить — меряю по обычной маршрутизации"}
	}
	ifaces, err := list()
	if err != nil {
		return zapretProbeBind{note: fmt.Sprintf(
			"список адаптеров не читается (%v) — меряю по обычной маршрутизации", err)}
	}
	if pin > 0 {
		for _, in := range ifaces {
			if in.Index == pin {
				return zapretProbeBind{iface: in, bound: true, note: fmt.Sprintf(
					"на том же интерфейсе, что и фильтр: %s (index %d)", in.Name, in.Index)}
			}
		}
		return zapretProbeBind{note: fmt.Sprintf(
			"фильтр закреплён за index %d, а такого интерфейса в списке нет — "+
				"меряю по обычной маршрутизации", pin)}
	}
	in, err := selectDefaultInterface(ifaces, tunIfaceName)
	if err != nil {
		return zapretProbeBind{note: "фильтр ни к чему не закреплён и физический интерфейс не опознан — " +
			"меряю по обычной маршрутизации; с поднятым туннелем это снова замер туннеля"}
	}
	return zapretProbeBind{iface: in, bound: true, note: fmt.Sprintf(
		"фильтр ни к чему не закреплён, беру %s (index %d) по списку адаптеров", in.Name, in.Index)}
}

// zapretProbeDialer builds the dialer a pick measures on from a bind decision
// already made.
//
// The decision is made once per run rather than per dial, for the same reason
// the filter's pin is: the pin is written into the strategy's launch file when
// the runner is built, so a probe re-choosing its interface mid-run could drift
// away from the interface the filter is actually on. (The ping dialer does
// re-resolve per dial — it has no pin to stay level with and wants a mid-session
// NIC change picked up by the next probe.)
//
// onBindError, when set, is called with a bind failure. The dial still proceeds
// by ordinary routing — see resolveZapretProbeBind for why measuring something
// beats measuring nothing — but swallowing the error outright left this path
// with no diagnosis at all: a failed bind produced the same verdict, and the
// same log, as the bug it was added to fix.
func zapretProbeDialer(bind zapretProbeBind, onBindError func(error)) *net.Dialer {
	return &net.Dialer{
		Control: func(network, address string, c syscall.RawConn) error {
			if !bind.bound {
				return nil // nothing to pin to; dial by routing, as this always did
			}
			if err := bindSocketToInterface(c, bind.iface, network); err != nil && onBindError != nil {
				onBindError(err)
			}
			return nil
		},
	}
}
