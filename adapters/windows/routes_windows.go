package windows

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"github.com/Divaaaan/tenebra/core/tunguard"
)

// Route enumeration for the tun-conflict guard.
//
// It reads the IPv4 forwarding table straight from iphlpapi rather than parsing
// `route print` or `netsh`: their output is localised, so on a Russian or German
// Windows a text parser silently finds nothing — and a guard that silently finds
// nothing is worse than no guard, because it reports "all clear" for the exact
// machine it was meant to protect.
//
// GetIpForwardTable (the original, IPv4-only call) is used deliberately over
// GetIpForwardTable2: MIB_IPFORWARDROW is a flat block of 14 DWORDs whose layout
// has been stable since Windows 2000, while MIB_IPFORWARD_ROW2 carries nested
// SOCKADDR_INET unions whose padding is easy to get subtly wrong. The guard only
// needs "which interface owns a default route, and at what metric", and the old
// call answers exactly that.
var (
	modiphlpapi           = syscall.NewLazyDLL("iphlpapi.dll")
	procGetIpForwardTable = modiphlpapi.NewProc("GetIpForwardTable")
)

// mibIPForwardRow mirrors MIB_IPFORWARDROW. Every field is a DWORD, so the
// struct is 56 bytes with no padding surprises on either 32- or 64-bit.
type mibIPForwardRow struct {
	ForwardDest      uint32
	ForwardMask      uint32
	ForwardPolicy    uint32
	ForwardNextHop   uint32
	ForwardIfIndex   uint32
	ForwardType      uint32
	ForwardProto     uint32
	ForwardAge       uint32
	ForwardNextHopAS uint32
	ForwardMetric1   uint32
	ForwardMetric2   uint32
	ForwardMetric3   uint32
	ForwardMetric4   uint32
	ForwardMetric5   uint32
}

const errInsufficientBuffer = 122 // ERROR_INSUFFICIENT_BUFFER

// defaultRoutes returns, per interface index, the lowest metric of any default
// route (0.0.0.0/0) that interface owns. An interface absent from the map has no
// default route and therefore cannot capture arbitrary traffic.
func defaultRoutes() (map[uint32]uint32, error) {
	// Two-call idiom: ask with a zero-sized buffer to learn the required size,
	// then allocate. The table can grow between the calls, so the size is read
	// back from the second call's own error rather than assumed.
	var size uint32
	ret, _, _ := procGetIpForwardTable.Call(0, uintptr(unsafe.Pointer(&size)), 0)
	if ret != errInsufficientBuffer && ret != 0 {
		return nil, fmt.Errorf("GetIpForwardTable(size): error %d", ret)
	}
	if size == 0 {
		return map[uint32]uint32{}, nil
	}

	buf := make([]byte, size)
	ret, _, _ = procGetIpForwardTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
	)
	if ret != 0 {
		return nil, fmt.Errorf("GetIpForwardTable: error %d", ret)
	}

	// Layout: DWORD dwNumEntries, then dwNumEntries × MIB_IPFORWARDROW.
	const (
		headerSize = 4
		rowSize    = int(unsafe.Sizeof(mibIPForwardRow{}))
	)
	if len(buf) < headerSize {
		return nil, fmt.Errorf("GetIpForwardTable: short buffer (%d bytes)", len(buf))
	}
	n := int(*(*uint32)(unsafe.Pointer(&buf[0])))

	out := make(map[uint32]uint32, 4)
	for i := 0; i < n; i++ {
		off := headerSize + i*rowSize
		// Guard the arithmetic: a truncated buffer must not be read past.
		if off+rowSize > len(buf) {
			break
		}
		row := (*mibIPForwardRow)(unsafe.Pointer(&buf[off]))
		// A default route is destination 0.0.0.0 with mask 0.0.0.0. Checking the
		// mask too matters: 0.0.0.0 with a non-zero mask is not a default route.
		if row.ForwardDest != 0 || row.ForwardMask != 0 {
			continue
		}
		if cur, ok := out[row.ForwardIfIndex]; !ok || row.ForwardMetric1 < cur {
			out[row.ForwardIfIndex] = row.ForwardMetric1
		}
	}
	return out, nil
}

// Interfaces reports the machine's up interfaces annotated with whether they own
// a default route, in the shape the tun-conflict guard consumes.
//
// Interfaces that are down are skipped: a disabled adapter's stale route cannot
// capture anything, and refusing to connect because of one would be a false
// alarm the user cannot act on.
//
// IsTunnel is deliberately left false here. Classifying an adapter as a tunnel
// from Windows' own metadata needs GetAdaptersAddresses and its IfType, and
// getting that wrong in the permissive direction silently disables the guard;
// the core's name heuristic is conservative, covered by tests, and shared with
// every other platform, so the decision stays in one place.
func Interfaces() ([]tunguard.Iface, error) {
	routes, err := defaultRoutes()
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
		metric, hasDefault := routes[uint32(ifc.Index)]
		out = append(out, tunguard.Iface{
			Name:            ifc.Name,
			HasDefaultRoute: hasDefault,
			RouteMetric:     int(metric),
		})
	}
	return out, nil
}
