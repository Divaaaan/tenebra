//go:build windows

package control

import (
	"fmt"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// tunIfaceName is the tenebra tun's name on Windows. wintun accepts an arbitrary
// adapter name, so sing-box gives it the branded "tenebra" (see core/singbox
// tunname_other) and the selector excludes it by name. wintun adapters are not
// flagged point-to-point, so the name match — not the point-to-point rule — is
// what keeps the tun out of the ping path here.
const tunIfaceName = "tenebra"

// Winsock option identifiers, not exported by golang.org/x/sys/windows. Values
// per Ws2ipdef.h.
const (
	winIPPROTO_IP      = 0
	winIPPROTO_IPV6    = 41
	winIP_UNICAST_IF   = 31
	winIPV6_UNICAST_IF = 31
)

// bindSocketToInterface pins the socket to iface with IP_UNICAST_IF (IPv4) or
// IPV6_UNICAST_IF (IPv6), the Windows per-socket egress-interface scope. A bound
// socket egresses through iface regardless of the routing table, so a ping
// dialed while the tun owns the default route still leaves via the physical NIC
// and measures the real server RTT.
//
// Quirk worth stating: IP_UNICAST_IF takes the interface index in NETWORK byte
// order, while IPV6_UNICAST_IF takes it in HOST byte order — a long-standing
// Winsock asymmetry (documented on the option itself). SetsockoptInt serialises
// the value in the host's native (little-endian) order, so for the IPv4 option
// we byte-swap the index first to land big-endian on the wire; the IPv6 option
// is passed as-is.
func bindSocketToInterface(c syscall.RawConn, iface ifaceInfo, network string) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		h := windows.Handle(fd)
		if strings.HasSuffix(network, "6") {
			setErr = windows.SetsockoptInt(h, winIPPROTO_IPV6, winIPV6_UNICAST_IF, iface.Index)
			return
		}
		idx := uint32(iface.Index)
		netOrder := (idx&0x000000FF)<<24 | (idx&0x0000FF00)<<8 | (idx&0x00FF0000)>>8 | (idx&0xFF000000)>>24
		setErr = windows.SetsockoptInt(h, winIPPROTO_IP, winIP_UNICAST_IF, int(netOrder))
	}); err != nil {
		return fmt.Errorf("control: access ping socket to bind %s: %w", iface.Name, err)
	}
	if setErr != nil {
		return fmt.Errorf("control: IP_UNICAST_IF %s: %w", iface.Name, setErr)
	}
	return nil
}
