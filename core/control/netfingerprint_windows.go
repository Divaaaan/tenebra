//go:build windows

package control

import (
	"net"
	"runtime"
	"unsafe"

	winsys "golang.org/x/sys/windows"
)

// networkIdentity describes the network the machine's own traffic leaves by:
// the default gateway's hardware address, and the gateway's IP behind it.
//
// The gateway's MAC is the discriminator that actually works. A gateway IP on
// its own does not: 192.168.1.1 and 192.168.0.1 are the address of the router at
// home, at the office and in every cafe, so a fingerprint built on the address
// alone would call three different networks the same one and hand each of them
// the strategy measured on another. The router's hardware address is unique to
// the box, stable for as long as the user keeps going back to it, and — unlike
// an SSID — is there on a cable as well as on Wi-Fi. Our own address is no use
// for this at all: the machine's NIC has the same MAC wherever it is plugged in,
// which is the property that makes it a device identifier and a useless network
// one.
//
// The IP is kept alongside it as a second component rather than as a fallback:
// on the rare network where the neighbour table has no entry for the gateway,
// the address alone still separates most networks, and it costs nothing to
// include when the MAC is there. It does mean the same network fingerprints
// differently depending on whether the MAC was readable at that moment, which
// costs a cache miss and a full pick — the safe direction to be wrong in.
//
// Nothing here sends a packet. Both calls read tables Windows already keeps: the
// adapter list and the neighbour (ARP) cache. Asking the gateway directly, with
// SendARP, would work too and would stay on the local link, but a fingerprint
// that is quiet is easier to keep honest than one that is merely quiet enough.
//
// An empty return means "this network is not recognisable", which the caller
// reads as "no memory for it" and falls back to the single stored strategy.
func networkIdentity() string {
	gw, ifIndex, ok := defaultGatewayV4()
	if !ok {
		return ""
	}
	if mac := gatewayMAC(ifIndex, gw); mac != "" {
		return mac + "@" + gw.String()
	}
	return gw.String()
}

// ifTypePropVirtual is IF_TYPE_PROP_VIRTUAL, which x/sys/windows does not name.
// It is what wintun-based clients report — ours, sing-box's, Tailscale's — so it
// is how a tunnel's own gateway is kept out of the answer. IF_TYPE_PPP is
// deliberately not excluded: a mobile-broadband uplink is PPP, and skipping it
// would leave a tethered machine unidentifiable.
const ifTypePropVirtual = 53

// defaultGatewayV4 returns the IPv4 gateway of the interface the machine's
// ordinary traffic leaves by, with that interface's index.
//
// Tunnels are skipped, and that is the whole reason this does not simply take
// the first gateway it finds. With a tunnel up, its adapter carries a gateway
// too — the same one at home and in a cafe, because it is our own — so
// fingerprinting it would collapse every network the user visits into one entry
// and hand them all the strategy measured on whichever they were on first. The
// choice among what is left goes by interface metric, the same preference the
// stack itself uses to pick an exit.
func defaultGatewayV4() (net.IP, uint32, bool) {
	const flags = winsys.GAA_FLAG_INCLUDE_GATEWAYS |
		winsys.GAA_FLAG_SKIP_ANYCAST |
		winsys.GAA_FLAG_SKIP_MULTICAST |
		winsys.GAA_FLAG_SKIP_DNS_SERVER

	// Two-call idiom, retried: the adapter list can grow between learning the size
	// and filling the buffer. Giving up leaves the network unidentified, which is
	// a missed optimisation and not a failure.
	var size uint32
	var buf []byte
	for attempt := 0; attempt < 4; attempt++ {
		var first *winsys.IpAdapterAddresses
		if size > 0 {
			buf = make([]byte, size)
			first = (*winsys.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		}
		err := winsys.GetAdaptersAddresses(winsys.AF_INET, flags, 0, first, &size)
		if err == winsys.ERROR_BUFFER_OVERFLOW || (first == nil && err == nil) {
			if size == 0 {
				return nil, 0, false
			}
			continue
		}
		if err != nil {
			return nil, 0, false
		}
		return bestGatewayV4(first)
	}
	return nil, 0, false
}

// bestGatewayV4 walks the adapter list for the live, non-tunnel interface with
// the lowest IPv4 metric that has a gateway, and returns that gateway.
func bestGatewayV4(first *winsys.IpAdapterAddresses) (net.IP, uint32, bool) {
	var best net.IP
	var bestIndex, bestMetric uint32
	found := false

	for a := first; a != nil; a = a.Next {
		if a.OperStatus != winsys.IfOperStatusUp {
			continue
		}
		switch a.IfType {
		case winsys.IF_TYPE_SOFTWARE_LOOPBACK, winsys.IF_TYPE_TUNNEL, ifTypePropVirtual:
			continue
		}
		for g := a.FirstGatewayAddress; g != nil; g = g.Next {
			v4 := g.Address.IP().To4()
			if v4 == nil || v4.IsUnspecified() {
				continue
			}
			if !found || a.Ipv4Metric < bestMetric {
				best, bestIndex, bestMetric, found = v4, a.IfIndex, a.Ipv4Metric, true
			}
			break // one gateway per adapter is enough to rank it
		}
	}
	return best, bestIndex, found
}

// mibIPNetRow is MIB_IPNETROW from iphlpapi.h: one entry of the IPv4 neighbour
// (ARP) cache. It is laid out with nothing but DWORDs and a fixed byte array —
// no nested address union — which is why the old IPv4-only GetIpNetTable is used
// here rather than GetIpNetTable2. x/sys/windows declares neither, and of the
// two this is the one whose struct can be got right by reading the header.
type mibIPNetRow struct {
	Index       uint32
	PhysAddrLen uint32
	PhysAddr    [8]byte // MAXLEN_PHYSADDR
	Addr        uint32  // IPv4, in network byte order
	Type        uint32
}

// mibIPNetTypeInvalid is MIB_IPNET_TYPE_INVALID: an entry Windows keeps but no
// longer believes. Reading a MAC out of one would fingerprint a router that is
// not there any more.
const mibIPNetTypeInvalid = 2

var procGetIPNetTable = winsys.NewLazySystemDLL("iphlpapi.dll").NewProc("GetIpNetTable")

// gatewayMAC looks the gateway up in the neighbour cache and returns its
// hardware address, or "" when there is no usable entry.
//
// The cache is read, never primed: if Windows has no entry for the gateway, this
// says so rather than sending an ARP request to make one. The machine talks to
// its gateway constantly, so an entry is there in every case this runs in — and
// on the rare occasion it is not, the caller loses a cache hit, which is a
// slower pick and not a wrong one.
func gatewayMAC(ifIndex uint32, gw net.IP) string {
	v4 := gw.To4()
	if v4 == nil {
		return ""
	}
	// MIB_IPNETROW.dwAddr holds the address in network byte order, which is the
	// order net.IP already stores it in: read the four bytes back as a
	// little-endian DWORD and the two agree.
	want := uint32(v4[0]) | uint32(v4[1])<<8 | uint32(v4[2])<<16 | uint32(v4[3])<<24

	var size uint32
	for attempt := 0; attempt < 4; attempt++ {
		var buf []byte
		var table uintptr
		if size > 0 {
			buf = make([]byte, size)
			table = uintptr(unsafe.Pointer(&buf[0]))
		}
		// The third argument is bOrder; the table is scanned, not indexed, so
		// there is nothing to gain by having Windows sort it.
		ret, _, _ := procGetIPNetTable.Call(table, uintptr(unsafe.Pointer(&size)), 0)
		mac := ""
		if winsys.Errno(ret) == winsys.ERROR_SUCCESS {
			mac = findGatewayMAC(buf, ifIndex, want)
		}
		runtime.KeepAlive(buf)
		switch winsys.Errno(ret) {
		case winsys.ERROR_SUCCESS:
			return mac
		case winsys.ERROR_INSUFFICIENT_BUFFER:
			continue // grew, or this was the sizing call; go round with the new size
		default:
			return ""
		}
	}
	return ""
}

// findGatewayMAC reads a MIB_IPNETTABLE out of buf and returns the hardware
// address recorded for one address on one interface.
//
// The buffer is bounds-checked against the entry count the table declares before
// anything is read through it: this is a byte slice reinterpreted as a struct
// array, so a short or malformed answer must stop here rather than at a segfault.
func findGatewayMAC(buf []byte, ifIndex, addr uint32) string {
	const header = 4 // MIB_IPNETTABLE.dwNumEntries
	if len(buf) < header {
		return ""
	}
	n := int(*(*uint32)(unsafe.Pointer(&buf[0])))
	if n <= 0 || header+n*int(unsafe.Sizeof(mibIPNetRow{})) > len(buf) {
		return ""
	}

	rows := unsafe.Slice((*mibIPNetRow)(unsafe.Pointer(&buf[header])), n)
	for i := range rows {
		row := rows[i]
		if row.Index != ifIndex || row.Addr != addr || row.Type == mibIPNetTypeInvalid {
			continue
		}
		if row.PhysAddrLen == 0 || row.PhysAddrLen > uint32(len(row.PhysAddr)) {
			return ""
		}
		return net.HardwareAddr(row.PhysAddr[:row.PhysAddrLen]).String()
	}
	return ""
}
