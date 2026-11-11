//go:build linux

package control

import (
	"context"
	"math"
	"net"

	"github.com/Divaaaan/tenebra/internal/sessionauth"
	"golang.org/x/sys/unix"
)

// peerCredUID reads the identity stamped by the kernel on the Unix socket.
func peerCredUID(conn net.Conn) (int, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var uid int
	var innerErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil {
			innerErr = e
			return
		}
		uid = int(cred.Uid)
	})
	if ctrlErr != nil || innerErr != nil {
		return 0, false
	}
	return uid, true
}

func unixPeerAllowed(peer, self int, warn func(string)) bool {
	return linuxPeerAllowed(peer, self, sessionauth.LogindConsoleSource{}, sessionauth.FileOperatorPolicy{}, warn)
}

func linuxPeerAllowed(peer, self int, console sessionauth.ConsoleSource, operators sessionauth.OperatorPolicy, warn func(string)) bool {
	if peer < 0 || uint64(peer) >= math.MaxUint32 {
		return false
	}
	if peer == 0 || peer == self {
		return true
	}
	admission, err := sessionauth.ResolveLinux(context.Background(), console, operators)
	if err != nil {
		warn("control: cannot resolve Linux console/operator admission; refusing peer")
		return false
	}
	return admission.Allows(uint32(peer))
}
