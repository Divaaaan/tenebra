//go:build darwin || linux

package control

import (
	"net"
	"os"
	"strconv"
)

// authorizePeer decides whether the just-accepted control-socket peer may drive
// the daemon, and whether it holds the daemon's own authority (see
// peerPrivileged). It reads the connecting process's uid from the unix socket
// and runs the shared policies against the console user (see peer_auth.go for
// the trust rationale).
//
// A conn whose peer uid cannot be read is REFUSED. The production listener only
// ever yields unix sockets, whose credentials the kernel always attaches, so an
// unreadable uid means the lookup failed — and an identity the daemon cannot
// establish must not be granted a channel that drives a root process.
//
// Both halves it leans on are per-platform: peerCredUID reads the credentials
// the kernel attached to the socket (LOCAL_PEERCRED on macOS, SO_PEERCRED on
// Linux) and consoleUserUID names the account the interactive session belongs
// to. The decision itself is identical on the two, so it lives here rather than
// being copied into each.
func (d *Daemon) authorizePeer(conn net.Conn) (allowed, privileged bool) {
	uid, ok := peerCredUID(conn)
	if !ok {
		d.emitLog(LogWarn, "control: cannot identify the peer on this connection; refusing it")
		return false, false
	}
	peer := strconv.Itoa(uid)
	self := strconv.Itoa(os.Getuid())
	if !peerAllowed(peer, self, consoleUserUID, func(msg string) {
		d.emitLog(LogWarn, msg)
	}) {
		return false, false
	}
	// uid 0 is the unix answer to "already holds the daemon's authority": root
	// can rewrite the daemon's binary or its unit file regardless, so letting it
	// hand the daemon a bundle grants nothing new. Everyone else asks through
	// sudo, which is where the prompt belongs.
	return true, peerPrivileged(peer, self, uid == 0)
}
