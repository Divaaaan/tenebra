//go:build darwin || linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/control"
)

// TestSocketPathFrom pins the env mapping the socket transport shares with the
// desktop UI's configured_name: unset/empty is the well-known default, `off`/`0`
// disables the transport, and any other value names an alternate path.
func TestSocketPathFrom(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		wantPath    string
		wantEnabled bool
	}{
		{"unset or empty", "", control.DefaultSocketPath, true},
		{"off disables", "off", "", false},
		{"zero disables", "0", "", false},
		{"custom path", "/tmp/tenebra-alt.sock", "/tmp/tenebra-alt.sock", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, gotEnabled := socketPathFrom(tc.value)
			if gotPath != tc.wantPath || gotEnabled != tc.wantEnabled {
				t.Errorf("socketPathFrom(%q) = (%q, %v), want (%q, %v)",
					tc.value, gotPath, gotEnabled, tc.wantPath, tc.wantEnabled)
			}
		})
	}
}

// TestServeSocketDisabled: asking to serve while TENEBRA_SOCKET=off is a caller
// error, not a silent stdio fallback (a LaunchDaemon has no stdio). It must
// refuse before touching the daemon, so a nil daemon is safe here.
func TestServeSocketDisabled(t *testing.T) {
	t.Setenv("TENEBRA_SOCKET", "off")
	if err := serveSocket(context.Background(), nil); err == nil {
		t.Fatal("serveSocket with TENEBRA_SOCKET=off returned nil, want refusal")
	}
}

// TestConfigureSocketPathsNonRootNoOp: a non-root --socket run (the dev path)
// keeps its per-user paths — the machine-scoped store and sing-box pinning is
// for the root LaunchDaemon only. The euid is injected so the no-op is asserted
// regardless of who runs the test, the way elevationHintFor is exercised.
func TestConfigureSocketPathsNonRootNoOp(t *testing.T) {
	t.Setenv("TENEBRA_CONFIG_DIR", "")
	t.Setenv("TENEBRA_SINGBOX", "")

	if err := configureSocketPathsFor(501); err != nil {
		t.Fatalf("configureSocketPathsFor(non-root) = %v, want nil", err)
	}
	if got := os.Getenv("TENEBRA_CONFIG_DIR"); got != "" {
		t.Errorf("non-root run set TENEBRA_CONFIG_DIR = %q, want untouched", got)
	}
	if got := os.Getenv("TENEBRA_SINGBOX"); got != "" {
		t.Errorf("non-root run set TENEBRA_SINGBOX = %q, want untouched", got)
	}
}

// TestSecureDataDirRejectsSymlink proves the anti-squat clamp is symlink-safe: a
// symlink pre-planted at the data-dir path is refused instead of being followed
// so the clamp lands on the link's target. This runs unprivileged — the
// O_NOFOLLOW open fails before any chown to root is attempted, so the rejection
// does not depend on being able to complete the clamp.
func TestSecureDataDirRejectsSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "data")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := secureDataDir(link); err == nil {
		t.Fatal("secureDataDir followed a pre-planted symlink, want refusal")
	}
	// The target must be left as it was — the clamp must not have chmod'd it
	// through the link (it started 0700; a dangling clamp attempt would not
	// change owner without privilege, but mode is observable here).
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("symlink target mode changed to %#o; clamp followed the link", perm)
	}
}

// TestClampRootOnlyDir exercises the descriptor-bound fail-closed core against a
// fake handle, so both the accepting and the refusing paths are covered without
// a real root-owned directory (fchown to root needs privilege the test lacks).
func TestClampRootOnlyDir(t *testing.T) {
	t.Run("root-owned 0700 passes", func(t *testing.T) {
		h := &fakeDirHandle{info: fakeDirInfo{mode: os.ModeDir | 0o700, uid: 0}}
		if err := clampRootOnlyDir("data", h); err != nil {
			t.Fatalf("clampRootOnlyDir on a root-owned 0700 dir = %v, want nil", err)
		}
	})

	t.Run("wrong owner refused", func(t *testing.T) {
		h := &fakeDirHandle{info: fakeDirInfo{mode: os.ModeDir | 0o700, uid: 501}}
		if err := clampRootOnlyDir("data", h); err == nil {
			t.Error("clampRootOnlyDir accepted a non-root-owned dir, want refusal")
		}
	})

	t.Run("wrong mode refused", func(t *testing.T) {
		h := &fakeDirHandle{info: fakeDirInfo{mode: os.ModeDir | 0o777, uid: 0}}
		if err := clampRootOnlyDir("data", h); err == nil {
			t.Error("clampRootOnlyDir accepted a group/other-accessible dir, want refusal")
		}
	})

	t.Run("chown failure is fatal", func(t *testing.T) {
		h := &fakeDirHandle{chownErr: os.ErrPermission, info: fakeDirInfo{mode: os.ModeDir | 0o700, uid: 0}}
		if err := clampRootOnlyDir("data", h); err == nil {
			t.Error("clampRootOnlyDir swallowed a failed chown, want refusal")
		}
	})
}

// fakeDirHandle is a dirHandle whose clamp operations are no-ops (optionally
// erroring) and whose Stat reports a caller-chosen FileInfo, standing in for the
// privileged *os.File so clampRootOnlyDir's verdict can be tested unprivileged.
type fakeDirHandle struct {
	info     os.FileInfo
	chownErr error
	chmodErr error
}

func (h *fakeDirHandle) Chown(uid, gid int) error     { return h.chownErr }
func (h *fakeDirHandle) Chmod(mode os.FileMode) error { return h.chmodErr }
func (h *fakeDirHandle) Stat() (os.FileInfo, error)   { return h.info, nil }

// fakeDirInfo is the minimal os.FileInfo verifyRootOnlyDir reads: IsDir/Mode from
// mode, and a syscall.Stat_t carrying uid behind Sys() for the ownership check.
type fakeDirInfo struct {
	mode os.FileMode
	uid  uint32
}

func (f fakeDirInfo) Name() string       { return "data" }
func (f fakeDirInfo) Size() int64        { return 0 }
func (f fakeDirInfo) Mode() os.FileMode  { return f.mode }
func (f fakeDirInfo) ModTime() time.Time { return time.Time{} }
func (f fakeDirInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeDirInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

// TestVerifyRootOnlyDir exercises the fail-closed predicate secureDataDir ends
// on. The success case (a real root-owned 0700 dir) needs privilege and is
// validated in a live daemon run; here we prove the rejections fire.
func TestVerifyRootOnlyDir(t *testing.T) {
	t.Run("loose mode rejected", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "loose")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		// Mkdir is subject to umask; force the group/other bits on explicitly.
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyRootOnlyDir(info); err == nil {
			t.Error("verifyRootOnlyDir accepted a 0777 dir, want rejection")
		}
	})

	t.Run("regular file rejected", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyRootOnlyDir(info); err == nil {
			t.Error("verifyRootOnlyDir accepted a regular file, want rejection")
		}
	})

	t.Run("non-root owner rejected", func(t *testing.T) {
		// A 0700 dir owned by the test user must fail the ownership check. When
		// the suite runs as root that dir is legitimately root-owned and the
		// check passes, so the assertion only applies to an unprivileged run.
		if os.Geteuid() == 0 {
			t.Skip("running as root: a self-owned dir is root-owned, nothing to reject")
		}
		dir := filepath.Join(t.TempDir(), "tight")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyRootOnlyDir(info); err == nil {
			t.Error("verifyRootOnlyDir accepted a non-root 0700 dir, want ownership rejection")
		}
	})
}
