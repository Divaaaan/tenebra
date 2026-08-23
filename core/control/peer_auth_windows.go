//go:build windows

package control

import (
	"fmt"
	"net"

	"golang.org/x/sys/windows"
)

// authorizePeer decides whether the just-accepted named-pipe peer may drive the
// daemon, and whether it holds the daemon's own authority (see peerPrivileged).
// It resolves the connecting process's user SID and administrative membership
// and runs the shared policies against the console user's SID (see peer_auth.go
// for the trust rationale).
//
// A conn whose peer cannot be identified is REFUSED. The production listener
// only ever yields winio pipe conns, whose client process is always resolvable,
// so an unidentifiable peer means the lookup itself failed — and an identity the
// daemon cannot establish must not be granted a channel that drives LocalSystem.
func (d *Daemon) authorizePeer(conn net.Conn) (allowed, privileged bool) {
	sid, admin, ok := pipePeerIdentity(conn)
	if !ok {
		d.emitLog(LogWarn, "control: cannot identify the peer on this connection; refusing it")
		return false, false
	}
	self, err := currentUserSID()
	if err != nil {
		// An unknown self just means the self-shortcut can't fire; the console
		// check below still governs. Empty never matches a real peer SID.
		self = ""
	}
	if !peerAllowed(sid, self, consoleUserSID, func(msg string) {
		d.emitLog(LogWarn, msg)
	}) {
		return false, false
	}
	return true, peerPrivileged(sid, self, admin)
}

// pipePeerIdentity resolves the user SID of the process on the client end of the
// named pipe, and whether that process's token carries administrative rights.
// The accepted winio conn exposes the pipe handle through an Fd() uintptr
// method; GetNamedPipeClientProcessId turns that into the client pid, whose
// token answers both questions. ok=false for any conn that isn't a winio pipe
// (a test in-memory conn) or when the lookup fails, so the caller refuses.
func pipePeerIdentity(conn net.Conn) (sid string, admin, ok bool) {
	fder, isPipe := conn.(interface{ Fd() uintptr })
	if !isPipe {
		return "", false, false
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(fder.Fd()), &pid); err != nil {
		return "", false, false
	}
	return processIdentity(pid)
}

// processIdentity opens the process by pid (with the minimal
// QUERY_LIMITED_INFORMATION right so it works across integrity levels) and reads
// its token user SID plus its administrative membership. Both come from the one
// token handle: asking twice could straddle a token change and answer about two
// different callers.
func processIdentity(pid uint32) (sid string, admin, ok bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", false, false
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return "", false, false
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", false, false
	}
	return tu.User.Sid.String(), tokenIsAdmin(tok), true
}

// tokenIsAdmin reports whether tok carries BUILTIN\Administrators as an ENABLED
// group.
//
// The group list is walked directly rather than asking CheckTokenMembership,
// for two reasons. CheckTokenMembership wants an impersonation token, which
// would mean duplicating a token opened from someone else's process; and the
// attribute check is exactly the distinction that matters here. A UAC-filtered
// token — what every non-elevated process of an administrator runs with — still
// LISTS Administrators, but marks it SE_GROUP_USE_FOR_DENY_ONLY with
// SE_GROUP_ENABLED cleared. Treating that as administrative would hand the
// service's authority to any process the user launched by double-clicking it,
// which is the escalation this check exists to stop; the elevated half of the
// same account, obtained through the UAC prompt, has the group enabled and
// passes.
//
// A token whose groups can't be read is not administrative as far as this
// answers: the caller then falls back on the peer==self shortcut, which is
// decided from identities that were read successfully.
func tokenIsAdmin(tok windows.Token) bool {
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	groups, err := tok.GetTokenGroups()
	if err != nil {
		return false
	}
	return groupEnabled(groups.AllGroups(), admins)
}

// groupEnabled reports whether want appears in groups as an ENABLED membership.
// It is the whole of the deny-only distinction tokenIsAdmin rests on, split out
// so it can be tested against a group list a test builds by hand — a real
// UAC-filtered token cannot be minted inside a test process.
func groupEnabled(groups []windows.SIDAndAttributes, want *windows.SID) bool {
	for _, g := range groups {
		if g.Attributes&windows.SE_GROUP_ENABLED == 0 {
			continue
		}
		if windows.EqualSid(g.Sid, want) {
			return true
		}
	}
	return false
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
// the login screen, or a session mid-transition) leaves the policy to refuse.
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
