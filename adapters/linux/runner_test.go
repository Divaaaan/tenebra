//go:build linux

package linux

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/control"
)

// Runner must satisfy the control.Runner contract; this fails to compile if the
// interface drifts.
var _ control.Runner = (*Runner)(nil)

// emptyPATH points PATH at an empty directory for the duration of a test, so the
// last step of the search order is decided by the test rather than by whatever
// the machine running it happens to have installed.
func emptyPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

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

// TestResolveSingboxOverrideSkipsSearch: the override names a path that does not
// exist. It must still be returned verbatim — an operator pointing at a specific
// build wants that build's absence reported, not a silent substitution from
// PATH.
func TestResolveSingboxOverrideSkipsSearch(t *testing.T) {
	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "sing-box"), []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	want := filepath.Join(t.TempDir(), "absent-sing-box")
	t.Setenv(singboxEnv, want)

	r := New()
	got, err := r.resolveSingbox()
	if err != nil {
		t.Fatalf("resolveSingbox: %v", err)
	}
	if got != want {
		t.Errorf("resolved path = %q, want the override %q", got, want)
	}
}

// TestResolveSingboxFallsBackToNeighbour: with no override and nothing found
// anywhere, the resolver still names the path next to the executable so the
// spawn error points somewhere concrete.
func TestResolveSingboxFallsBackToNeighbour(t *testing.T) {
	t.Setenv(singboxEnv, "")
	emptyPATH(t)

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

// TestInstallDirsOrder pins the search order: the executable's own directory
// first (a self-contained install, an AppImage, a dev checkout), then the
// prefix-relative private directories a distribution package uses, then the
// absolute /usr backstop for a core that is not under <prefix>/bin.
func TestInstallDirsOrder(t *testing.T) {
	got := InstallDirs("/opt/tenebra/bin")
	want := []string{
		"/opt/tenebra/bin",
		"/opt/tenebra/bin/resources",
		"/opt/tenebra/lib/tenebra",
		"/opt/tenebra/lib/tenebra/resources",
		"/opt/tenebra/lib/Tenebra",
		"/opt/tenebra/lib/Tenebra/resources",
		"/opt/tenebra/libexec/tenebra",
		"/opt/tenebra/libexec/tenebra/resources",
		"/opt/tenebra/libexec/Tenebra",
		"/opt/tenebra/libexec/Tenebra/resources",
		"/opt/tenebra/share/tenebra",
		"/opt/tenebra/share/tenebra/resources",
		"/opt/tenebra/share/Tenebra",
		"/opt/tenebra/share/Tenebra/resources",
		"/usr/lib/tenebra",
		"/usr/lib/tenebra/resources",
		"/usr/lib/Tenebra",
		"/usr/lib/Tenebra/resources",
		"/usr/libexec/tenebra",
		"/usr/libexec/tenebra/resources",
		"/usr/libexec/Tenebra",
		"/usr/libexec/Tenebra/resources",
		"/usr/share/tenebra",
		"/usr/share/tenebra/resources",
		"/usr/share/Tenebra",
		"/usr/share/Tenebra/resources",
	}
	if !slices.Equal(got, want) {
		t.Errorf("InstallDirs = %v, want %v", got, want)
	}
}

// TestInstallDirsCoversTheDebianLayout pins the one path the .deb actually uses.
// Tauri's bundler names that directory after the product, not the package, and
// nests resources one level down; a search that only knew the hand-written Arch
// layout would leave a .deb install with a daemon that cannot find sing-box —
// while the GUI, which resolves through Tauri, kept working and hid the fault.
func TestInstallDirsCoversTheDebianLayout(t *testing.T) {
	if !slices.Contains(InstallDirs("/usr/bin"), "/usr/lib/Tenebra/resources") {
		t.Error("InstallDirs does not probe /usr/lib/Tenebra/resources, where the .deb puts sing-box")
	}
}

// TestInstallDirsDedupes: the overwhelmingly common install has the core in
// /usr/bin, where the prefix-relative directories and the absolute backstop are
// the same paths. They must appear once, so the search log reads as a list of
// distinct places rather than a repeat.
func TestInstallDirsDedupes(t *testing.T) {
	got := InstallDirs("/usr/bin")
	want := []string{
		"/usr/bin",
		"/usr/bin/resources",
		"/usr/lib/tenebra",
		"/usr/lib/tenebra/resources",
		"/usr/lib/Tenebra",
		"/usr/lib/Tenebra/resources",
		"/usr/libexec/tenebra",
		"/usr/libexec/tenebra/resources",
		"/usr/libexec/Tenebra",
		"/usr/libexec/Tenebra/resources",
		"/usr/share/tenebra",
		"/usr/share/tenebra/resources",
		"/usr/share/Tenebra",
		"/usr/share/Tenebra/resources",
	}
	if !slices.Equal(got, want) {
		t.Errorf("InstallDirs = %v, want %v", got, want)
	}
}

// TestSingboxCandidatesEndWithPATH: PATH is consulted, and last. That ordering
// is the whole contract with a distribution that ships sing-box as its own
// package — it is found — while a copy installed with Tenebra still wins.
func TestSingboxCandidatesEndWithPATH(t *testing.T) {
	pathDir := t.TempDir()
	onPath := filepath.Join(pathDir, "sing-box")
	if err := os.WriteFile(onPath, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	got := SingboxCandidates("/opt/tenebra/bin")
	if len(got) == 0 {
		t.Fatal("SingboxCandidates returned nothing")
	}
	if got[0] != "/opt/tenebra/bin/sing-box" {
		t.Errorf("first candidate = %q, want the neighbour binary", got[0])
	}
	if last := got[len(got)-1]; last != onPath {
		t.Errorf("last candidate = %q, want the PATH hit %q", last, onPath)
	}
	if slices.Contains(got[:len(got)-1], onPath) {
		t.Errorf("the PATH hit appears before the install locations: %v", got)
	}
}

// TestSingboxCandidatesWithoutPATH: nothing on PATH simply shortens the list; it
// is not an error and must not drop the install locations. The expectation is
// derived from InstallDirs rather than spelled out, so adding a packaging layout
// to the search path stays a one-line change there — TestInstallDirsOrder is
// where the literal list is pinned.
func TestSingboxCandidatesWithoutPATH(t *testing.T) {
	emptyPATH(t)

	dirs := InstallDirs("/opt/tenebra/bin")
	want := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		want = append(want, filepath.Join(dir, "sing-box"))
	}
	got := SingboxCandidates("/opt/tenebra/bin")
	if !slices.Equal(got, want) {
		t.Errorf("SingboxCandidates = %v, want %v", got, want)
	}
}

// TestFindSingboxPrefersEarlierCandidate: with a binary both next to the core
// and in a shared helper directory, the neighbour — the one installed with this
// build — is the answer.
func TestFindSingboxPrefersEarlierCandidate(t *testing.T) {
	emptyPATH(t)
	prefix := t.TempDir()
	binDir := filepath.Join(prefix, "bin")
	helperDir := filepath.Join(prefix, "lib", "tenebra")
	for _, d := range []string{binDir, helperDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := filepath.Join(binDir, "sing-box")
	for _, p := range []string{want, filepath.Join(helperDir, "sing-box")} {
		if err := os.WriteFile(p, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if got := FindSingbox(binDir); got != want {
		t.Errorf("FindSingbox = %q, want %q", got, want)
	}
}

func TestFindSingboxMissing(t *testing.T) {
	emptyPATH(t)
	// The absolute backstop in the search order is not injectable — that is the
	// point of it — so a machine with Tenebra genuinely installed system-wide
	// answers this correctly and has no miss left to observe.
	for _, dir := range []string{"/usr/lib/tenebra", "/usr/libexec/tenebra", "/usr/share/tenebra"} {
		p := filepath.Join(dir, "sing-box")
		if _, err := os.Stat(p); err == nil {
			t.Skipf("a system-wide sing-box at %s leaves no miss to observe", p)
		}
	}
	if got := FindSingbox(t.TempDir()); got != "" {
		t.Errorf("FindSingbox with nothing installed = %q, want empty", got)
	}
}

func TestSingboxBinaryName(t *testing.T) {
	// The Linux release ships the binary without an extension.
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
	if !slices.Equal(got, want) {
		t.Errorf("ring = %v, want %v", got, want)
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
	// explains why the tun device cannot be opened.
	if got := elevationHintFor(0); got != "" {
		t.Errorf("elevationHintFor(0) = %q, want empty", got)
	}
	got := elevationHintFor(1000)
	if got == "" {
		t.Fatal("elevationHintFor(1000) = empty, want a diagnostic hint")
	}
	// The hint has to name the thing a Linux user can act on. A message about a
	// Windows service or a macOS helper would be worse than none.
	for _, want := range []string{"CAP_NET_ADMIN", tunDevice, "tenebra.service"} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Errorf("elevation hint %q does not mention %q", got, want)
		}
	}
}

func TestTunDeviceHintFor(t *testing.T) {
	// A present device node needs no hint: any regular file stands in for it,
	// since the check is only "does this path exist".
	present := filepath.Join(t.TempDir(), "tun")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tunDeviceHintFor(present); got != "" {
		t.Errorf("tunDeviceHintFor(existing) = %q, want empty", got)
	}

	missing := filepath.Join(t.TempDir(), "net", "tun")
	got := tunDeviceHintFor(missing)
	if got == "" {
		t.Fatal("tunDeviceHintFor(missing) = empty, want a diagnostic hint")
	}
	if !bytes.Contains([]byte(got), []byte(missing)) {
		t.Errorf("hint %q does not name the missing device %q", got, missing)
	}
}
