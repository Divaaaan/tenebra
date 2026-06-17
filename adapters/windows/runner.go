// Package windows runs and supervises the sing-box process that backs the tunnel
// on desktop. It satisfies control.Runner: the control daemon hands it a config,
// it spawns sing-box, exposes traffic counters from the clash API, and reports
// the process exit.
//
// The package is named for its target platform because wintun handling only
// applies there, but it deliberately uses nothing OS-specific at the source
// level (no syscall, no x/sys) so it compiles and vets on every platform the CI
// builds. Windows-only behaviour is gated on runtime.GOOS at run time.
package windows

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// defaultClashPort matches singbox.TunOptions' default external controller port,
// where the config tells sing-box to expose the clash API.
const defaultClashPort = 9090

// singboxEnv overrides binary resolution when set, so an operator (or a test, or
// the smoke harness) can point at a specific sing-box build.
const singboxEnv = "TENEBRA_SINGBOX"

// logRingSize bounds the in-memory tail of sing-box output kept for diagnostics.
const logRingSize = 200

// statsTimeout keeps a clash API poll from blocking the traffic loop if the API
// is slow or not yet listening.
const statsTimeout = 2 * time.Second

// blocked is returned by Done before the first Start. It has no sender and is
// never closed, so a receive on it blocks forever — exactly the "Done must block
// before any Start" contract, without allocating a fresh channel per caller.
var blocked = make(chan error)

// Runner spawns and supervises one sing-box process at a time. The zero value is
// not usable; build it with New. It is safe for concurrent use: Stats and Done
// may be called while Start or Stop runs.
type Runner struct {
	// binOverride, when non-empty, is the sing-box path to run instead of the
	// one resolved next to the executable. New seeds it from TENEBRA_SINGBOX.
	binOverride string
	// ClashPort is the clash API port Stats queries; 0 means defaultClashPort.
	ClashPort int

	mu      sync.Mutex
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	done    chan error
	cfgPath string // temp config file for the running process, removed on stop
	ring    *ringBuffer
}

// New builds a Runner with defaults: the sing-box binary is resolved from
// TENEBRA_SINGBOX or located next to the current executable, and the clash API
// is polled on port 9090. Both can be overridden afterwards via the exported
// field or by setting the environment before New.
func New() *Runner {
	return &Runner{
		binOverride: os.Getenv(singboxEnv),
		ClashPort:   defaultClashPort,
		ring:        newRingBuffer(logRingSize),
	}
}

// Start launches sing-box with configJSON. It returns once the process is
// spawned; the tunnel coming up (or failing) is observed through Done. Starting
// while a process is already running is rejected — the caller is expected to
// Stop first.
func (r *Runner) Start(ctx context.Context, configJSON []byte) error {
	bin, err := r.resolveSingbox()
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := ensureWintun(filepath.Dir(bin)); err != nil {
			return err
		}
	}

	cfgPath, err := writeConfig(configJSON)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		os.Remove(cfgPath)
		return errors.New("windows: sing-box already running")
	}

	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, bin, "run", "-c", cfgPath)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		os.Remove(cfgPath)
		return fmt.Errorf("windows: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		os.Remove(cfgPath)
		return fmt.Errorf("windows: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		os.Remove(cfgPath)
		return fmt.Errorf("windows: start sing-box: %w", err)
	}

	done := make(chan error, 1)
	r.cmd = cmd
	r.cancel = cancel
	r.done = done
	r.cfgPath = cfgPath

	// Drain both streams into the ring buffer; the goroutines end when the pipes
	// close on process exit.
	go r.scan(stdout)
	go r.scan(stderr)

	// One watcher owns Wait. It publishes the exit on done, closes it, and clears
	// the running state so the Runner can be started again.
	go func() {
		werr := cmd.Wait()
		cancel()
		os.Remove(cfgPath)

		r.mu.Lock()
		if r.cmd == cmd { // still the current process, not superseded
			r.cmd = nil
			r.cancel = nil
			r.cfgPath = ""
		}
		r.mu.Unlock()

		done <- werr
		close(done)
	}()

	return nil
}

// Stop terminates the running process and waits for it to exit. It is
// idempotent: with nothing running it returns nil.
func (r *Runner) Stop() error {
	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	running := r.cmd != nil
	r.mu.Unlock()

	if !running {
		return nil
	}
	if cancel != nil {
		cancel() // signals exec to kill the process group
	}
	// Wait for the watcher to observe the exit so Stop doesn't race ahead of
	// cleanup. done is buffered and always closed by the watcher.
	if done != nil {
		<-done
	}
	return nil
}

// Done reports the next process exit. Before any Start it blocks forever; after
// a Start it returns that run's channel, which delivers the exit error once and
// is then closed.
func (r *Runner) Done() <-chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done == nil {
		return blocked
	}
	return r.done
}

// Stats fetches cumulative byte counters from the clash API. The API only
// listens once sing-box has started, so an error here is expected during
// startup and treated as non-fatal by the caller.
func (r *Runner) Stats() (up, down int64, err error) {
	port := r.ClashPort
	if port == 0 {
		port = defaultClashPort
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/connections", port)

	client := &http.Client{Timeout: statsTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return 0, 0, fmt.Errorf("windows: clash stats: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("windows: clash stats: status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, 0, fmt.Errorf("windows: clash stats: read: %w", err)
	}
	return parseConnections(body)
}

// Logs returns a copy of the most recent sing-box output lines, newest last, for
// diagnostics.
func (r *Runner) Logs() []string {
	r.mu.Lock()
	ring := r.ring
	r.mu.Unlock()
	if ring == nil {
		return nil
	}
	return ring.snapshot()
}

// scan copies a process stream line by line into the ring buffer.
func (r *Runner) scan(rc io.ReadCloser) {
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		r.ring.add(sc.Text())
	}
}

// resolveSingbox returns the sing-box binary path: the override (env or field)
// if set, otherwise the platform-named binary sitting next to this executable.
func (r *Runner) resolveSingbox() (string, error) {
	if r.binOverride != "" {
		return r.binOverride, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("windows: locate executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), singboxBinaryName()), nil
}

// singboxBinaryName is the expected sing-box filename for the current platform.
func singboxBinaryName() string {
	if runtime.GOOS == "windows" {
		return "sing-box.exe"
	}
	return "sing-box"
}

// ensureWintun makes sure wintun.dll sits in binDir, copying it from beside the
// current executable when missing. The tun device on Windows loads wintun.dll
// from the directory of the running binary, so sing-box needs it alongside.
func ensureWintun(binDir string) error {
	dst := filepath.Join(binDir, "wintun.dll")
	if _, err := os.Stat(dst); err == nil {
		return nil // already present
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("windows: locate executable: %w", err)
	}
	src := filepath.Join(filepath.Dir(exe), "wintun.dll")
	if src == dst {
		return nil
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("windows: wintun.dll not found beside executable: %w", err)
	}
	if err := copyFile(dst, src); err != nil {
		return fmt.Errorf("windows: place wintun.dll: %w", err)
	}
	return nil
}

// writeConfig writes config JSON to a temp file and returns its path. The caller
// (the watcher) removes it when the process exits.
func writeConfig(configJSON []byte) (string, error) {
	f, err := os.CreateTemp("", "tenebra-singbox-*.json")
	if err != nil {
		return "", fmt.Errorf("windows: create config file: %w", err)
	}
	if _, err := f.Write(configJSON); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("windows: write config file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("windows: close config file: %w", err)
	}
	return f.Name(), nil
}

// copyFile copies src to dst, creating or truncating dst. It flushes to disk so
// a freshly placed wintun.dll is fully written before sing-box loads it.
func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// connections is the subset of the clash API /connections payload we read.
type connections struct {
	DownloadTotal int64 `json:"downloadTotal"`
	UploadTotal   int64 `json:"uploadTotal"`
}

// parseConnections extracts cumulative upload/download totals from a clash API
// /connections body.
func parseConnections(body []byte) (up, down int64, err error) {
	var c connections
	if err := json.Unmarshal(body, &c); err != nil {
		return 0, 0, fmt.Errorf("windows: parse clash stats: %w", err)
	}
	return c.UploadTotal, c.DownloadTotal, nil
}

// ringBuffer is a fixed-capacity FIFO of recent log lines, safe for concurrent
// writes from the two stream scanners and reads from Logs.
type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	cap   int
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{cap: capacity}
}

func (b *ringBuffer) add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lines) == b.cap {
		copy(b.lines, b.lines[1:])
		b.lines[len(b.lines)-1] = line
		return
	}
	b.lines = append(b.lines, line)
}

func (b *ringBuffer) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}
