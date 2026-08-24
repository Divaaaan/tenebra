package control

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// escalationBundle builds the archive the local privilege escalation was carried
// in: the two members zapret.Install checks for, and nothing else. One .bat —
// which start_zapret runs through cmd.exe, as LocalSystem in a service install —
// and a one-byte bin/winws.exe to satisfy the "is this really a zapret bundle?"
// test. Neither file's contents are looked at anywhere, which is the point: the
// only thing standing between an unprivileged local user and SYSTEM is who is
// allowed to send this.
func escalationBundle(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"pwn.bat":       "@echo off\r\nnet user attacker P@ssw0rd /add\r\n",
		"bin/winws.exe": "x",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// zapretDirOf is where an imported bundle lands for this daemon.
func zapretDirOf(d *Daemon) string {
	return filepath.Join(d.store.Dir(), zapretDirName)
}

// wantAdminRefusal asserts a response is the privilege refusal and not some
// other failure that happens to be an error. The distinction matters: a handler
// that ran and failed for its own reasons would leave this test passing while
// the gate did nothing.
func wantAdminRefusal(t *testing.T, r Response, cmd string) {
	t.Helper()
	if r.Ok {
		t.Fatalf("%s from an unprivileged peer succeeded; it must be refused", cmd)
	}
	if !strings.Contains(r.Error, "права администратора") {
		t.Fatalf("%s refused with %q, want a message naming the missing administrator rights", cmd, r.Error)
	}
}

// TestUnprivilegedPeerCannotImportZapret is the escalation itself, over the
// transport it ran on: an ordinary local user, admitted to the control channel
// (the pipe DACL grants INTERACTIVE by design), sends the two-file archive and
// gets a refusal instead of a bundle installed into the daemon's directory —
// the directory a service install clamps to SYSTEM+Administrators precisely so
// this cannot happen.
func TestUnprivilegedPeerCannotImportZapret(t *testing.T) {
	h := newListenerHarness(t)
	h.setPeerVerdict(true, false)

	c := h.dial()
	c.send(Request{ID: 1, Cmd: CmdImportZapret, Name: "zapret-1.0.zip", Data: escalationBundle(t)})
	wantAdminRefusal(t, c.await(), CmdImportZapret)

	if _, err := os.Stat(zapretDirOf(h.daemon)); !os.IsNotExist(err) {
		t.Fatalf("the refused import still created %s (err=%v); nothing may be written on a denied command", zapretDirOf(h.daemon), err)
	}
}

// TestUnprivilegedPeerCannotImportZapretFromPath covers the other half of the
// same command. import_zapret also takes a filesystem path (Tauri's drag-drop
// hands over real paths, which is the only way a dropped folder can be taken),
// and a caller who can name a directory they control needs no archive at all.
func TestUnprivilegedPeerCannotImportZapretFromPath(t *testing.T) {
	h := newListenerHarness(t)
	h.setPeerVerdict(true, false)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "pwn.bat"), []byte("@echo off\r\n"), 0o600); err != nil {
		t.Fatalf("write bat: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, "bin"), 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "bin", "winws.exe"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write winws: %v", err)
	}

	c := h.dial()
	c.send(Request{ID: 1, Cmd: CmdImportZapret, Path: src})
	wantAdminRefusal(t, c.await(), CmdImportZapret)

	if _, err := os.Stat(zapretDirOf(h.daemon)); !os.IsNotExist(err) {
		t.Fatalf("the refused path import still created %s", zapretDirOf(h.daemon))
	}
}

// TestUnprivilegedPeerCannotRunZapretCommands: the commands that EXECUTE code
// from the daemon's directory are refused for the same peer. start_zapret and
// pick_zapret both end up running a .bat through cmd.exe under the daemon's
// account, so gating the import alone would leave half the primitive open.
func TestUnprivilegedPeerCannotRunZapretCommands(t *testing.T) {
	h := newListenerHarness(t)
	h.setPeerVerdict(true, false)

	c := h.dial()
	for i, cmd := range []string{CmdStartZapret, CmdPickZapret, CmdUpdateZapret} {
		c.send(Request{ID: int64(i + 1), Cmd: cmd, Name: "pwn"})
		wantAdminRefusal(t, c.await(), cmd)
	}
}

// TestUnprivilegedPeerKeepsOrdinaryCommands: the gate is narrow on purpose. The
// unprivileged user is still the person the tunnel belongs to — status, routing
// and connect are their product — so only the commands that hand the daemon code
// are closed to them.
func TestUnprivilegedPeerKeepsOrdinaryCommands(t *testing.T) {
	h := newListenerHarness(t)
	h.setPeerVerdict(true, false)

	c := h.dial()
	c.send(Request{ID: 1, Cmd: CmdStatus})
	if r := c.await(); !r.Ok {
		t.Fatalf("status refused for an unprivileged peer: %s", r.Error)
	}
	c.send(Request{ID: 2, Cmd: CmdSetRouting, Mode: "global"})
	if r := c.await(); !r.Ok {
		t.Fatalf("set_routing refused for an unprivileged peer: %s", r.Error)
	}
	c.send(Request{ID: 3, Cmd: CmdStopZapret})
	if r := c.await(); !r.Ok {
		t.Fatalf("stop_zapret refused for an unprivileged peer: %s", r.Error)
	}
}

// TestPrivilegedPeerImportsZapret is the other side of the gate, and the
// scenario the fix must not break: the core running as an ordinary process of
// its own user (or an administrator talking to the service) still installs a
// bundle. Without this the gate would be indistinguishable from removing the
// feature.
func TestPrivilegedPeerImportsZapret(t *testing.T) {
	h := newListenerHarness(t) // default verdict: admitted and privileged

	c := h.dial()
	c.send(Request{ID: 1, Cmd: CmdImportZapret, Name: "zapret-1.0.zip", Data: escalationBundle(t)})
	r := c.await()
	if !r.Ok {
		t.Fatalf("import_zapret refused for a privileged peer: %s", r.Error)
	}
	var got struct {
		Dir        string   `json:"dir"`
		Strategies []string `json:"strategies"`
	}
	c.dataInto(r, &got)
	if len(got.Strategies) != 1 || got.Strategies[0] != "pwn" {
		t.Fatalf("installed strategies = %v, want the one strategy in the bundle", got.Strategies)
	}
	if _, err := os.Stat(filepath.Join(zapretDirOf(h.daemon), "pwn.bat")); err != nil {
		t.Fatalf("the bundle was reported installed but %s is missing: %v", filepath.Join(zapretDirOf(h.daemon), "pwn.bat"), err)
	}
}

// TestRejectedPeerNeverReachesTheDaemon: a peer the check refuses outright — an
// unidentifiable caller, which is now a denial rather than a fail-open — gets
// its connection closed without a single request being served, and crucially
// does NOT displace the session already in progress. Otherwise the refusal would
// itself be a denial of service: connect, be rejected, and take the real GUI's
// session down on the way out.
func TestRejectedPeerNeverReachesTheDaemon(t *testing.T) {
	h := newListenerHarness(t)

	good := h.dial()
	good.send(Request{ID: 1, Cmd: CmdStatus})
	if r := good.await(); !r.Ok {
		t.Fatalf("status on the authorized session: %s", r.Error)
	}

	h.setPeerVerdict(false, false)
	bad := h.dial()
	// The write is expected to fail once the refusal has closed the stream; a
	// write that still lands is the more interesting case, because then the
	// silence below is the daemon never having seen the request rather than the
	// transport swallowing it.
	if line, err := marshalLine(Request{ID: 2, Cmd: CmdStatus}); err == nil {
		_, _ = bad.conn.Write(line)
	}
	bad.awaitGone()
	select {
	case r := <-bad.resp:
		t.Fatalf("a rejected peer got a response: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}

	// The authorized session is untouched and still serving.
	good.send(Request{ID: 3, Cmd: CmdStatus})
	if r := good.await(); !r.Ok || r.ID != 3 {
		t.Fatalf("the authorized session did not survive a rejected peer: %+v", r)
	}
}

// TestAdminOnlyCommandsAreTheCodeCarryingOnes pins the gated set. Each entry
// either writes into the daemon's trusted directory or runs what is in it; the
// listed exclusions are commands that do neither, so widening this set by
// accident (or narrowing it) is a deliberate edit with a test to argue with.
func TestAdminOnlyCommandsAreTheCodeCarryingOnes(t *testing.T) {
	for _, cmd := range []string{CmdImportZapret, CmdPickZapret, CmdStartZapret, CmdUpdateZapret} {
		if !requiresAdminPeer(cmd) {
			t.Errorf("%s places or runs code in the daemon's directory and must require an administrative peer", cmd)
		}
	}
	for _, cmd := range []string{CmdStatus, CmdConnect, CmdDisconnect, CmdStopZapret, CmdListZapret, CmdSetZapretAutoUpdate, CmdSetRouting} {
		if requiresAdminPeer(cmd) {
			t.Errorf("%s neither places nor runs code; gating it only takes the tunnel away from its owner", cmd)
		}
	}
}

// TestPeerPrivilegeContextRoundTrip: an unstamped context is the in-process
// caller (the daemon's own background work, and unit tests calling Handle
// directly), which carries the daemon's authority; a stamped one answers what
// the transport decided. Both transports that can introduce an outside caller
// stamp it before dispatching anything.
func TestPeerPrivilegeContextRoundTrip(t *testing.T) {
	if !peerPrivilegeFrom(context.Background()) {
		t.Error("an unstamped context is an in-process call and must carry the daemon's authority")
	}
	if !peerPrivilegeFrom(withPeerPrivilege(context.Background(), true)) {
		t.Error("a privileged stamp must read back as privileged")
	}
	if peerPrivilegeFrom(withPeerPrivilege(context.Background(), false)) {
		t.Error("an unprivileged stamp must read back as unprivileged")
	}
}

// TestHandleGatesOnTheContextNotTheHandler proves the refusal happens before
// dispatch: the daemon has no bundle installed, so an ungated start_zapret would
// fail with the handler's own "load a bundle first" message. Getting the
// privilege message instead is what shows the gate is in front of the switch,
// where a newly added command cannot slip past it.
func TestHandleGatesOnTheContextNotTheHandler(t *testing.T) {
	d, _ := newTestDaemon(t)

	r := d.Handle(withPeerPrivilege(context.Background(), false), Request{ID: 1, Cmd: CmdStartZapret})
	wantAdminRefusal(t, r, CmdStartZapret)

	r = d.Handle(withPeerPrivilege(context.Background(), true), Request{ID: 2, Cmd: CmdStartZapret})
	if r.Ok {
		t.Fatal("start_zapret with no bundle installed should have failed in the handler")
	}
	if strings.Contains(r.Error, "права администратора") {
		t.Fatalf("a privileged peer was refused by the gate: %s", r.Error)
	}
}
