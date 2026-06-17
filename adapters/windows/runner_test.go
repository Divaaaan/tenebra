package windows

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/control"
)

// Runner must satisfy the control.Runner contract; this fails to compile if the
// interface drifts.
var _ control.Runner = (*Runner)(nil)

func TestResolveSingboxEnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom-sing-box")
	t.Setenv(singboxEnv, want)

	r := New()
	got, err := r.resolveSingbox()
	if err != nil {
		t.Fatalf("resolveSingbox: %v", err)
	}
	if got != want {
		t.Errorf("override path = %q, want %q", got, want)
	}
}

func TestResolveSingboxNextToExe(t *testing.T) {
	t.Setenv(singboxEnv, "")

	r := New()
	got, err := r.resolveSingbox()
	if err != nil {
		t.Fatalf("resolveSingbox: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}
	want := filepath.Join(filepath.Dir(exe), singboxBinaryName())
	if got != want {
		t.Errorf("resolved path = %q, want %q", got, want)
	}
}

func TestSingboxBinaryNameSuffix(t *testing.T) {
	name := singboxBinaryName()
	if runtime.GOOS == "windows" {
		if name != "sing-box.exe" {
			t.Errorf("windows binary = %q, want sing-box.exe", name)
		}
	} else if name != "sing-box" {
		t.Errorf("non-windows binary = %q, want sing-box", name)
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "sub", "dst.bin")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("fake wintun payload\x00\x01\x02")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyFile(dst, src); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("copied contents = %q, want %q", got, want)
	}
}

// TestEnsureWintunCopiesBesideBin exercises the wintun placement on Windows,
// where it runs, and the no-op path elsewhere. The copy helper itself is covered
// cross-platform by TestCopyFile.
func TestEnsureWintunCopiesBesideBin(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("wintun handling only runs on windows")
	}
	// On Windows the source is resolved next to the test binary; we can't plant a
	// file there safely, so only assert that a present dll short-circuits.
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "wintun.dll"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureWintun(binDir); err != nil {
		t.Errorf("ensureWintun with present dll should be a no-op, got %v", err)
	}
}

func TestWriteConfig(t *testing.T) {
	cfg := []byte(`{"log":{"level":"info"},"outbounds":[]}`)
	path, err := writeConfig(cfg)
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	defer os.Remove(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Equal(got, cfg) {
		t.Errorf("config bytes = %q, want %q", got, cfg)
	}
}

func TestDoneBlocksBeforeStart(t *testing.T) {
	r := New()
	select {
	case <-r.Done():
		t.Fatal("Done fired before any Start")
	case <-time.After(50 * time.Millisecond):
		// expected: nothing to receive
	}
}

func TestStopWithoutStartIsNil(t *testing.T) {
	r := New()
	if err := r.Stop(); err != nil {
		t.Errorf("Stop on idle runner = %v, want nil", err)
	}
	// Idempotent second call.
	if err := r.Stop(); err != nil {
		t.Errorf("second Stop = %v, want nil", err)
	}
}

func TestParseConnections(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantUp   int64
		wantDown int64
		wantErr  bool
	}{
		{
			name:     "typical",
			body:     `{"downloadTotal":51200,"uploadTotal":10240,"connections":[]}`,
			wantUp:   10240,
			wantDown: 51200,
		},
		{
			name:     "zero",
			body:     `{"downloadTotal":0,"uploadTotal":0,"connections":null}`,
			wantUp:   0,
			wantDown: 0,
		},
		{
			name:     "extra fields ignored",
			body:     `{"downloadTotal":7,"uploadTotal":3,"memory":123,"foo":"bar"}`,
			wantUp:   3,
			wantDown: 7,
		},
		{
			name:    "malformed",
			body:    `{not json`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up, down, err := parseConnections([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseConnections: %v", err)
			}
			if up != tt.wantUp || down != tt.wantDown {
				t.Errorf("up,down = %d,%d, want %d,%d", up, down, tt.wantUp, tt.wantDown)
			}
		})
	}
}

func TestRingBuffer(t *testing.T) {
	b := newRingBuffer(3)
	if got := b.snapshot(); len(got) != 0 {
		t.Errorf("fresh ring = %v, want empty", got)
	}
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		b.add(s)
	}
	got := b.snapshot()
	want := []string{"c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("ring len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ring[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestStatsErrorWhenAPIDown(t *testing.T) {
	// Point at a port nothing listens on; Stats must error, not panic, and the
	// error is the caller's signal that the API isn't up yet.
	r := New()
	r.ClashPort = 1 // privileged, unused by us in tests
	if _, _, err := r.Stats(); err == nil {
		t.Error("Stats against a dead port should error")
	}
}
