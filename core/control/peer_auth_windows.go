//go:build windows

package control

import (
	"fmt"
	"net"

	"golang.org/x/sys/windows"
)

// authorizePeer decides whether the just-accepted named-pipe peer may drive the
// daemon. It resolves the connecting process's user SID and runs the shared
// peerAllowed policy against the console user's SID (see peer_auth.go for the
// trust rationale). A conn whose SID can't be resolved — an in-memory test conn,
// or a lookup failure — is allowed with a log line, matching the policy's
// fail-open stance.
func (d *Daemon) authorizePeer(conn net.Conn) bool {
	sid, ok := pipePeerSID(conn)
	if !ok {
		d.emitLog(LogInfo, "control: peer SID unavailable on this connection; allowing")
		return true
	}
	self, err := currentUserSID()
	if err != nil {
		// An unknown self just means the self-shortcut can't fire; the console
		// check below still governs. Empty never matches a real peer SID.
		self = ""
	}
	return peerAllowed(sid, self, consoleUserSID, func(msg string) {
		d.emitLog(LogWarn, msg)
	})
}

// pipePeerSID resolves the user SID of the process on the client end of the
// named pipe. The accepted winio conn exposes the pipe handle through an
// Fd() uintptr method; GetNamedPipeClientProcessId turns that into the client
// pid, whose token user is the SID. ok=false for any conn that isn't a winio
// pipe (a test in-memory conn) or when the lookup fails, so the caller falls
// open.
func pipePeerSID(conn net.Conn) (string, bool) {
	fder, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return "", false
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(fder.Fd()), &pid); err != nil {
		return "", false
	}
	return userSIDOfProcess(pid)
}

// userSIDOfProcess opens the process by pid (with the minimal
// QUERY_LIMITED_INFORMATION right so it works across integrity levels) and reads
// its token user SID.
func userSIDOfProcess(pid uint32) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return "", false
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", false
	}
	return tu.User.Sid.String(), true
}

// currentUserSID is the daemon's own user SID (LocalSystem when it runs as the
// Windows service): the "self" identity peerAllowed always admits, so an
// elevated same-account helper is not locked out.
func currentUserSID() (string, error) {
	tok := windows.GetCurrentProcessToken()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}

// consoleUserSID reports the user SID of the interactive console session — the
// account the GUI runs under. WTSGetActiveConsoleSessionId names the physical
// console session; WTSQueryUserToken yields that session's user token, whose
// token user is the SID. An error (no active console session — e.g. sitting at
// the login screen, or a session mid-transition) leaves the policy to fail open.
func consoleUserSID() (string, error) {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xFFFFFFFF {
		return "", fmt.Errorf("control: no active console session")
	}
	var tok windows.Token
	if err := windows.WTSQueryUserToken(session, &tok); err != nil {
		return "", err
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}
