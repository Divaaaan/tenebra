// Package byedpi runs and supervises the ByeDPI process (`ciadpi`) that backs
// the DPI-bypass mode: a plain userspace SOCKS5 proxy on loopback that the
// generated config points its bypass outbound at. The daemon hands it a command
// line, it spawns the process, keeps a tail of its output for diagnostics, and
// reports the exit.
//
// It deliberately does not satisfy control.Runner. That interface also carries
// Stats and Probe, which read sing-box's clash API; ByeDPI exposes no control
// surface at all, so pretending it does would mean answering those calls with
// invented numbers. Reachability is answered honestly instead, by speaking to
// the listener (see Ready).
//
// Like adapters/windows, this package uses nothing OS-specific at the source
// level (no syscall, no x/sys) so it compiles and vets on every platform CI
// builds; Windows-only behaviour is gated on runtime.GOOS at run time.
package byedpi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"reflect"
	"runtime"
	"sync"
	"time"

	"github.com/Divaaaan/tenebra/core/dpi"
)

// logRingSize bounds the in-memory tail of ByeDPI output kept for diagnostics.
const logRingSize = 200

// dialTimeout bounds one readiness check, so a probe cannot hold up a connect
// when the listener accepts but never answers.
const dialTimeout = 2 * time.Second

// readyPoll is how often WaitReady retries while the process is still coming
// up. ByeDPI binds its listener within milliseconds of starting, so a short
// interval keeps the wait from adding perceptible latency to a connect.
const readyPoll = 50 * time.Millisecond

// The two bytes of a SOCKS5 no-auth handshake worth naming: the version and the
// "no authentication required" method. ByeDPI's listener supports no other.
const (
	socksVersion = 0x05
	socksNoAuth  = 0x00
)

// createNoWindow is Windows' CREATE_NO_WINDOW process creation flag. ciadpi is
// a console program, and the desktop shell that spawns this core is not, so
// without the flag every start pops a console window in the user's face. The
// Rust shell passes the same flag when it spawns us (see backend/sidecar.rs);
// it does not descend to grandchildren, so it has to be repeated here.
const createNoWindow = 0x08000000

// blocked is returned by Done before the first Start. It has no sender and is
// never closed, so a receive on it blocks forever — exactly the "Done must
// block before any Start" contract, without allocating a channel per caller.
var blocked = make(chan error)

// Runner spawns and supervises one ByeDPI process at a time. The zero value is
// not usable; build it with New. It is safe for concurrent use: Done, Logs and
// the readiness checks may be called while Start or Stop runs.
type Runner struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan error
	ring   *ringBuffer
}

// New builds a Runner. The binary is resolved at each Start through
// dpi.ResolveBinary, so the TENEBRA_BYEDPI override applies to a process that
// learns its resource path after startup.
func New() *Runner {
	return &Runner{ring: newRingBuffer(logRingSize)}
}

// Start launches ByeDPI with args, which the caller renders with dpi.BuildArgs
// — this runner spawns whatever it is given and does no validation of its own,
// so the argv must already have been through that allowlist. It returns once
// the process is spawned; the listener actually coming up is observed through
// Ready, and the process dying through Done. Starting while one already runs is
// rejected: the caller is expected to Stop first.
func (r *Runner) Start(ctx context.Context, args []string) error {
	bin, err := dpi.ResolveBinary()
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		return errors.New("byedpi: bypass process already running")
	}

	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, bin, args...)
	hideConsole(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("byedpi: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("byedpi: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("byedpi: start %s: %w", bin, err)
	}

	done := make(chan error, 1)
	r.cmd = cmd
	r.cancel = cancel
	r.done = done

	// Drain both streams into the ring buffer; the goroutines end when the pipes
	// close on process exit.
	var streams sync.WaitGroup
	streams.Add(2)
	go func() {
		defer streams.Done()
		r.scan(stdout)
	}()
	go func() {
		defer streams.Done()
		r.scan(stderr)
	}()

	// One watcher owns Wait. It publishes the exit on done, closes it, and
	// clears the running state so the Runner can be started again. Wait closes
	// the pipes, so the scanners are joined first — otherwise the tail of a
	// failing process's output, which is the only explanation the user gets,
	// races with the reap and is lost. Joining first assumes the streams reach
	// EOF when the process dies, which holds because ByeDPI spawns no children
	// to inherit the handles; a helper that did would have to be reaped on a
	// budget instead.
	go func() {
		streams.Wait()
		werr := cmd.Wait()
		cancel()

		r.mu.Lock()
		if r.cmd == cmd { // still the current process, not superseded
			r.cmd = nil
			r.cancel = nil
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
		cancel() // signals exec to kill the process
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

// Logs returns a copy of the most recent ByeDPI output lines, newest last, for
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

// Ready reports whether the bypass listener at addr is actually serving.
//
// It dials and completes the SOCKS5 no-auth greeting rather than just opening a
// connection. A bare dial only proves something holds the port, and the port is
// picked before the process binds it (see dpi.FreePort), so an unrelated
// listener that grabbed it in between would pass — and traffic would then be
// routed into it. Answering the greeting is proof it is a SOCKS5 proxy taking
// unauthenticated clients, which is what the generated config expects. It is
// still not proof that a bypass works end to end: whether a given strategy
// defeats a given filter is only answered by real traffic.
func (r *Runner) Ready(ctx context.Context, addr string) error {
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("byedpi: dial bypass listener %s: %w", addr, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(dialTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("byedpi: bypass listener %s: %w", addr, err)
	}

	if _, err := conn.Write([]byte{socksVersion, 1, socksNoAuth}); err != nil {
		return fmt.Errorf("byedpi: greet bypass listener %s: %w", addr, err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return fmt.Errorf("byedpi: bypass listener %s did not answer the SOCKS5 greeting: %w", addr, err)
	}
	if reply[0] != socksVersion || reply[1] != socksNoAuth {
		return fmt.Errorf("byedpi: %s answered %#02x %#02x, which is not SOCKS5 without authentication", addr, reply[0], reply[1])
	}
	return nil
}

// WaitReady retries Ready until the listener answers, the wait budget runs out,
// or the process dies. The last case is the point of tying the wait to the
// runner: a ByeDPI that failed to bind exits immediately, and a caller that
// only watched the socket would sit out the whole budget before finding out.
// It is meant to be called after Start; with no process running it fails at
// once rather than waiting for one that is never coming.
func (r *Runner) WaitReady(ctx context.Context, addr string, within time.Duration) error {
	deadline := time.Now().Add(within)
	var last error

	for {
		if !r.running() {
			if last != nil {
				return fmt.Errorf("byedpi: bypass process is not running, %s never answered: %w", addr, last)
			}
			return fmt.Errorf("byedpi: bypass process is not running, cannot wait for %s", addr)
		}
		err := r.Ready(ctx, addr)
		if err == nil {
			return nil
		}
		last = err
		if ctx.Err() != nil {
			return fmt.Errorf("byedpi: waiting for bypass listener %s: %w", addr, ctx.Err())
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("byedpi: bypass listener %s did not come up within %s: %w", addr, within, last)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("byedpi: waiting for bypass listener %s: %w", addr, ctx.Err())
		case <-time.After(readyPoll):
		}
	}
}

// running reports whether a process is currently supervised.
func (r *Runner) running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd != nil
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

// hideConsole sets CREATE_NO_WINDOW on cmd when running on Windows.
//
// That flag lives in exec.Cmd.SysProcAttr, whose type is platform-specific, so
// naming it in the source would pull in syscall and pin this file to one GOOS —
// and the package is deliberately buildable and vettable everywhere (see the
// package comment). Reflection reaches the field on Windows and finds nothing
// to set anywhere else, which keeps one file for all platforms. The fields are
// exported, so this reads the documented API rather than poking at internals,
// and every step is guarded: an absent or reshaped field leaves the spawn
// untouched instead of panicking.
func hideConsole(cmd *exec.Cmd) {
	if runtime.GOOS != "windows" {
		return
	}
	attr := reflect.ValueOf(cmd).Elem().FieldByName("SysProcAttr")
	if !attr.IsValid() || attr.Kind() != reflect.Pointer || !attr.CanSet() {
		return
	}
	if attr.IsNil() {
		attr.Set(reflect.New(attr.Type().Elem()))
	}
	flags := attr.Elem().FieldByName("CreationFlags")
	if !flags.IsValid() || !flags.CanSet() || !flags.CanUint() {
		return
	}
	flags.SetUint(flags.Uint() | createNoWindow)
}

// ringBuffer is a fixed-capacity FIFO of recent log lines, safe for concurrent
// writes from the two stream scanners and reads from Logs.
type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	cap   int
}

// newRingBuffer returns an empty ring that retains the most recent capacity
// lines.
func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{cap: capacity}
}

// add appends a line, dropping the oldest once the buffer is at capacity.
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

// snapshot returns a copy of the buffered lines, oldest first, so callers can
// read them without holding the lock or aliasing the backing array.
func (b *ringBuffer) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}
