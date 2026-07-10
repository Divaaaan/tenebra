//go:build linux

package control

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// tunIfaceName is the tenebra tun's name on Linux. Unlike macOS, Linux accepts
// an arbitrary tun name, so sing-box gives it the branded "tenebra" (see
// core/singbox tunname_other) and the selector can exclude it by name; the
// point-to-point rule backs that up.
const tunIfaceName = "tenebra"

// bindSocketToInterface pins the socket to iface with SO_BINDTODEVICE, which
// ties every packet on the socket to that NIC regardless of the routing table.
// A ping dialed while the tun owns the default route thus egresses through the
// physical link and measures the real server RTT. SO_BINDTODEVICE takes the
// interface name and needs CAP_NET_RAW; the daemon runs as root, which has it.
func bindSocketToInterface(c syscall.RawConn, iface ifaceInfo, network string) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface.Name)
	}); err != nil {
		return fmt.Errorf("control: access ping socket to bind %s: %w", iface.Name, err)
	}
	if setErr != nil {
		return fmt.Errorf("control: SO_BINDTODEVICE %s: %w", iface.Name, setErr)
	}
	return nil
}
