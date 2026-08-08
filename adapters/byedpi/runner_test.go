package byedpi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/dpi"
)

// Runner must satisfy the narrow dpi.Runner contract; this fails to compile if
// the interface drifts.
var _ dpi.Runner = (*Runner)(nil)

// fakeChildFlag turns this test binary into a stand-in for ciadpi: TestMain
// spots it as the first argument and runs the named script instead of the test
// suite. Re-executing ourselves keeps the lifecycle honest — a real process,
// real pipes, a real exit code — without carrying a ByeDPI build into CI.
const fakeChildFlag = "-tenebra-fake-child"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], fakeChildFlag+"=") {
		os.Exit(fakeChild(strings.TrimPrefix(os.Args[1], fakeChildFlag+"=")))
	}
	os.Exit(m.Run())
}

// fakeChild plays the child-process behaviours the lifecycle tests need.
func fakeChild(script string) int {
	switch script {
	case "block":
		fmt.Println("fake ciadpi listening")
		fmt.Fprintln(os.Stderr, "fake ciadpi warming up")
		// Sleep rather than block forever: a parked main goroutine trips Go's
		// deadlock detector, and the runner kills us long before this elapses.
		time.Sleep(10 * time.Minute)
		return 0
	case "chatty":
		fmt.Println("fake ciadpi stdout line")
		fmt.Fprintln(os.Stderr, "fake ciadpi stderr line")
		return 0
	case "exit3":
		fmt.Fprintln(os.Stderr, "fake ciadpi: bind failed")
		return 3
	case "quiet":
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown fake child script %q\n", script)
	return 2
}

// startFake points binary resolution at this test binary and starts the named
// script under a fresh Runner.
func startFake(t *testing.T, script string) *Runner {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}
	t.Setenv(dpi.BinaryEnv, exe)

	r := New()
	t.Cleanup(func() { _ = r.Stop() })
	if err := r.Start(context.Background(), []string{fakeChildFlag + "=" + script}); err != nil {
		t.Fatalf("Start(%s): %v", script, err)
	}
	return r
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
		t.Errorf("Stop on an idle runner = %v, want nil", err)
	}
	if err := r.Stop(); err != nil {
		t.Errorf("second Stop = %v, want nil", err)
	}
}

func TestStartStopLifecycle(t *testing.T) {
	r := startFake(t, "block")

	// A running process must not report an exit.
	select {
	case err := <-r.Done():
		t.Fatalf("Done fired while the process runs: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Stop waits for the watcher, so the exit channel is drained and closed by
	// the time it returns.
	select {
	case <-r.Done():
	case <-time.After(time.Second):
		t.Fatal("Done still blocking after Stop returned")
	}
	// Idempotent: stopping an already-stopped runner is a no-op, not an error.
	if err := r.Stop(); err != nil {
		t.Errorf("second Stop = %v, want nil", err)
	}
}

func TestStartAfterStop(t *testing.T) {
	r := startFake(t, "quiet")
	if err := <-r.Done(); err != nil {
		t.Fatalf("clean child exit reported as %v", err)
	}
	// The watcher clears the running state, so the same Runner can be reused —
	// the daemon restarts the bypass on a settings change.
	if err := r.Start(context.Background(), []string{fakeChildFlag + "=quiet"}); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if err := <-r.Done(); err != nil {
		t.Errorf("second run exited with %v", err)
	}
}

func TestStartRejectsSecondProcess(t *testing.T) {
	r := startFake(t, "block")
	err := r.Start(context.Background(), []string{fakeChildFlag + "=block"})
	if err == nil {
		t.Fatal("Start while running = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error %q does not say the process is already running", err)
	}
}

func TestChildExitReportedOnDone(t *testing.T) {
	r := startFake(t, "exit3")

	select {
	case err := <-r.Done():
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("Done delivered %v (%T), want an *exec.ExitError", err, err)
		}
		if got := exitErr.ExitCode(); got != 3 {
			t.Errorf("exit code = %d, want 3", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Done never fired for a process that exited")
	}
}

func TestStartMissingBinary(t *testing.T) {
	t.Setenv(dpi.BinaryEnv, filepath.Join(t.TempDir(), "definitely-not-here"))

	r := New()
	if err := r.Start(context.Background(), nil); err == nil {
		t.Fatal("Start with a missing binary = nil, want error")
	}
	// A failed Start must leave nothing behind: no phantom running process, and
	// Done must still block rather than report an exit that never happened.
	if err := r.Stop(); err != nil {
		t.Errorf("Stop after a failed Start = %v, want nil", err)
	}
	select {
	case <-r.Done():
		t.Error("Done fired after a Start that never spawned anything")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLogsCaptureBothStreams(t *testing.T) {
	r := startFake(t, "chatty")
	if err := <-r.Done(); err != nil {
		t.Fatalf("chatty child exited with %v", err)
	}

	// The watcher drains both pipes before reaping, so the full output is in the
	// ring by the time the exit is published.
	logs := strings.Join(r.Logs(), "\n")
	for _, want := range []string{"fake ciadpi stdout line", "fake ciadpi stderr line"} {
		if !strings.Contains(logs, want) {
			t.Errorf("Logs missing %q; got:\n%s", want, logs)
		}
	}
}

func TestLogsFreshRunnerIsEmpty(t *testing.T) {
	if got := New().Logs(); len(got) != 0 {
		t.Errorf("Logs on a fresh runner = %v, want empty", got)
	}
}

func TestLogsNilRing(t *testing.T) {
	// A zero-value Runner has no ring; Logs must report nil, not panic.
	var r Runner
	if got := r.Logs(); got != nil {
		t.Errorf("Logs on a zero-value runner = %v, want nil", got)
	}
}

func TestLogsReturnsACopy(t *testing.T) {
	r := New()
	r.ring.add("first")
	r.ring.add("second")
	got := r.Logs()
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("Logs = %v, want [first second]", got)
	}
	got[0] = "tampered"
	if again := r.Logs(); again[0] != "first" {
		t.Errorf("Logs returned an aliased slice; ring corrupted to %v", again)
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

// TestHideConsoleWindow pins the one piece of platform behaviour in the package:
// on Windows the child must carry CREATE_NO_WINDOW so a GUI-launched core does
// not flash a console; everywhere else the spawn attributes stay untouched.
func TestHideConsoleWindow(t *testing.T) {
	cmd := exec.Command("does-not-need-to-exist")
	hideConsole(cmd)

	flags, ok := creationFlags(cmd)
	if runtime.GOOS == "windows" {
		if !ok {
			t.Fatal("SysProcAttr.CreationFlags was not set on windows")
		}
		if flags&createNoWindow == 0 {
			t.Errorf("CreationFlags = %#x, missing CREATE_NO_WINDOW (%#x)", flags, createNoWindow)
		}
		return
	}
	if ok {
		t.Errorf("spawn attributes were touched on %s (flags %#x)", runtime.GOOS, flags)
	}
}

// creationFlags reads back the Windows creation flags the same way hideConsole
// writes them, so the assertion does not need syscall either.
func creationFlags(cmd *exec.Cmd) (uint64, bool) {
	attr := reflect.ValueOf(cmd).Elem().FieldByName("SysProcAttr")
	if !attr.IsValid() || attr.Kind() != reflect.Pointer || attr.IsNil() {
		return 0, false
	}
	f := attr.Elem().FieldByName("CreationFlags")
	if !f.IsValid() || !f.CanUint() {
		return 0, false
	}
	return f.Uint(), true
}
