//go:build !darwin && !linux && !windows

package control

import "net"

// authorizePeer is the fallback for platforms with no supported detached-daemon
// transport: there is no ListenSocket/ListenPipe here, so nothing ever binds a
// world-reachable control channel and there is no peer to authenticate. It
// allows unconditionally, keeping ServeListener buildable everywhere without
// pulling in an OS credential API that doesn't exist on the platform. The three
// targets that do bind one — macOS and Linux over a unix socket, Windows over
// the named pipe — each carry a real check instead.
func (d *Daemon) authorizePeer(conn net.Conn) bool {
	return true
}
