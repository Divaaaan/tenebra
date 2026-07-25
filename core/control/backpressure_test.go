package control

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// The tests in this file pin down what must happen when a client stops draining
// its stream — the failure that took a running Windows service off the air:
// the process stayed alive and the service stayed Running, but the control pipe
// stopped accepting anyone (every dial died with ERROR_SEM_TIMEOUT, "all pipe
// instances are busy"), so the UI rendered and nothing in it worked. Only a
// service restart brought it back.
//
// Two independent mechanisms lead there, and both are covered below:
//
//   - a write to a client that isn't reading blocks forever (the production
//     named pipe has a zero-byte buffer, so the very first frame to a stalled
//     client stalls), taking with it whichever goroutine emitted it — including
//     the accept loop itself, which emits peer-authentication diagnostics before
//     it displaces the previous session;
//   - displacing the previous session waited for that session's goroutine to
//     finish, so any command still in flight (a settings hot-swap tears the
//     tunnel down and waits for the fallback walk to drain, none of which
//     watches the request context) held the accept loop for its whole duration.

// silentClient is a client that connects and then never reads a byte, modelling
// a UI whose reader has stalled — the state that turns an unbounded write into
// a permanent stall.
type silentClient struct {
	t    *testing.T
	conn net.Conn
}

// dialSilent opens a session whose client end is never read from.
func (h *listenerHarness) dialSilent() *silentClient {
	h.t.Helper()
	c, s := net.Pipe()
	select {
	case h.lis.conns <- s:
	case <-time.After(3 * time.Second):
		h.t.Fatal("listener never accepted the connection")
	}
	h.t.Cleanup(func() { _ = c.Close() })
	return &silentClient{t: h.t, conn: c}
}

// send writes a request. The write itself is bounded so a wedged listener fails
// the test instead of hanging it out to the package timeout.
func (c *silentClient) send(req Request) {
	c.t.Helper()
	line, err := marshalLine(req)
	if err != nil {
		c.t.Fatalf("marshal request: %v", err)
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.conn.Write(line); err != nil {
		c.t.Fatalf("write request: %v", err)
	}
	_ = c.conn.SetWriteDeadline(time.Time{})
}

// awaitClosed reports whether the server ended this client's stream within d.
func (c *silentClient) awaitClosed(d time.Duration) error {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 4096)
	for {
		if _, err := c.conn.Read(buf); err != nil {
			return err
		}
	}
}

// trySend writes a request with a deadline, so a client whose session was never
// started (nothing on the other end reads) reports a timeout rather than
// blocking the test forever.
func trySend(t *testing.T, c *lineClient, req Request, within time.Duration) error {
	t.Helper()
	line, err := marshalLine(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(within)); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}
	defer func() { _ = c.conn.SetWriteDeadline(time.Time{}) }()
	_, err = c.conn.Write(line)
	return err
}

// TestEmitDoesNotBlockOnAClientThatStoppedReading is the root of the first
// mechanism. Events are emitted by goroutines that have nothing to do with the
// client — the traffic poller, the fallback walk, the health watchdog, and (on
// Windows) the accept loop's peer-authentication logging. None of them may be
// held hostage by a UI that stopped reading, because the client's stream is not
// theirs to wait on.
func TestEmitDoesNotBlockOnAClientThatStoppedReading(t *testing.T) {
	d, _ := newTestDaemon(t)

	client, server := net.Pipe() // an unbuffered pipe: a write needs a reader
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	NewServer(d, server, server) // installs the daemon's emitter

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 4; i++ {
			d.emitLog(LogInfo, "tick")
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("emitting to a client that stopped reading blocked the emitter")
	}
}

// TestServeListenerServesANewClientWhileACommandIsInFlight covers the second
// mechanism: a command that is still running must not keep the listener from
// taking the next client. The blocked handler here stands in for what the real
// ones do — a settings hot-swap synchronously tears the tunnel down and waits
// for the fallback walk to drain under a background context, so cancelling the
// session does not shorten it.
func TestServeListenerServesANewClientWhileACommandIsInFlight(t *testing.T) {
	h := newListenerHarness(t)

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	t.Cleanup(func() { close(release) })
	h.daemon.httpGet = func(context.Context, string) ([]byte, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return nil, errors.New("released")
	}

	a := h.dial()
	a.send(Request{ID: 1, Cmd: CmdLeakCheck})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("leak_check never reached the injected getter")
	}

	// A's session goroutine is now inside its command. B must still be served.
	b := h.dial()
	if err := trySend(t, b, Request{ID: 2, Cmd: CmdStatus}, 3*time.Second); err != nil {
		t.Fatalf("the new client's session never started: %v", err)
	}
	r := b.await()
	if r.ID != 2 || !r.Ok {
		t.Fatalf("status response = %+v, want ok with id 2", r)
	}
}

// TestServeListenerServesANewClientWhileTheOldStreamIsStalled is the first
// mechanism seen through the listener. The stalled client leaves an unfinished
// write in the server; the accept loop must neither wait on it nor add one of
// its own (on Windows it emits a peer-authentication log line before displacing
// the old session, straight into that stalled stream).
func TestServeListenerServesANewClientWhileTheOldStreamIsStalled(t *testing.T) {
	h := newListenerHarness(t)

	a := h.dialSilent()
	a.send(Request{ID: 1, Cmd: CmdStatus}) // its response can never be delivered
	// Give the server a moment to block on that response.
	time.Sleep(100 * time.Millisecond)

	b := h.dial()
	if err := trySend(t, b, Request{ID: 2, Cmd: CmdStatus}, 3*time.Second); err != nil {
		t.Fatalf("the new client's session never started: %v", err)
	}
	r := b.await()
	if r.ID != 2 || !r.Ok {
		t.Fatalf("status response = %+v, want ok with id 2", r)
	}
}
