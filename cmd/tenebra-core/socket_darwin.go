//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Divaaaan/tenebra/core/control"
)

// rootDataDir is the machine-scoped profile store for the root LaunchDaemon —
// the darwin analog of the Windows service's %ProgramData%\Tenebra\data. It sits
// under /Library/Application Support (the macOS home for machine-wide app data)
// and is locked to root by configureSocketPaths, since profiles carry
// subscription credentials that unprivileged users reach through the socket
// protocol, never the files.
const rootDataDir = "/Library/Application Support/Tenebra/data"

// singboxBinaryName is the bundled sing-box filename on macOS: no extension,
// unlike the Windows sing-box.exe (matching adapters/macos's own resolution).
const singboxBinaryName = "sing-box"

// serveSocket serves the control protocol on the unix domain socket from a
// console or a root LaunchDaemon — the macOS counterpart of servePipe, so the
// socket path can be exercised with `tenebra-core --socket` without installing a
// daemon. The transport can be disabled with TENEBRA_SOCKET=off; asking to serve
// on a disabled socket is a caller error, so report it rather than fall through
// to a stdio a LaunchDaemon does not have.
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
// reads them when the core serves the socket as root — the darwin analog of the
// Windows service's configureServicePaths. A root LaunchDaemon owns the tunnel
// while an unprivileged GUI drives it over the socket, so the per-user defaults
// (which would land in root's own ~/Library) are wrong: the store belongs under
// rootDataDir and the bundled sing-box is resolved from the install layout
// around the executable. Both knobs are the environment variables every mode
// already honours, set process-wide here so the existing readers — configDir,
// the runner, ruleSetDir — pick them up unchanged; an operator-set environment
// still wins, matching the override semantics everywhere else. A non-root
// `--socket` run (the dev path) is left on its per-user paths untouched.
func configureSocketPaths() error {
	return configureSocketPathsFor(os.Geteuid())
}

// configureSocketPathsFor is the euid-injectable core of configureSocketPaths,
// so the non-root no-op can be tested without a privileged process (as
// elevationHintFor is in adapters/macos). Only euid 0 — the LaunchDaemon that
// can open utun — gets the machine-scoped layout; every other euid keeps its
// per-user paths.
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
		if bin := findBundledSingbox(filepath.Dir(exe)); bin != "" {
			// Setting the variable rather than the runner field also lights up
			// ruleSetDir, which probes the directory this path points into.
			os.Setenv("TENEBRA_SINGBOX", bin)
		} else {
			// Not fatal: the runner falls back to sing-box next to the executable
			// and reports precisely, at connect time, if it is missing.
			log.Printf("tenebra-core: no bundled sing-box near %s", exe)
		}
	}
	return nil
}

// findBundledSingbox resolves the sing-box binary from the installed layout
// around the core executable. The macOS app bundles sing-box as a Tauri
// externalBin sidecar, which lands next to the main binary (Contents/MacOS/), as
// does a flat dev checkout; a bundle that instead stages it under Contents/
// Resources is covered by the sibling fallback. Returns "" when neither has it,
// matching the Windows resolver's contract — the runner then falls back to its
// own next-to-executable default and reports a miss at connect time.
func findBundledSingbox(exeDir string) string {
	for _, p := range []string{
		filepath.Join(exeDir, singboxBinaryName),
		filepath.Join(exeDir, "..", "Resources", singboxBinaryName),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// secureDataDir creates dir (with parents) and clamps it to root-owned 0700 —
// the darwin analog of the Windows service's DACL clamp, expressed in unix
// ownership and mode. The clamp is re-applied and then verified on every start,
// not just on creation: /Library/Application Support is world-traversable and a
// non-root user could have pre-created the path, so we chown it back to root and
// strip every group/other bit before the store writes credentials into it. It is
// fail-closed: if the directory cannot be made root-only, or a final stat still
// shows the wrong owner or mode, we return an error rather than scatter
// subscription secrets into a directory another user can read. (chown to root
// needs privilege the daemon has in production; configureSocketPathsFor only
// calls this at euid 0, so an unprivileged run never reaches it.)
func secureDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chown(dir, 0, 0); err != nil {
		return fmt.Errorf("chown %s to root: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if err := verifyRootOnlyDir(info); err != nil {
		return fmt.Errorf("%s is not root-only after clamp: %w", dir, err)
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
