package windows

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogs(t *testing.T) {
	r := New()
	if got := r.Logs(); len(got) != 0 {
		t.Errorf("Logs on a fresh runner = %v, want empty", got)
	}

	// Seed the ring the way the stream scanners would and confirm Logs returns a
	// newest-last copy.
	r.ring.add("first")
	r.ring.add("second")
	got := r.Logs()
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("Logs = %v, want [first second]", got)
	}

	// The returned slice must be a copy: mutating it must not corrupt the ring.
	got[0] = "tampered"
	if again := r.Logs(); again[0] != "first" {
		t.Errorf("Logs returned an aliased slice; ring corrupted to %v", again)
	}
}

func TestLogsNilRing(t *testing.T) {
	// A zero-value Runner has no ring; Logs must report nil, not panic.
	var r Runner
	if got := r.Logs(); got != nil {
		t.Errorf("Logs on zero-value runner = %v, want nil", got)
	}
}

func TestCopyFileMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := copyFile(filepath.Join(dir, "dst.bin"), filepath.Join(dir, "nope.bin"))
	if err == nil {
		t.Error("copyFile from a missing source = nil, want error")
	}
}

func TestCopyFileUnwritableDest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A destination whose parent is a file (not a directory) can't be created on
	// any platform.
	notADir := filepath.Join(dir, "afile")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(notADir, "dst.bin")
	if err := copyFile(dst, src); err == nil {
		t.Error("copyFile into a non-directory parent = nil, want error")
	}
}

func TestWriteConfigBadTempDir(t *testing.T) {
	// Point the temp dir at a path that isn't a directory; CreateTemp must fail
	// and writeConfig must wrap the error rather than return a usable path.
	dir := t.TempDir()
	notADir := filepath.Join(dir, "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMP", notADir)
	t.Setenv("TEMP", notADir)
	t.Setenv("TMPDIR", notADir)

	if _, err := writeConfig([]byte(`{}`)); err == nil {
		t.Error("writeConfig with a bogus temp dir = nil, want error")
	}
}
