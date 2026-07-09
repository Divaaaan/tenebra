//go:build darwin

package macos

import (
	"bytes"
	"os"
	"path/filepath"
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

func TestSingboxBinaryName(t *testing.T) {
	// The darwin release ships the binary without an extension.
	if name := singboxBinaryName(); name != "sing-box" {
		t.Errorf("binary = %q, want sing-box", name)
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

func TestWriteConfigBadTempDir(t *testing.T) {
	// Point the temp dir at a path that isn't a directory; CreateTemp must fail
	// and writeConfig must wrap the error rather than return a usable path.
	dir := t.TempDir()
	notADir := filepath.Join(dir, "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", notADir)

	if _, err := writeConfig([]byte(`{}`)); err == nil {
		t.Error("writeConfig with a bogus temp dir = nil, want error")
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

func TestElevationHintFor(t *testing.T) {
	// root (euid 0) needs no hint; anything else gets the diagnostic note that
	// explains why utun cannot be opened without a privileged helper.
	if got := elevationHintFor(0); got != "" {
		t.Errorf("elevationHintFor(0) = %q, want empty", got)
	}
	if got := elevationHintFor(501); got == "" {
		t.Error("elevationHintFor(501) = empty, want a diagnostic hint")
	}
}
