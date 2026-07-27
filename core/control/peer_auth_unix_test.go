//go:build darwin || linux

package control

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// shortSocketPath returns a unix-socket path short enough to satisfy the
// sun_path limit (~104 bytes on macOS, 108 on Linux) — t.TempDir() embeds the
// (long) test name and can overflow it, so a bind there fails with EINVAL. The
// dir is cleaned up with the test.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tnb")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// TestPeerCredUIDOverUnixSocket drives the real peer-credential read — the
// platform's own syscall, LOCAL_PEERCRED on macOS and SO_PEERCRED on Linux: it binds a
// unix socket, dials it, and asserts peerCredUID reports the connecting
// process's uid — which, for a client dialled from this same test process, is
// our own uid. This is the syscall the production policy relies on to identify a
// control-socket peer.
func TestPeerCredUIDOverUnixSocket(t *testing.T) {
	sock := shortSocketPath(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer l.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()

	client, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial unix: %v", err)
	}
	defer client.Close()

	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("accept timed out")
	}
	if server == nil {
		t.Fatal("accept failed")
	}
	defer server.Close()

	uid, ok := peerCredUID(server)
	if !ok {
		t.Fatal("peerCredUID could not read the peer uid over a real unix socket")
	}
	if want := os.Getuid(); uid != want {
		t.Errorf("peer uid = %d, want %d (this process)", uid, want)
	}
}

// TestAuthorizePeerAllowsSelfOverUnixSocket: a peer running as the daemon's own
// account (here the test process == the daemon, same uid) is admitted by the
// self shortcut, independent of who the console user is. This exercises the full
// authorizePeer path over a real socket.
func TestAuthorizePeerAllowsSelfOverUnixSocket(t *testing.T) {
	d, _ := newTestDaemon(t)

	sock := shortSocketPath(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer l.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := l.Accept()
		accepted <- c
	}()
	client, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial unix: %v", err)
	}
	defer client.Close()
	server := <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	defer server.Close()

	if !d.authorizePeer(server) {
		t.Errorf("authorizePeer denied a same-account (uid %s) peer; the GUI's own account must attach", strconv.Itoa(os.Getuid()))
	}
}

// TestAuthorizePeerFailsOpenOnNonUnixConn: a conn we can't read a uid from (an
// in-memory pipe, as the listener tests use) is allowed — the transport-unknown
// fail-open path — so the policy never bricks a connection it can't identify.
func TestAuthorizePeerFailsOpenOnNonUnixConn(t *testing.T) {
	d, _ := newTestDaemon(t)

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	if !d.authorizePeer(a) {
		t.Error("authorizePeer must fail open on a conn whose peer uid can't be read")
	}
}
