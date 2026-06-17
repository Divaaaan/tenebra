package windows

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureWintunFrom covers the three branches of the wintun placement logic
// cross-platform, using temp dirs for both the destination and the source so it
// never depends on os.Executable's real location.
func TestEnsureWintunFrom(t *testing.T) {
	t.Run("present is noop", func(t *testing.T) {
		binDir := t.TempDir()
		srcDir := t.TempDir()
		want := []byte("already here")
		if err := os.WriteFile(filepath.Join(binDir, "wintun.dll"), want, 0o644); err != nil {
			t.Fatal(err)
		}
		// A nonexistent source must be ignored when the dll is already present.
		if err := ensureWintunFrom(binDir, srcDir); err != nil {
			t.Fatalf("ensureWintunFrom with present dll = %v, want nil", err)
		}
		got, err := os.ReadFile(filepath.Join(binDir, "wintun.dll"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("present dll was overwritten: got %q, want %q", got, want)
		}
	})

	t.Run("copies when missing", func(t *testing.T) {
		binDir := t.TempDir()
		srcDir := t.TempDir()
		want := []byte("fake wintun bytes")
		if err := os.WriteFile(filepath.Join(srcDir, "wintun.dll"), want, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ensureWintunFrom(binDir, srcDir); err != nil {
			t.Fatalf("ensureWintunFrom = %v, want nil", err)
		}
		got, err := os.ReadFile(filepath.Join(binDir, "wintun.dll"))
		if err != nil {
			t.Fatalf("dll not placed in binDir: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("copied dll = %q, want %q", got, want)
		}
	})

	t.Run("missing source errors", func(t *testing.T) {
		binDir := t.TempDir()
		srcDir := t.TempDir() // empty: no wintun.dll to copy
		if err := ensureWintunFrom(binDir, srcDir); err == nil {
			t.Fatal("ensureWintunFrom with no source dll = nil, want error")
		}
	})

	t.Run("same dir is noop", func(t *testing.T) {
		// When src and dst resolve to the same path and nothing is there, the
		// function returns nil rather than trying to copy a file onto itself.
		dir := t.TempDir()
		if err := ensureWintunFrom(dir, dir); err != nil {
			t.Fatalf("ensureWintunFrom(dir, dir) = %v, want nil", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "wintun.dll")); !os.IsNotExist(err) {
			t.Error("same-dir no-op should not have created a dll")
		}
	})
}

// TestEnsureWintunCurrentExe exercises the exported wrapper. It resolves the
// source from os.Executable; with the dll already present in binDir the source
// is irrelevant, so the call must be a no-op regardless of platform.
func TestEnsureWintunCurrentExe(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "wintun.dll"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureWintun(binDir); err != nil {
		t.Errorf("ensureWintun with present dll = %v, want nil", err)
	}
}

func TestWriteConfigRoundTrip(t *testing.T) {
	// writeConfig uses os.TempDir; pin it to a test dir so nothing leaks and we
	// can assert the file landed where expected.
	dir := t.TempDir()
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	t.Setenv("TMPDIR", dir)

	cfg := []byte(`{"log":{"level":"warn"},"inbounds":[],"outbounds":[]}`)
	path, err := writeConfig(cfg)
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	defer os.Remove(path)

	if filepath.Dir(path) != filepath.Clean(dir) {
		t.Errorf("config written to %q, want under %q", path, dir)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Equal(got, cfg) {
		t.Errorf("config bytes = %q, want %q", got, cfg)
	}
}

func TestProbeAgainstDeadPort(t *testing.T) {
	// Nothing listens on port 1; Probe must return a transport error, not panic.
	r := New()
	r.ClashPort = 1
	if _, err := r.Probe(context.Background(), "proxy"); err == nil {
		t.Error("Probe against a dead port should error")
	}
}

func TestProbeContextCancelled(t *testing.T) {
	// A cancelled context fails the request before it leaves; Probe surfaces it.
	r := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Probe(ctx, "proxy"); err == nil {
		t.Error("Probe with a cancelled context should error")
	}
}
