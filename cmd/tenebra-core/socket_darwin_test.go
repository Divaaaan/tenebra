//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindBundledSingbox walks the layouts the daemon resolves sing-box against:
// none present (decline), the flat/externalBin layout (beside the executable),
// and the Contents/Resources sibling of an .app bundle.
func TestFindBundledSingbox(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		if got := findSingbox(t.TempDir()); got != "" {
			t.Errorf("findSingbox on empty dir = %q, want empty", got)
		}
	})

	t.Run("flat", func(t *testing.T) {
		dir := t.TempDir()
		want := filepath.Join(dir, "sing-box")
		if err := os.WriteFile(want, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := findSingbox(dir); got != want {
			t.Errorf("findSingbox flat = %q, want %q", got, want)
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
		if got := findSingbox(macosDir); got != want {
			t.Errorf("findSingbox resources = %q, want %q", got, want)
		}
	})
}

// TestRootDataDirIsMachineScoped: the store the LaunchDaemon pins must be the
// machine-wide location, not anything under a user's home — a root daemon
// writing into /Users/... would both hide the profiles from the GUI's view of
// the machine store and put credentials on a user-writable path.
func TestRootDataDirIsMachineScoped(t *testing.T) {
	const want = "/Library/Application Support/Tenebra/data"
	if rootDataDir != want {
		t.Errorf("rootDataDir = %q, want %q", rootDataDir, want)
	}
}
