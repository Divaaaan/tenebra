package control

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// fakeConn is a no-op net.Conn returned by an injected dialer in ping tests.
type fakeConn struct{}

func (fakeConn) Read([]byte) (int, error)         { return 0, nil }
func (fakeConn) Write(b []byte) (int, error)      { return len(b), nil }
func (fakeConn) Close() error                     { return nil }
func (fakeConn) LocalAddr() net.Addr              { return fakeAddr{} }
func (fakeConn) RemoteAddr() net.Addr             { return fakeAddr{} }
func (fakeConn) SetDeadline(time.Time) error      { return nil }
func (fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (fakeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr struct{}

func (fakeAddr) Network() string { return "tcp" }
func (fakeAddr) String() string  { return "127.0.0.1:0" }

// fakeRunner is an in-memory Runner for tests. It records Start/Stop, lets a
// test feed cumulative stats and trigger a simulated process exit, and honours
// the Runner contract: Done blocks before the first Start, fires once per
// process, and the runner is reusable across Start/Stop cycles.
type fakeRunner struct {
	mu sync.Mutex

	startN  int
	stopN   int
	probeN  int
	lastCfg []byte
	cfgs    [][]byte // every config Start was called with, in order

	up, down int64
	statsErr error

	// connectionsJSON is the clash /connections body ConnectionsJSON hands back
	// and connectionsErr the failure it reports instead; connectionsN counts the
	// calls, so a test can prove the daemon never reached the API when there was
	// no tunnel to ask about.
	connectionsJSON []byte
	connectionsErr  error
	connectionsN    int

	// logs is the sing-box output tail Logs returns. Tests seed it to exercise the
	// diagnostic tail the daemon emits when a connect exhausts; empty by default.
	logs []string

	// done is the channel for the current "process". It is recreated on each
	// Start. Before any Start it is the never-firing channel so Done blocks.
	done chan error

	// startErr, when set, makes Start fail.
	startErr error

	// failStarts makes every probe fail while the current process is one of the
	// first that-many Starts — i.e. the first failStarts candidates are treated as
	// blocked (no matter how many times the loop retries them within their budget),
	// and candidate failStarts+1 onward comes up. This models a blocked protocol
	// the fallback loop must skip, rather than a transient probe blip. probeErr is
	// the error returned for a failed probe (a default is used when nil).
	// probeDelay is the delayMs a successful probe reports.
	failStarts int
	probeErr   error
	probeDelay int

	// onProbe, if set, is called (with the lock released) at the start of each
	// Probe, letting a test drive timing — e.g. block until it chooses, or trigger
	// a process exit. n is the cumulative probe count. It runs before the
	// fail/success decision.
	onProbe func(ctx context.Context, n int)
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{done: make(chan error)} // never fires until Start
}

func (f *fakeRunner) Start(ctx context.Context, configJSON []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.startN++
	f.lastCfg = append([]byte(nil), configJSON...)
	f.cfgs = append(f.cfgs, append([]byte(nil), configJSON...))
	f.done = make(chan error, 1)
	return nil
}

func (f *fakeRunner) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopN++
	return nil
}

func (f *fakeRunner) Stats() (int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.up, f.down, f.statsErr
}

func (f *fakeRunner) ConnectionsJSON(ctx context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectionsN++
	if f.connectionsErr != nil {
		return nil, f.connectionsErr
	}
	return append([]byte(nil), f.connectionsJSON...), nil
}

func (f *fakeRunner) Done() <-chan error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done
}

func (f *fakeRunner) Probe(ctx context.Context, tag string) (int, error) {
	f.mu.Lock()
	f.probeN++
	n := f.probeN
	hook := f.onProbe
	blocked := f.probeErr != nil || f.startN <= f.failStarts
	perr := f.probeErr
	delay := f.probeDelay
	f.mu.Unlock()

	if hook != nil {
		hook(ctx, n)
	}
	// Honour ctx so a probe abandoned by teardown returns promptly.
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if blocked {
		if perr != nil {
			return 0, perr
		}
		return 0, errors.New("fake probe: blocked")
	}
	return delay, nil
}

// Logs returns a copy of the seeded sing-box output tail, satisfying the
// control.Runner contract (safe any time, newest last, empty when unset).
func (f *fakeRunner) Logs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.logs))
	copy(out, f.logs)
	return out
}

// setStats updates the cumulative counters the next Stats call returns.
func (f *fakeRunner) setStats(up, down int64) {
	f.mu.Lock()
	f.up, f.down = up, down
	f.mu.Unlock()
}

// setConnections seeds what ConnectionsJSON answers: a clash /connections body,
// or an error standing in for an API that is not there.
func (f *fakeRunner) setConnections(body []byte, err error) {
	f.mu.Lock()
	f.connectionsJSON = append([]byte(nil), body...)
	f.connectionsErr = err
	f.mu.Unlock()
}

// connectionsCalls reports how many times ConnectionsJSON was asked.
func (f *fakeRunner) connectionsCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectionsN
}

// setLogs seeds the sing-box output tail Logs returns.
func (f *fakeRunner) setLogs(lines ...string) {
	f.mu.Lock()
	f.logs = append([]string(nil), lines...)
	f.mu.Unlock()
}

// setFailStarts updates failStarts under the lock, safe to call between connects
// while a probe goroutine from a prior connection may still be unwinding.
func (f *fakeRunner) setFailStarts(n int) {
	f.mu.Lock()
	f.failStarts = n
	f.mu.Unlock()
}

// exit simulates the sing-box process terminating with err.
func (f *fakeRunner) exit(err error) {
	f.mu.Lock()
	ch := f.done
	f.mu.Unlock()
	ch <- err
}

func (f *fakeRunner) starts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startN
}

func (f *fakeRunner) stops() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopN
}

func (f *fakeRunner) probes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probeN
}

// startCfgs returns a copy of every config Start was handed, in order.
func (f *fakeRunner) startCfgs() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.cfgs))
	copy(out, f.cfgs)
	return out
}
