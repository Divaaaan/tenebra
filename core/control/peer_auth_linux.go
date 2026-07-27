//go:build linux

package control

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// seatStateDir is where logind publishes one file per seat — a seat being a set
// of input and display devices one person sits at. consoleUserUID reads the
// active session's uid out of it.
const seatStateDir = "/run/systemd/seats"

// userRuntimeDir is the parent of the per-user runtime directories
// (XDG_RUNTIME_DIR) a session manager creates as /run/user/<uid>. It backs the
// fallback consoleUserUID takes when no seat state is published.
const userRuntimeDir = "/run/user"

// activeSeatUIDKey is the field logind writes into a seat's state file naming
// the uid whose session currently owns that seat's input and display. A seat
// with no active session (nobody logged in at the display) omits it.
const activeSeatUIDKey = "ACTIVE_UID="

// peerCredUID reads the connected peer's uid from a unix-domain socket via
// getsockopt(SOL_SOCKET, SO_PEERCRED), which returns the pid/uid/gid the kernel
// recorded for the process on the other end at connect time. The credentials are
// captured by the kernel, not sent by the peer, so they cannot be forged; a
// process that execs into something else after connecting keeps the identity it
// had when it connected, which is the identity we want to judge. It returns
// ok=false for any conn that is not a live unix socket (e.g. a net.Pipe used in
// tests) or when the getsockopt fails, leaving the caller to take the fail-open
// path.
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

// consoleUserUID reports the uid of the user sitting at the machine, as a
// decimal string — the Linux stand-in for the owner of /dev/console on macOS,
// and the identity the peer policy admits besides the daemon's own account.
//
// Linux has no single file the kernel keeps the console owner in, so this reads
// the session manager's runtime state instead, in two steps (see
// consoleUserUIDIn). Both are deliberately file-based: the daemon must not grow
// a D-Bus client, and an OS lookup that needs a session bus is exactly what a
// root service does not have.
//
// The honest limits of the answer:
//
//   - It depends on logind (systemd-logind, or elogind, which publishes the
//     same runtime layout). A machine running seatd or no session manager at all
//     publishes neither source, so the lookup fails and the policy falls open to
//     the historical any-local-user trust — no worse than the transport had
//     before the check existed, and logged as such by peerAllowed.
//   - The seat files carry a "This is private data. Do not parse." header, and
//     that warning is taken at face value: they are runtime state with no
//     stability guarantee, and this reads them anyway because the sanctioned
//     alternative is a D-Bus call to org.freedesktop.login1, which means a bus
//     client in a root daemon that has no session bus and a dependency the core
//     does not carry. The trade is only acceptable because the failure mode is
//     benign — every parse failure returns an error and the policy falls open,
//     so a format change degrades the check to what it was before it existed
//     rather than misidentifying anyone.
//   - Fast user switching leaves the non-active session's GUI unable to attach
//     until it is switched back to. That matches the macOS behaviour, where
//     /dev/console follows the foreground session.
func consoleUserUID() (string, error) {
	return consoleUserUIDIn(seatStateDir, userRuntimeDir)
}

// consoleUserUIDIn is the injectable core of consoleUserUID: the two runtime
// directories are parameters so the lookup can be tested against fixtures rather
// than the host's live login state. It prefers the seat state, which answers
// "who is at the display right now", and only falls back to the runtime
// directories, which answer the weaker "who has a session at all".
func consoleUserUIDIn(seatDir, runtimeDir string) (string, error) {
	uid, seatErr := activeSeatUID(seatDir)
	if seatErr == nil {
		return uid, nil
	}
	uid, runtimeErr := soleRuntimeDirUID(runtimeDir)
	if runtimeErr == nil {
		return uid, nil
	}
	return "", fmt.Errorf("control: no interactive session found (%v; %v)", seatErr, runtimeErr)
}

// activeSeatUID reads the uid of the session currently active on a physical
// seat. seat0 is the built-in seat — the keyboard and display attached to the
// machine — which is the one every ordinary desktop logs into, so it is tried
// first by name. A multi-seat host (or a distro that names its seats otherwise)
// falls through to scanning the directory, and refuses to answer when two seats
// name different users: guessing which of two people at one machine owns the
// tunnel is exactly the decision that must not be made silently, and an error
// here means the policy falls open with a warning instead.
func activeSeatUID(seatDir string) (string, error) {
	if uid, err := seatFileUID(filepath.Join(seatDir, "seat0")); err == nil {
		return uid, nil
	}
	entries, err := os.ReadDir(seatDir)
	if err != nil {
		return "", err
	}
	found := ""
	for _, e := range entries {
		uid, err := seatFileUID(filepath.Join(seatDir, e.Name()))
		if err != nil {
			continue
		}
		if found != "" && found != uid {
			return "", fmt.Errorf("control: seats under %s report different active users", seatDir)
		}
		found = uid
	}
	if found == "" {
		return "", fmt.Errorf("control: no seat under %s has an active session", seatDir)
	}
	return found, nil
}

// seatFileUID pulls ACTIVE_UID out of one seat state file. The file is a flat
// list of KEY=VALUE lines; anything else in it is ignored. A file without the
// key describes a seat nobody is logged into, which is an error here rather than
// an empty answer, so the caller keeps looking (and ultimately falls open) —
// never treats "nobody" as an identity that could match a peer.
func seatFileUID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		value, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), activeSeatUIDKey)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		// Only a well-formed uid is an answer: a mangled value must not be
		// compared against a peer uid, and returning it as an error keeps the
		// policy on its fail-open path rather than silently denying everyone.
		if _, err := strconv.ParseUint(value, 10, 32); err != nil {
			return "", fmt.Errorf("control: %s has a malformed active uid %q", path, value)
		}
		return value, nil
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("control: %s names no active session", path)
}

// soleRuntimeDirUID names the owner of the one per-user runtime directory under
// runtimeDir, or fails. It is the fallback for hosts that keep /run/user (every
// pam_systemd or elogind login does) but publish no seat state — a headless-ish
// or unusually configured desktop — and it is deliberately weaker than the seat
// lookup: a runtime directory means "this user has a session somewhere", not
// "this user is at the display", and it lingers for a user with logind linger
// enabled. So it only answers when the answer is unambiguous; two candidates
// mean the caller falls open rather than picking one.
//
// The directories are created by the session manager with runtimeDir itself
// root-owned and 0755, so an unprivileged user cannot plant an entry here to
// nominate themselves as the console user. Ownership is read from the directory
// inode rather than parsed out of its name for the same reason: the name is a
// label, the owner is what the kernel recorded.
//
// The daemon's own account (root) is skipped. A root shell session — an ssh
// login, a `machinectl shell` — creates /run/user/0 and would otherwise make
// every lookup ambiguous on an administered machine, while root is already
// admitted by peerAllowed's self shortcut, so it is never the answer we need.
func soleRuntimeDirUID(runtimeDir string) (string, error) {
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return "", err
	}
	found := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		if st.Uid == 0 {
			continue
		}
		uid := strconv.FormatUint(uint64(st.Uid), 10)
		if found != "" && found != uid {
			return "", fmt.Errorf("control: %s holds runtime directories for several users", runtimeDir)
		}
		found = uid
	}
	if found == "" {
		return "", fmt.Errorf("control: no user runtime directory under %s", runtimeDir)
	}
	return found, nil
}
