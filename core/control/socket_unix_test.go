//go:build darwin || linux

package control

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// tempSocketPath returns a short unix socket path under /tmp. sun_path is capped
// at 104 bytes on macOS (108 on Linux), and the per-test TMPDIR — under
// /var/folders on macOS — is long enough to blow that once a socket name is
// appended; /tmp is short and always present on both. Never the production
// DefaultSocketPath: a test must not touch the path a live daemon binds.
func tempSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "tnb-sock")
	if err != nil {
		t.Fatalf("mkdir temp socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "t.sock")
}

// socketHarness runs ServeListener over a real unix domain socket, mirroring the
// pipe harness so the shared session semantics (round-trip, takeover, EOF,
// shutdown) are exercised on the unix transport too. The session-behavior
// coverage itself lives in listener_test.go; these tests focus on the socket
// bring-up hygiene ListenSocket adds.
type socketHarness struct {
	t      *testing.T
	path   string
	daemon *Daemon
	runner *fakeRunner
	cancel context.CancelFunc
	done   chan error

	doneOnce sync.Once
	doneErr  error
}

func newSocketHarness(t *testing.T) *socketHarness {
	t.Helper()
	path := tempSocketPath(t)
	l, err := ListenSocket(path)
	if err != nil {
		t.Fatalf("ListenSocket(%s): %v", path, err)
	}
	d, runner := newTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	h := &socketHarness{t: t, path: path, daemon: d, runner: runner, cancel: cancel, done: make(chan error, 1)}
	go func() { h.done <- ServeListener(ctx, d, l) }()
	t.Cleanup(func() {
		cancel()
		h.awaitDone()
	})
	return h
}

// awaitDone waits for ServeListener to return, caching the result so cleanup can
// call it again after a test already has.
func (h *socketHarness) awaitDone() error {
	h.t.Helper()
	h.doneOnce.Do(func() {
		select {
		case h.doneErr = <-h.done:
		case <-time.After(3 * time.Second):
			h.t.Error("ServeListener did not return")
		}
	})
	return h.doneErr
}

// dial connects a client to the socket.
func (h *socketHarness) dial() *lineClient {
	h.t.Helper()
	conn, err := net.DialTimeout("unix", h.path, 3*time.Second)
	if err != nil {
		h.t.Fatalf("dial %s: %v", h.path, err)
	}
	return newLineClient(h.t, conn)
}

// TestSocketRoundTrip: the protocol works end to end over a real unix socket, a
// client sending a request and reading its response.
func TestSocketRoundTrip(t *testing.T) {
	h := newSocketHarness(t)
	c := h.dial()
	c.send(Request{ID: 3, Cmd: CmdStatus})
	r := c.await()
	if r.ID != 3 || !r.Ok {
		t.Fatalf("status response = %+v, want ok with id 3", r)
	}
	var st State
	c.dataInto(r, &st)
	if st.State != StateIdle {
		t.Errorf("initial state = %q, want idle", st.State)
	}
}

// TestSocketPermissions0666: the bound socket is chmod'd to 0666 so any local
// user's unprivileged GUI can attach to what a root daemon bound.
func TestSocketPermissions0666(t *testing.T) {
	path := tempSocketPath(t)
	l, err := ListenSocket(path)
	if err != nil {
		t.Fatalf("ListenSocket: %v", err)
	}
	defer l.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Errorf("bound path is not a socket: mode %v", info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o666 {
		t.Errorf("socket perms = %#o, want 0666 (any local user may attach)", perm)
	}
}

// TestSocketStaleSocketCleaned: a socket file a crashed core left behind (no one
// serving it) is unlinked and rebound rather than wedging every later start.
func TestSocketStaleSocketCleaned(t *testing.T) {
	path := tempSocketPath(t)

	// Simulate the crash: bind, then close without unlinking, leaving a dead
	// socket file behind.
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale socket file missing before test: %v", err)
	}

	// ListenSocket finds nothing answering, clears the stale file, and binds.
	l, err := ListenSocket(path)
	if err != nil {
		t.Fatalf("ListenSocket over stale file: %v", err)
	}
	defer l.Close()

	// The rebound socket is live: a dial connects.
	conn, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		t.Fatalf("dial rebound socket: %v", err)
	}
	_ = conn.Close()
}

// TestSocketLiveNotStolen: a second bring-up on a socket someone is already
// serving is refused — the dial probe answers, so the file is not stale and the
// running core keeps its tunnel. This is the unix stand-in for the pipe's
// first-instance claim (TestPipeNameCannotBeSquatted).
func TestSocketLiveNotStolen(t *testing.T) {
	path := tempSocketPath(t)
	l, err := ListenSocket(path)
	if err != nil {
		t.Fatalf("first ListenSocket: %v", err)
	}
	defer l.Close()

	// The kernel accepts the probe into the live listener's backlog, so the
	// dial succeeds and ListenSocket must decline rather than unlink and steal.
	if l2, err := ListenSocket(path); err == nil {
		_ = l2.Close()
		t.Fatal("second ListenSocket on a live socket succeeded, want refusal")
	}
}

// TestSocketRemovedOnShutdown: a clean shutdown unlinks the socket file so the
// next start finds a free path.
func TestSocketRemovedOnShutdown(t *testing.T) {
	h := newSocketHarness(t)
	if _, err := os.Stat(h.path); err != nil {
		t.Fatalf("socket missing while serving: %v", err)
	}

	h.cancel()
	if err := h.awaitDone(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeListener returned %v, want context.Canceled", err)
	}

	if _, err := os.Stat(h.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket still present after shutdown: stat err = %v, want not-exist", err)
	}
}
