//go:build linux

package linux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const runnerHelperModeEnv = "TENEBRA_LINUX_RUNNER_HELPER_MODE"

func TestMain(m *testing.M) {
	switch os.Getenv(runnerHelperModeEnv) {
	case "normal":
		fmt.Fprintln(os.Stdout, "stdout-final")
		fmt.Fprint(os.Stderr, "stderr-tail")
		os.Exit(0)
	case "orphan-parent":
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "locate helper:", err)
			os.Exit(2)
		}
		cmd := exec.Command(exe)
		cmd.Env = withRunnerHelperMode(os.Environ(), "orphan-child")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "start orphan:", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, "orphan-ready")
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "orphan-child":
		time.Sleep(2500 * time.Millisecond)
		os.Exit(0)
	default:
		os.Exit(m.Run())
	}
}

func TestRunnerDoneIncludesFinalStdoutAndStderr(t *testing.T) {
	t.Setenv(runnerHelperModeEnv, "normal")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	r := New()
	r.binOverride = exe

	if err := r.Start(context.Background(), []byte(`{}`)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case err := <-r.Done():
		if err != nil {
			t.Fatalf("Done: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Done did not report helper exit")
	}

	logs := strings.Join(r.Logs(), "\n")
	for _, want := range []string{"stdout-final", "stderr-tail"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("Logs() = %q, missing %q", logs, want)
		}
	}
}

func TestRunnerStopBoundsOrphanedOutputPipeDrain(t *testing.T) {
	t.Setenv(runnerHelperModeEnv, "orphan-parent")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	r := New()
	r.binOverride = exe
	if err := r.Start(context.Background(), []byte(`{}`)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForRunnerLog(t, r, "orphan-ready")

	started := time.Now()
	stopped := make(chan error, 1)
	go func() { stopped <- r.Stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("Stop took %v; orphaned output pipe drain was not bounded", elapsed)
		}
	case <-time.After(time.Second):
		select {
		case <-stopped:
		case <-time.After(4 * time.Second):
		}
		t.Fatalf("Stop still blocked after %v on an orphaned output pipe", time.Since(started))
	}
}

func waitForRunnerLog(t *testing.T, r *Runner, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(r.Logs(), "\n"), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runner log never contained %q; logs=%q", want, r.Logs())
}

func withRunnerHelperMode(env []string, mode string) []string {
	prefix := runnerHelperModeEnv + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+mode)
}
