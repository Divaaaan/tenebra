//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestFindBundledSingbox walks the two layouts the service resolves against:
// the Tauri bundle (resources\ next to the executable), the flat layout
// (sing-box beside the executable), and the bundle winning when both exist.
func TestFindBundledSingbox(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		if got := findBundledSingbox(t.TempDir()); got != "" {
			t.Errorf("findBundledSingbox on empty dir = %q, want empty", got)
		}
	})

	t.Run("flat", func(t *testing.T) {
		dir := t.TempDir()
		want := filepath.Join(dir, "sing-box.exe")
		if err := os.WriteFile(want, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := findBundledSingbox(dir); got != want {
			t.Errorf("findBundledSingbox flat = %q, want %q", got, want)
		}
	})

	t.Run("bundle wins", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "resources"), 0o755); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(dir, "resources", "sing-box.exe")
		for _, p := range []string{want, filepath.Join(dir, "sing-box.exe")} {
			if err := os.WriteFile(p, []byte("fake"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := findBundledSingbox(dir); got != want {
			t.Errorf("findBundledSingbox bundle = %q, want %q", got, want)
		}
	})
}

// TestSecureDataDir verifies the directory comes out with the clamped
// descriptor — a protected DACL granting exactly SYSTEM and Administrators —
// and that a second run over the existing directory is a no-op rather than an
// error (the service re-stamps on every start).
func TestSecureDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	// The clamped DACL locks the test user out of its own temp subdirectory,
	// which would trip t.TempDir's cleanup; reopen it before the test ends.
	t.Cleanup(func() { unclampDir(t, dir) })

	if err := secureDataDir(dir); err != nil {
		t.Fatalf("secureDataDir: %v", err)
	}
	assertClamped(t, dir)

	// Re-stamping an existing directory must succeed and keep the clamp.
	if err := secureDataDir(dir); err != nil {
		t.Fatalf("secureDataDir on existing dir: %v", err)
	}
	assertClamped(t, dir)
}

// assertClamped fails the test unless dir carries the protected two-ACE DACL
// secureDataDir stamps.
func assertClamped(t *testing.T, dir string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read security info: %v", err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("read control bits: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("DACL is not protected; control = %#x", control)
	}
	s := sd.String()
	for _, ace := range []string{"(A;OICI;FA;;;SY)", "(A;OICI;FA;;;BA)"} {
		if !strings.Contains(s, ace) {
			t.Errorf("descriptor %q missing ACE %s", s, ace)
		}
	}
	if got := strings.Count(s, "(A;"); got != 2 {
		t.Errorf("descriptor %q has %d allow ACEs, want exactly 2", s, got)
	}
}

// unclampDir restores an everyone-full-control DACL so the temp cleanup can
// remove what secureDataDir locked down.
func unclampDir(t *testing.T, dir string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString("D:(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatalf("parse cleanup descriptor: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("read cleanup DACL: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Logf("reopen %s for cleanup: %v", dir, err)
	}
}

// TestConfigureServicePathsRespectsOverrides: explicit environment wins — the
// service must not move a store or binary an operator pinned, and with both
// knobs set it touches nothing (no %ProgramData% directory creation).
func TestConfigureServicePathsRespectsOverrides(t *testing.T) {
	cfg := t.TempDir()
	bin := filepath.Join(t.TempDir(), "sing-box.exe")
	t.Setenv("TENEBRA_CONFIG_DIR", cfg)
	t.Setenv("TENEBRA_SINGBOX", bin)

	if err := configureServicePaths(); err != nil {
		t.Fatalf("configureServicePaths: %v", err)
	}
	if got := os.Getenv("TENEBRA_CONFIG_DIR"); got != cfg {
		t.Errorf("TENEBRA_CONFIG_DIR = %q, want %q", got, cfg)
	}
	if got := os.Getenv("TENEBRA_SINGBOX"); got != bin {
		t.Errorf("TENEBRA_SINGBOX = %q, want %q", got, bin)
	}
}

// TestConfigureServicePathsSingboxUnresolved: with no override and no bundled
// sing-box near the (test) executable, the variable stays unset so the
// runner's own next-to-executable default and error reporting apply.
func TestConfigureServicePathsSingboxUnresolved(t *testing.T) {
	t.Setenv("TENEBRA_CONFIG_DIR", t.TempDir()) // keep the test off %ProgramData%
	t.Setenv("TENEBRA_SINGBOX", "")

	if err := configureServicePaths(); err != nil {
		t.Fatalf("configureServicePaths: %v", err)
	}
	if got := os.Getenv("TENEBRA_SINGBOX"); got != "" {
		t.Errorf("TENEBRA_SINGBOX = %q, want empty", got)
	}
}
