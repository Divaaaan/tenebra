//go:build linux || darwin

package installtx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// This isolated fixture executes the Go TEST binary, not a production core or
// engine. It accepts only the exact production launcher flags and checks that
// parent environment authority did not cross the exec boundary.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "--socket" {
		tx := strings.TrimPrefix(os.Args[2], "--maintenance-tx=")
		launch := strings.TrimPrefix(os.Args[3], "--launch-id=")
		os.Exit(runOwnedFixture(tx, launch))
	}
	os.Exit(m.Run())
}
func runOwnedFixture(tx, launch string) int {
	if !protocolID(tx, "it1_") || !hexValue(launch, 32) {
		return 43
	}
	for _, key := range []string{"TENEBRA_CONFIG_DIR", "TENEBRA_SINGBOX", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES"} {
		if os.Getenv(key) != "" {
			return 42
		}
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	executable, err := os.Executable()
	if err != nil {
		return 44
	}
	release := filepath.Dir(executable)
	if !hexValue(filepath.Base(release), 64) || filepath.Base(filepath.Dir(release)) != "releases" {
		return 44
	}
	base := filepath.Dir(filepath.Dir(release))
	readyPath := filepath.Join(base, "maintenance", "ready")
	if info, err := os.Stat(readyPath); err == nil && info.IsDir() {
		policy := securefs.Policy{Mode: securefs.MachinePrivate}
		ready, err := securefs.OpenRoot(readyPath, policy, false)
		if err != nil {
			return 45
		}
		defer ready.Close()
		owners, err := securefs.OpenRoot(filepath.Join(base, "maintenance", "socket-owner"), policy, false)
		if err != nil {
			return 45
		}
		defer owners.Close()
		listener, err := ListenPrivateReady(context.Background(), ready, owners, tx, launch)
		if err != nil {
			return 46
		}
		defer listener.Close()
		if _, err := os.Stat(filepath.Join(base, "maintenance", "journal.json")); err == nil {
			stop, err := startHostFixturePrivate(base, release, tx, launch, listener)
			if err != nil {
				return 47
			}
			defer stop()
		}
	}
	fmt.Println("FIXTURE_ENV_AND_FLAGS_OK")
	if strings.HasPrefix(launch, "d") {
		for {
			<-signals
		}
	}
	<-signals
	return 0
}

type fixtureReadyWriter struct {
	mu    sync.Mutex
	body  []byte
	once  sync.Once
	ready chan struct{}
}

func (w *fixtureReadyWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.body) < 256 {
		w.body = append(w.body, data...)
	}
	if bytes.Contains(w.body, []byte("FIXTURE_ENV_AND_FLAGS_OK")) {
		w.once.Do(func() { close(w.ready) })
	}
	return len(data), nil
}
func coreProcessFixture(t *testing.T) (*CoreLauncher, RuntimeRef, *fixtureReadyWriter) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("actual root child fixture requires an isolated native root process")
	}
	stager, source, input, manifest := stagingFixture(t, "flat", "operator_dev", true)
	if runtime.GOOS == "darwin" {
		manifest.Target = "darwin-universal"
		stager.policy.Target = manifest.Target
	}
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if state, err := input.ReplaceStream(context.Background(), "tenebra-core", bytes.NewReader(body), MaxContainerBytes, 0600); err != nil || state != securefs.Committed {
		t.Fatal(state, err)
	}
	for i := range manifest.Files {
		if manifest.Files[i].Name == "tenebra-core" {
			manifest.Files[i].Size = int64(len(body))
			manifest.Files[i].SHA256 = hex.EncodeToString(sum[:])
		}
	}
	canonical, err := CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := input.ReplaceFile("payload.json", canonical); err != nil || state != securefs.Committed {
		t.Fatal(state, err)
	}
	payload, err := stager.Stage(context.Background(), source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := stager.PublishRuntime(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	data := privateInstallerRoot(t)
	launcher, err := newCoreLauncher(data, stager.releases, securefs.Policy{Mode: securefs.SameAccountPrivate}, ref.HelperSHA256, manifest.Target)
	if err != nil {
		t.Fatal(err)
	}
	writer := &fixtureReadyWriter{ready: make(chan struct{})}
	launcher.output = writer
	t.Cleanup(func() {
		if err := launcher.Close(); err != nil {
			t.Error(err)
		}
	})
	return launcher, ref, writer
}
func startFixtureCore(t *testing.T, l *CoreLauncher, ref RuntimeRef, w *fixtureReadyWriter, launch string) *CoreProcess {
	t.Helper()
	child, err := l.StartHeld(context.Background(), ref, "it1_"+strings.Repeat("a", 32), launch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := child.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-w.ready:
	case <-child.Done():
		t.Fatal("fixture exited before checking real exec flags and sanitized environment")
	case <-time.After(3 * time.Second):
		t.Fatal("fixture did not confirm real exec flags/environment")
	}
	return child
}
func TestNativeCoreLauncherOwnsProcessAndLeaseAndSanitizesEnvironment(t *testing.T) {
	l, ref, w := coreProcessFixture(t)
	t.Setenv("TENEBRA_CONFIG_DIR", "/tmp/SYNTHETIC_UNTRUSTED_CONFIG")
	t.Setenv("TENEBRA_SINGBOX", "/tmp/SYNTHETIC_UNTRUSTED_ENGINE")
	t.Setenv("LD_PRELOAD", "/tmp/SYNTHETIC_UNTRUSTED_LIBRARY")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/tmp/SYNTHETIC_UNTRUSTED_LIBRARY")
	child := startFixtureCore(t, l, ref, w, strings.Repeat("c", 32))
	identity := child.Identity()
	if second, err := l.StartHeld(context.Background(), ref, "it1_"+strings.Repeat("b", 32), strings.Repeat("b", 32)); !errors.Is(err, securefs.ErrBusy) || second != nil {
		t.Fatal("second core overlapped owned process", err)
	}
	if !validHostIdentity(identity) || identity.LaunchID != strings.Repeat("c", 32) || child.Runtime() != ref || !hexValue(child.CoreSHA256(), 64) {
		t.Fatal("spawn identity/bytes lost")
	}
	if err := l.Close(); !errors.Is(err, securefs.ErrBusy) {
		t.Fatal("launcher released roots beneath a live child", err)
	}
	short, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := child.Wait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("live child falsely acknowledged exit", err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 4*time.Second)
	defer stop()
	if err := child.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.Done():
	default:
		t.Fatal("stop ACK preceded actual Wait and lease release")
	}
	if token, err := ProcessStartToken(identity.PID); err == nil && token == identity.StartToken {
		t.Fatal("same process lifetime still exists after stop")
	}
	if err := child.Stop(ctx); err != nil {
		t.Fatal("stop was not idempotent", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestNativeCoreStopTimeoutKeepsCleanupRunningUntilActualExit(t *testing.T) {
	l, ref, w := coreProcessFixture(t)
	child := startFixtureCore(t, l, ref, w, strings.Repeat("d", 32))
	short, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := child.Stop(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("delayed native exit acknowledged too early", err)
	}
	if err := l.Close(); !errors.Is(err, securefs.ErrBusy) {
		t.Fatal("pending native child lost its lease", err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 4*time.Second)
	defer stop()
	if err := child.Stop(ctx); err != nil {
		t.Fatal("same owned cleanup did not finish", err)
	}
}
func TestNativeCoreLauncherRejectsHashAndHelperMismatchBeforeExec(t *testing.T) {
	l, ref, w := coreProcessFixture(t)
	wrong := ref
	wrong.HelperSHA256 = strings.Repeat("f", 64)
	if child, err := l.StartHeld(context.Background(), wrong, "it1_"+strings.Repeat("a", 32), strings.Repeat("c", 32)); err == nil || child != nil {
		t.Fatal("wrong build helper executed candidate")
	}
	rootPath, err := l.releases.Path()
	if err != nil {
		t.Fatal(err)
	}
	// Deliberate isolated-root tamper after publication; production never
	// repairs or executes this changed object.
	if err := os.WriteFile(filepath.Join(rootPath, ref.ArtifactSHA256, "tenebra-core"), []byte("SYNTHETIC_TAMPER_ONLY"), 0700); err != nil {
		t.Fatal(err)
	}
	if child, err := l.StartHeld(context.Background(), ref, "it1_"+strings.Repeat("a", 32), strings.Repeat("c", 32)); err == nil || child != nil {
		t.Fatal("tampered protected bytes were executed")
	}
	select {
	case <-w.ready:
		t.Fatal("rejected candidate executed")
	default:
	}
}

var _ io.Writer = (*fixtureReadyWriter)(nil)
