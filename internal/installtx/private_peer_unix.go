//go:build linux || darwin

package installtx

import "net"

func privateRootPeer(conn net.Conn) bool {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return false
	}
	allowed := false
	if err := raw.Control(func(fd uintptr) { allowed = rootPeerFD(int(fd)) }); err != nil {
		return false
	}
	return allowed
}
