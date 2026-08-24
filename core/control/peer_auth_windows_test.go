//go:build windows

package control

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// TestAuthorizePeerRefusesUnidentifiableConn: a conn whose client process cannot
// be resolved — no pipe handle to ask about — is REFUSED. Production's listener
// only ever yields winio pipes, so this is the lookup having failed, and the
// channel it guards drives LocalSystem: an unidentified caller must not be acted
// for. This is the fail-open that made the escalation reachable.
func TestAuthorizePeerRefusesUnidentifiableConn(t *testing.T) {
	d, _ := newTestDaemon(t)

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	allowed, privileged := d.authorizePeer(a)
	if allowed {
		t.Error("authorizePeer must refuse a conn whose peer cannot be identified")
	}
	if privileged {
		t.Error("a refused peer must never be reported as privileged")
	}
}

// TestAuthorizePeerAllowsSelfOverPipe drives the full Windows path over a real
// named pipe: the peer is this test process, which is also the "daemon", so the
// self shortcut admits it whatever the console lookup says — and reports it as
// holding the daemon's authority, because there is no privilege boundary between
// a process and itself. That is the non-service install, where importing a
// bypass bundle must keep working with no elevation at all.
func TestAuthorizePeerAllowsSelfOverPipe(t *testing.T) {
	requirePipeAccess(t)
	d, _ := newTestDaemon(t)

	name := testPipeName()
	l, err := ListenPipe(name)
	if err != nil {
		t.Fatalf("ListenPipe(%s): %v", name, err)
	}
	defer l.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := l.Accept()
		accepted <- c
	}()
	timeout := 3 * time.Second
	client, err := winio.DialPipe(name, &timeout)
	if err != nil {
		t.Fatalf("DialPipe(%s): %v", name, err)
	}
	defer client.Close()

	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("accept timed out")
	}
	if server == nil {
		t.Fatal("accept failed")
	}
	defer server.Close()

	allowed, privileged := d.authorizePeer(server)
	if !allowed {
		t.Error("authorizePeer denied a same-account peer over a real pipe; the GUI's own account must attach")
	}
	if !privileged {
		t.Error("a same-account peer must hold the daemon's authority; the non-service install imports bundles")
	}
}

// TestProcessIdentityReadsOwnSID pins the syscall chain the policy identifies a
// pipe client with: opening a process by pid and reading its token user must
// yield this process's own SID.
func TestProcessIdentityReadsOwnSID(t *testing.T) {
	sid, _, ok := processIdentity(uint32(os.Getpid()))
	if !ok {
		t.Fatal("processIdentity could not read this process's own token")
	}
	want, err := currentUserSID()
	if err != nil {
		t.Fatalf("currentUserSID: %v", err)
	}
	if sid != want {
		t.Errorf("processIdentity SID = %q, want %q (this process)", sid, want)
	}
}

// TestGroupEnabledIgnoresDenyOnlyMembership is the UAC-filtered token, stated as
// the group list Windows hands out for one: Administrators is PRESENT but
// marked deny-only, with SE_GROUP_ENABLED cleared. Reading that as "this caller
// is an administrator" is exactly what would let any double-clicked program of
// an admin user install code the LocalSystem service runs — the prompt UAC
// exists to show, skipped. The elevated half of the same account carries the
// group enabled and must pass.
func TestGroupEnabledIgnoresDenyOnlyMembership(t *testing.T) {
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid: %v", err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatalf("CreateWellKnownSid: %v", err)
	}

	filtered := []windows.SIDAndAttributes{
		{Sid: users, Attributes: windows.SE_GROUP_ENABLED | windows.SE_GROUP_ENABLED_BY_DEFAULT},
		{Sid: admins, Attributes: windows.SE_GROUP_USE_FOR_DENY_ONLY},
	}
	if groupEnabled(filtered, admins) {
		t.Error("a deny-only Administrators membership (the UAC-filtered token) must not read as administrative")
	}

	elevated := []windows.SIDAndAttributes{
		{Sid: users, Attributes: windows.SE_GROUP_ENABLED},
		{Sid: admins, Attributes: windows.SE_GROUP_ENABLED | windows.SE_GROUP_ENABLED_BY_DEFAULT},
	}
	if !groupEnabled(elevated, admins) {
		t.Error("an enabled Administrators membership (the elevated token) must read as administrative")
	}

	if groupEnabled([]windows.SIDAndAttributes{{Sid: users, Attributes: windows.SE_GROUP_ENABLED}}, admins) {
		t.Error("a token with no Administrators membership at all must not read as administrative")
	}
}
