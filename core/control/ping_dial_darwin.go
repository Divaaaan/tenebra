//go:build darwin

package control

import (
	"fmt"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// tunIfaceName is the name of the tenebra tun on macOS: empty. The kernel
// reserves the tun namespace to its own utun<N> pattern and sing-box lets it
// assign the number (see core/singbox tunname_darwin), so the daemon can't name
// the device ahead of time. selectDefaultInterface therefore relies on its
// point-to-point / no-global-address heuristics to keep the utun out of the ping
// path rather than a name match.
const tunIfaceName = ""

// bindSocketToInterface pins the socket to iface with IP_BOUND_IF (IPv4) or
// IPV6_BOUND_IF (IPv6), Darwin's per-socket interface scope. A bound socket
// ignores the routing table and always egresses through iface, so a ping dialed
// while the utun owns the default route still leaves via the physical NIC and
// measures the real server RTT. The option value is the interface index.
func bindSocketToInterface(c syscall.RawConn, iface ifaceInfo, network string) error {
	level, opt := unix.IPPROTO_IP, unix.IP_BOUND_IF
	if strings.HasSuffix(network, "6") {
		level, opt = unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF
	}
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = unix.SetsockoptInt(int(fd), level, opt, iface.Index)
	}); err != nil {
		return fmt.Errorf("control: access ping socket to bind %s: %w", iface.Name, err)
	}
	if setErr != nil {
		return fmt.Errorf("control: IP_BOUND_IF %s: %w", iface.Name, setErr)
	}
	return nil
}
