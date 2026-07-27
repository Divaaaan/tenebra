//go:build darwin || linux

package control

import (
	"net"
	"os"
	"strconv"
)

// authorizePeer decides whether the just-accepted control-socket peer may drive
// the daemon. It reads the connecting process's uid from the unix socket and
// runs the shared peerAllowed policy against the console user (see peer_auth.go
// for the trust rationale). A conn whose peer uid can't be read — an in-memory
// test pipe, or a getsockopt failure — is allowed with a log line, matching the
// policy's fail-open stance: the goal is to authenticate, never to brick attach.
//
// Both halves it leans on are per-platform: peerCredUID reads the credentials
// the kernel attached to the socket (LOCAL_PEERCRED on macOS, SO_PEERCRED on
// Linux) and consoleUserUID names the account the interactive session belongs
// to. The decision itself is identical on the two, so it lives here rather than
// being copied into each.
func (d *Daemon) authorizePeer(conn net.Conn) bool {
	uid, ok := peerCredUID(conn)
	if !ok {
		// Only reached off the production path (the real listener always hands us
		// a unix socket); log at info so it doesn't masquerade as a security event.
		d.emitLog(LogInfo, "control: peer uid unavailable on this connection; allowing")
		return true
	}
	self := strconv.Itoa(os.Getuid())
	return peerAllowed(strconv.Itoa(uid), self, consoleUserUID, func(msg string) {
		d.emitLog(LogWarn, msg)
	})
}
