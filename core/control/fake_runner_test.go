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

	// selects records every Select call, in order, so a test can assert what the
	// live selector was pointed at (and, by their absence, that nothing was
	// steered). selectErr, when set, makes every Select fail — the runner that
	// cannot steer, which must degrade to a full reconnect.
	selects   []selectCall
	selectErr error

	// viaDelays and viaErrs script ProbeVia per outbound tag: a tag present in
	// viaErrs fails, otherwise the delay from viaDelays (or viaDefault) is
	// returned. viaErrAll fails every tag — the field where no exit carries
	// traffic. viaCalls records every call so a scan's shape is assertable.
	viaDelays  map[string]int
	viaErrs    map[string]error
	viaErrAll  error
	viaDefault int
	viaCalls   []viaCall
}

// selectCall is one recorded Select: the group steered and the outbound it was
// pointed at.
type selectCall struct {
	group string
	tag   string
}

// viaCall is one recorded ProbeVia: the outbound measured and the destination it
// was measured against.
type viaCall struct {
	tag    string
	target string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		done:       make(chan error), // never fires until Start
		viaDelays:  map[string]int{},
		viaErrs:    map[string]error{},
		viaDefault: 20,
	}
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

// Select records the requested selection and reports the scripted outcome. A
// successful Select does NOT change what Probe answers: a runner that accepts a
// selection and then carries nothing is exactly the case the switch path has to
// catch, so tests drive the two independently.
func (f *fakeRunner) Select(ctx context.Context, group, tag string) error {
	f.mu.Lock()
	f.selects = append(f.selects, selectCall{group: group, tag: tag})
	err := f.selectErr
	f.mu.Unlock()
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

// ProbeVia answers per-outbound: a tag scripted in viaErrs fails, anything else
// returns its scripted delay (or viaDefault).
func (f *fakeRunner) ProbeVia(ctx context.Context, tag, target string) (int, error) {
	f.mu.Lock()
	f.viaCalls = append(f.viaCalls, viaCall{tag: tag, target: target})
	err := f.viaErrs[tag]
	if err == nil {
		err = f.viaErrAll
	}
	delay, ok := f.viaDelays[tag]
	if !ok {
		delay = f.viaDefault
	}
	f.mu.Unlock()
	if cerr := ctx.Err(); cerr != nil {
		return 0, cerr
	}
	if err != nil {
		return 0, err
	}
	return delay, nil
}

// selectCalls returns a copy of every Select the runner was asked to make.
func (f *fakeRunner) selectCalls() []selectCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]selectCall, len(f.selects))
	copy(out, f.selects)
	return out
}

// viaProbes returns a copy of every ProbeVia call, in order.
func (f *fakeRunner) viaProbes() []viaCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]viaCall, len(f.viaCalls))
	copy(out, f.viaCalls)
	return out
}

// failAllVia scripts ProbeVia to fail for every outbound: the machine where no
// exit carries traffic, so a degradation has nowhere better to go.
func (f *fakeRunner) failAllVia() {
	f.mu.Lock()
	f.viaErrAll = errors.New("fake probe via: blocked")
	f.mu.Unlock()
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

// setStatsErr makes the clash reads fail from now on, the way a wedged or
// unparseable API does.
func (f *fakeRunner) setStatsErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statsErr = err
}
