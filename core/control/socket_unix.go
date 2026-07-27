//go:build darwin || linux

package control

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// socketDialProbe bounds the "is a live core already here?" dial in ListenSocket.
// A local connect() to a bound socket returns at once, and to a dead or non-
// socket path it fails at once (ECONNREFUSED / ENOTSOCK), so this only caps a
// pathological stall rather than being hit in the normal case.
const socketDialProbe = 2 * time.Second

// ListenSocket opens the unix domain socket listener the control protocol is
// served on. path is DefaultSocketPath in production — the per-platform
// well-known path the UI discovers the core by dialling (see socket_darwin.go
// and socket_linux.go); tests pass a path in a temp dir so a run never touches
// the production location. The returned listener plugs into ServeListener, and
// removes the socket file when it is closed (a clean shutdown), so the path is
// free for the next start.
//
// Bring-up is guarded against two hazards a named pipe gets from the OS for
// free but a bind-to-a-filesystem-path does not:
//
//   - A leftover file at path. Unlike the pipe's FILE_FLAG_FIRST_PIPE_INSTANCE,
//     bind(2) refuses a path that already exists with EADDRINUSE whether or not
//     anyone is serving it — so a crash that skipped cleanup would wedge every
//     later start. We disambiguate by dialling first: if something answers,
//     another core owns the tunnel and we must not steal it (refuse loudly, the
//     spirit of the pipe's first-instance claim); if the dial fails the file is
//     stale, so we unlink it and bind. This is best-effort against an honest
//     double-start, not a lock against a hostile racer — two roots racing the
//     unlink is out of scope, as it is for the pipe.
//   - Default permissions. bind honors the umask, which typically leaves the
//     socket rwx only for its owner (root), locking every unprivileged GUI out.
//     We chmod it to 0666 so any local user's GUI can attach — the unix-
//     permission analog of the pipe DACL's generic-read/write grant to
//     Interactive Users. Reaching the socket is not the same as being admitted:
//     every accepted connection is then authenticated by peer credentials
//     (authorizePeer), which narrows the caller set to the console user and the
//     daemon's own account. See docs/control-protocol.md.
func ListenSocket(path string) (net.Listener, error) {
	if _, err := os.Stat(path); err == nil {
		conn, derr := net.DialTimeout("unix", path, socketDialProbe)
		if derr == nil {
			// Someone is serving here right now. Leave their socket alone.
			_ = conn.Close()
			return nil, fmt.Errorf("control: another tenebra-core is already serving on %s", path)
		}
		// The file is there but nothing answers: a crashed core left it behind.
		// Clear it so the bind below can claim the path. A concurrent cleanup
		// that already removed it (the file raced out between the stat and here)
		// is fine — the goal state is "gone", and bind will create it fresh.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("control: remove stale socket %s: %w", path, err)
		}
	}

	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control: resolve %s: %w", path, err)
	}
	l, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("control: listen on %s: %w", path, err)
	}
	// Removing the file on Close is the default for a listener that created it;
	// state it so a clean shutdown is guaranteed to free the path, not left to
	// an implementation detail.
	l.SetUnlinkOnClose(true)

	if err := os.Chmod(path, 0o666); err != nil {
		// A socket only root can reach is useless to the GUI; fail rather than
		// serve a control surface no client can open. Close unlinks the file.
		_ = l.Close()
		return nil, fmt.Errorf("control: chmod %s: %w", path, err)
	}
	return l, nil
}
