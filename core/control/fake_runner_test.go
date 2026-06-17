package control

import (
	"context"
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
	lastCfg []byte

	up, down int64
	statsErr error

	// done is the channel for the current "process". It is recreated on each
	// Start. Before any Start it is the never-firing channel so Done blocks.
	done chan error

	// startErr, when set, makes Start fail.
	startErr error
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

// setStats updates the cumulative counters the next Stats call returns.
func (f *fakeRunner) setStats(up, down int64) {
	f.mu.Lock()
	f.up, f.down = up, down
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
