//go:build !darwin && !windows

package control

import "net"

// authorizePeer is the fallback for platforms with no supported detached-daemon
// transport: there is no ListenSocket/ListenPipe here, so nothing ever binds a
// world-reachable control channel and there is no peer to authenticate. It
// allows unconditionally, keeping ServeListener buildable everywhere (the cross
// -compiled linux build in CI needs this) without pulling in an OS credential
// API that doesn't exist on the platform.
func (d *Daemon) authorizePeer(conn net.Conn) bool {
	return true
}
