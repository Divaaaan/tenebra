package windows

import (
	"fmt"
	"net"
	"unsafe"

	"github.com/Divaaaan/tenebra/core/tunguard"
	winsys "golang.org/x/sys/windows"
)

// Route and adapter enumeration for the tun-conflict guard.
//
// It reads the forwarding table and the adapter list straight from iphlpapi
// rather than parsing `route print` or `netsh`: their output is localised, so on
// a Russian or German Windows a text parser silently finds nothing — and a guard
// that silently finds nothing is worse than no guard, because it reports "all
// clear" for the exact machine it was meant to protect.
//
// GetIpForwardTable2 is used over the older IPv4-only GetIpForwardTable for two
// reasons. It answers for IPv6 as well, and a tunnel that owns ::/0 captures
// every AAAA-resolved destination on a dual-stack machine, which is most of
// them; and its MIB_IPFORWARD_ROW2 comes with a vetted struct definition in
// golang.org/x/sys/windows, so the nested SOCKADDR_INET unions that made the
// call unattractive are not this package's to get right.
//
// The metric reported is the effective one — the route's own metric plus the
// owning interface's metric, which is what `route print` shows and what Windows
// actually compares when it picks a path. The route metric alone is not usable:
// every tunnel writes its route at 0, and so does the physical uplink on a
// machine behind a Hyper-V switch (measured 2026-08-24: route metric 0,
// interface metric 25). Reading it raw made the machine's own uplink look like a
// metric-0 path and, through the guard's "parked at a losing metric" test, waved
// every genuine conflict through.

// Interface types that mean "tunnel" in Windows' own metadata. IF_TYPE_TUNNEL is
// the honest one; IF_TYPE_PROP_VIRTUAL is what wintun-based clients actually
// report — measured 2026-08-24, sing-box's tun and Tailscale both come back as
// 53, while Hyper-V's vEthernet, which is the real uplink on that machine, comes
// back as 6 (ethernet). IF_TYPE_PPP is deliberately absent: a mobile-broadband
// or dial-up uplink is PPP, and classifying a machine's only exit as a tunnel
// would turn the guard into a refusal to ever connect.
const (
	ifTypePropVirtual = 53
	ifTypeTunnel      = 131
)

// adapter is what GetAdaptersAddresses knows about one interface beyond what
// net.Interfaces exposes: the driver's description, whether Windows itself calls
// the thing a tunnel, and the per-family interface metric.
type adapter struct {
	description string
	tunnel      bool
	metric4     uint32
	metric6     uint32
}

// metric returns the interface metric for one address family.
func (a adapter) metric(family uint16) uint32 {
	if family == winsys.AF_INET6 {
		return a.metric6
	}
	return a.metric4
}

// adapters returns the adapter metadata by interface index.
//
// It is best-effort: on failure it yields an empty map rather than an error, so
// the guard falls back to name heuristics and raw route metrics instead of going
// off altogether. A probe error disables the guard entirely (see
// control.checkTunConflict), and half the information is worth more than none.
func adapters() map[uint32]adapter {
	const flags = winsys.GAA_FLAG_SKIP_ANYCAST |
		winsys.GAA_FLAG_SKIP_MULTICAST |
		winsys.GAA_FLAG_SKIP_DNS_SERVER

	out := map[uint32]adapter{}
	// Two-call idiom, retried: the adapter list can grow between learning the
	// size and filling the buffer. A handful of attempts is plenty; giving up
	// lands on the empty map above.
	var size uint32
	var buf []byte
	for attempt := 0; attempt < 4; attempt++ {
		var first *winsys.IpAdapterAddresses
		if size > 0 {
			buf = make([]byte, size)
			first = (*winsys.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		}
		err := winsys.GetAdaptersAddresses(winsys.AF_UNSPEC, flags, 0, first, &size)
		if err == winsys.ERROR_BUFFER_OVERFLOW || (first == nil && err == nil) {
			// Nothing was written; go round with the size the call reported. A
			// zero size with no error means there is nothing to read at all.
			if size == 0 {
				return out
			}
			continue
		}
		if err != nil {
			return out
		}
		for a := first; a != nil; a = a.Next {
			info := adapter{
				description: winsys.UTF16PtrToString(a.Description),
				tunnel:      a.IfType == ifTypeTunnel || a.IfType == ifTypePropVirtual,
				metric4:     a.Ipv4Metric,
				metric6:     a.Ipv6Metric,
			}
			// Keyed under both indices: an adapter can carry a different index per
			// family, and net.Interfaces reports whichever is non-zero.
			if a.IfIndex != 0 {
				out[a.IfIndex] = info
			}
			if a.Ipv6IfIndex != 0 {
				out[a.Ipv6IfIndex] = info
			}
		}
		return out
	}
	return out
}

// readRoutes folds one address family's forwarding table into the coverage
// accumulator.
func readRoutes(family uint16, info map[uint32]adapter, into coverTable) error {
	var table *winsys.MibIpForwardTable2
	if err := winsys.GetIpForwardTable2(family, &table); err != nil {
		return fmt.Errorf("GetIpForwardTable2(family %d): %w", family, err)
	}
	if table == nil {
		return nil
	}
	defer winsys.FreeMibTable(unsafe.Pointer(table))

	for _, row := range table.Rows() {
		prefix := row.DestinationPrefix
		var dest []byte
		switch prefix.Prefix.Family {
		case winsys.AF_INET:
			sa := (*winsys.RawSockaddrInet4)(unsafe.Pointer(&prefix.Prefix))
			dest = sa.Addr[:]
		case winsys.AF_INET6:
			sa := (*winsys.RawSockaddrInet6)(unsafe.Pointer(&prefix.Prefix))
			dest = sa.Addr[:]
		default:
			continue
		}
		metric := addMetric(row.Metric, info[row.InterfaceIndex].metric(prefix.Prefix.Family))
		into.add(row.InterfaceIndex, prefix.Prefix.Family, dest, int(prefix.PrefixLength), metric)
	}
	return nil
}

// addMetric sums a route metric and an interface metric without wrapping. A
// metric that saturates is a route nothing will ever choose, which is the same
// answer the arithmetic would have given had it not overflowed.
func addMetric(route, iface uint32) uint32 {
	sum := uint64(route) + uint64(iface)
	if sum > uint64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(sum)
}

// defaultRoutes returns, per interface index, the metric of the best route by
// which that interface can capture arbitrary traffic — over either address
// family, and by a literal default route or by the split pair. An interface
// absent from the map has no such route and therefore cannot swallow ours.
func defaultRoutes(info map[uint32]adapter) (map[uint32]uint32, error) {
	table := coverTable{}
	for _, family := range []uint16{winsys.AF_INET, winsys.AF_INET6} {
		if err := readRoutes(family, info, table); err != nil {
			return nil, err
		}
	}
	return table.defaults(), nil
}

// Interfaces reports the machine's up interfaces annotated with whether they own
// a default route and what the driver says they are, in the shape the
// tun-conflict guard consumes.
//
// Interfaces that are down are skipped: a disabled adapter's stale route cannot
// capture anything, and refusing to connect because of one would be a false
// alarm the user cannot act on.
//
// IsTunnel used to be left false here, on the grounds that classifying an
// adapter wrong in the permissive direction silently disables the guard. That
// deferred the whole job to the core's name heuristic, which cannot do it:
// OpenVPN's TAP adapter is called "Ethernet 2" and no list of names will ever
// catch it. Windows does know — in IfType, and in the description the vendor
// wrote — so both are passed on, and the core still gets to apply its own
// heuristic on top.
func Interfaces() ([]tunguard.Iface, error) {
	info := adapters()
	routes, err := defaultRoutes(info)
	if err != nil {
		return nil, err
	}
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("enumerate interfaces: %w", err)
	}

	out := make([]tunguard.Iface, 0, len(ifs))
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		a := info[uint32(ifc.Index)]
		metric, hasDefault := routes[uint32(ifc.Index)]
		out = append(out, tunguard.Iface{
			Name:            ifc.Name,
			Description:     a.description,
			IsTunnel:        a.tunnel,
			HasDefaultRoute: hasDefault,
			RouteMetric:     int(metric),
		})
	}
	return out, nil
}
