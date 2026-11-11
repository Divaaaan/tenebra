//go:build darwin || linux

package control

import (
	"net"
	"os"
)

// authorizePeer decides whether the just-accepted control-socket peer may drive
// the daemon and whether it holds administrative authority. It applies the
// platform account policy to the UID stamped on the Unix socket by the kernel.
//
// A conn whose peer uid cannot be read is REFUSED. The production listener only
// ever yields unix sockets, whose credentials the kernel always attaches, so an
// unreadable uid means the lookup failed — and an identity the daemon cannot
// establish must not be granted a channel that drives a root process.
//
// The platform policy resolves the physical console and any supported local
// operator policy. Administrative permission is evaluated after admission.
func (d *Daemon) authorizePeer(conn net.Conn) (allowed, privileged bool) {
	uid, ok := peerCredUID(conn)
	if !ok {
		d.emitLog(LogWarn, "control: cannot identify the peer on this connection; refusing it")
		return false, false
	}
	self := os.Getuid()
	if !unixPeerAllowed(uid, self, func(msg string) {
		d.emitLog(LogWarn, msg)
	}) {
		return false, false
	}
	return true, uid == 0 || uid == self
}
