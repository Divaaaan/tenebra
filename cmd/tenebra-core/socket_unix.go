//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Divaaaan/tenebra/core/control"
)

// serveSocket serves the control protocol on the unix domain socket, from a
// console or from the privileged daemon that owns the tunnel (a LaunchDaemon on
// macOS, a system service on Linux) — the unix counterpart of servePipe, so the
// socket path can be exercised with `tenebra-core --socket` without installing a
// daemon. The transport can be disabled with TENEBRA_SOCKET=off; asking to serve
// on a disabled socket is a caller error, so report it rather than fall through
// to a stdio a detached daemon does not have.
func serveSocket(ctx context.Context, d *control.Daemon) error {
	path, enabled := socketPath()
	if !enabled {
		return errors.New("--socket: TENEBRA_SOCKET=off disables the unix-socket transport; unset it or give a path to serve")
	}
	l, err := control.ListenSocket(path)
	if err != nil {
		return err
	}
	log.Printf("tenebra-core: serving the control protocol on %s", path)
	return control.ServeListener(ctx, d, l)
}

// socketPath resolves the unix domain socket the protocol is served on and
// whether the socket transport is enabled at all. It mirrors the desktop UI's
// backend::pipe::configured_name so the two ends agree: TENEBRA_SOCKET unset or
// empty selects the well-known DefaultSocketPath, `off`/`0` disables the
// transport, and any other value names an alternate path (a test points it at a
// temp dir; an operator can relocate it). The pure mapping is split into
// socketPathFrom so it can be tested without touching the environment.
func socketPath() (path string, enabled bool) {
	return socketPathFrom(os.Getenv("TENEBRA_SOCKET"))
}

func socketPathFrom(value string) (path string, enabled bool) {
	switch value {
	case "":
		return control.DefaultSocketPath, true
	case "off", "0":
		return "", false
	default:
		return value, true
	}
}

// configureSocketPaths pins the machine-scoped locations before buildDaemon
// reads them when the core serves the socket as root — the unix analog of the
// Windows service's configureServicePaths. A root daemon owns the tunnel while
// an unprivileged GUI drives it over the socket, so the per-user defaults (which
// would land in root's own home) are wrong: the store belongs under rootDataDir
// and the bundled sing-box is resolved from the install layout around the
// executable. Both knobs are the environment variables every mode already
// honours, set process-wide here so the existing readers — configDir, the
// runner, ruleSetDir — pick them up unchanged; an operator-set environment still
// wins, matching the override semantics everywhere else. A non-root `--socket`
// run (the dev path) is left on its per-user paths untouched.
func configureSocketPaths() error {
	return configureSocketPathsFor(os.Geteuid())
}

// configureSocketPathsFor is the euid-injectable core of configureSocketPaths,
// so the non-root no-op can be tested without a privileged process (as
// elevationHintFor is in the platform adapters). Only euid 0 — the daemon that
// can open the tun device — gets the machine-scoped layout; every other euid
// keeps its per-user paths.
func configureSocketPathsFor(euid int) error {
	if euid != 0 {
		return nil
	}
	if os.Getenv("TENEBRA_CONFIG_DIR") == "" {
		if err := secureDataDir(rootDataDir); err != nil {
			return fmt.Errorf("socket: prepare data dir %s: %w", rootDataDir, err)
		}
		os.Setenv("TENEBRA_CONFIG_DIR", rootDataDir)
	}
	if os.Getenv("TENEBRA_SINGBOX") == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("socket: locate executable: %w", err)
		}
		exeDir := filepath.Dir(exe)
		if bin := findSingbox(exeDir); bin != "" {
			// Which of the candidate locations answered decides which sing-box
			// runs and, through ruleSetDir, whether the rule-sets load from disk
			// or are downloaded at every connect. Both are worth naming in the
			// log: on a machine that can hold several copies (one shipped with
			// Tenebra, one from the distribution's own package) "which one" is
			// otherwise unanswerable after the fact.
			log.Printf("tenebra-core: sing-box at %s", bin)
			// Setting the variable rather than the runner field also lights up
			// ruleSetDir, which probes the directory this path points into.
			os.Setenv("TENEBRA_SINGBOX", bin)
		} else {
			// Not fatal: the runner repeats the same search and reports
			// precisely, at connect time, if it still comes up empty.
			log.Printf("tenebra-core: no sing-box in any of: %s", strings.Join(singboxCandidates(exeDir), ", "))
		}
	}
	return nil
}

// findSingbox resolves the sing-box binary from the installed layout around the
// core executable, trying the platform's candidate locations in order
// (singboxCandidates). Returns "" when none of them has it, matching the Windows
// resolver's contract — the runner then falls back to its own default and
// reports a miss at connect time.
func findSingbox(exeDir string) string {
	for _, p := range singboxCandidates(exeDir) {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// secureDataDir creates dir (with parents) and clamps it to root-owned 0700 —
// the unix analog of the Windows service's DACL clamp, expressed in ownership
// and mode. The clamp is re-applied and then verified on every start, not just
// on creation: the parents of the machine data directory are world-traversable
// and a non-root user could have pre-created the path, so we chown it back to
// root and strip every group/other bit before the store writes credentials into
// it. It is fail-closed: if the directory cannot be made root-only, or a final
// stat still shows the wrong owner or mode, we return an error rather than
// scatter subscription secrets into a directory another user can read. (chown to
// root needs privilege the daemon has in production; configureSocketPathsFor
// only calls this at euid 0, so an unprivileged run never reaches it.)
//
// The clamp is symlink-safe. The world-traversable parent means a non-root user
// could pre-plant a symlink at the data-dir path; a chown/chmod/stat by name
// would then follow it and clamp — and verify — the link's target, not the
// directory we go on to write into, so the anti-squat defence would bless an
// attacker-controlled location. So the path is opened once with O_NOFOLLOW (a
// final symlink makes the open fail outright) and every subsequent step acts on
// that file descriptor: fchown/fchmod/fstat cannot be redirected by a symlink
// swapped in after the open, closing the squat-and-TOCTOU window together.
func secureDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Re-open the directory itself, refusing to traverse a final symlink. If the
	// path was pre-planted as a link the open fails here, before any privileged
	// clamp touches the target; MkdirAll above is a no-op on an existing entry,
	// so it neither creates through nor sanitises such a link — this open is what
	// rejects it.
	f, err := openDirNoFollow(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return clampRootOnlyDir(dir, f)
}

// openDirNoFollow opens dir for the ownership clamp without following a final
// symlink. O_DIRECTORY additionally fails the open if the (real) target is not a
// directory, so the descriptor handed to clampRootOnlyDir is always a genuine
// directory we can fchown/fchmod/fstat.
func openDirNoFollow(dir string) (*os.File, error) {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s without following symlinks: %w", dir, err)
	}
	return f, nil
}

// dirHandle is the slice of *os.File clampRootOnlyDir needs: the descriptor-bound
// chown/chmod/stat that make the clamp immune to a symlink swapped in after the
// open. It is an interface so the fail-closed logic can be exercised without a
// real root-owned directory — fchown(0,0) needs privilege the unit tests lack —
// by substituting a fake that reports the ownership and mode the check must
// accept or reject.
type dirHandle interface {
	Chown(uid, gid int) error
	Chmod(mode os.FileMode) error
	Stat() (os.FileInfo, error)
}

// clampRootOnlyDir forces the opened directory to root-owned 0700 through its
// descriptor and then reads it back to verify, the fail-closed core shared by
// the live path and the tests. name is only for error context. Every operation
// binds to the handle, never to the path, so no symlink race can redirect them.
func clampRootOnlyDir(name string, h dirHandle) error {
	if err := h.Chown(0, 0); err != nil {
		return fmt.Errorf("chown %s to root: %w", name, err)
	}
	if err := h.Chmod(0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", name, err)
	}
	info, err := h.Stat()
	if err != nil {
		return err
	}
	if err := verifyRootOnlyDir(info); err != nil {
		return fmt.Errorf("%s is not root-only after clamp: %w", name, err)
	}
	return nil
}

// verifyRootOnlyDir is the fail-closed check secureDataDir ends on: dir must be
// a directory, owned by root (uid 0), with no group or other access. It is the
// pure predicate over a stat result so the rejection paths can be tested without
// a root-owned directory (a non-root test dir trips the ownership check; a 0777
// dir trips the mode check).
func verifyRootOnlyDir(info os.FileInfo) error {
	if !info.IsDir() {
		return errors.New("not a directory")
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("mode %#o grants group/other access", perm)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot read ownership")
	}
	if st.Uid != 0 {
		return fmt.Errorf("owned by uid %d, not root", st.Uid)
	}
	return nil
}
