//go:build darwin

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

// TestFindBundledSingbox walks the layouts the daemon resolves sing-box against:
// none present (decline), the flat/externalBin layout (beside the executable),
// and the Contents/Resources sibling of an .app bundle.
func TestFindBundledSingbox(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		if got := findBundledSingbox(t.TempDir()); got != "" {
			t.Errorf("findBundledSingbox on empty dir = %q, want empty", got)
		}
	})

	t.Run("flat", func(t *testing.T) {
		dir := t.TempDir()
		want := filepath.Join(dir, "sing-box")
		if err := os.WriteFile(want, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := findBundledSingbox(dir); got != want {
			t.Errorf("findBundledSingbox flat = %q, want %q", got, want)
		}
	})

	t.Run("resources sibling", func(t *testing.T) {
		// <root>/MacOS/tenebra-core resolves sing-box from <root>/Resources.
		root := t.TempDir()
		macosDir := filepath.Join(root, "MacOS")
		resDir := filepath.Join(root, "Resources")
		if err := os.MkdirAll(macosDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(resDir, 0o755); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(resDir, "sing-box")
		if err := os.WriteFile(want, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := findBundledSingbox(macosDir); got != want {
			t.Errorf("findBundledSingbox resources = %q, want %q", got, want)
		}
	})
}

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
