package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
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

// awaitClosed waits for the server to end this client's stream and returns the
// error that surfaces on this end, or os.ErrDeadlineExceeded if the stream was
// still alive after within. It probes by writing a blank line (which the request
// scanner skips) rather than by reading: reading would take delivery of the very
// frame whose stall is under test.
func (c *silentClient) awaitClosed(within time.Duration) error {
	c.t.Helper()
	deadline := time.Now().Add(within)
	for {
		_ = c.conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
		_, err := c.conn.Write([]byte("\n"))
		var ne net.Error
		if err != nil && !(errors.As(err, &ne) && ne.Timeout()) {
			return err
		}
		if time.Now().After(deadline) {
			return os.ErrDeadlineExceeded
		}
		time.Sleep(20 * time.Millisecond)
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

// TestStalledClientIsDropped: a client that never takes delivery is let go
// rather than kept as the daemon's event sink forever. Closing its stream is
// what tells it to reconnect, and a reconnect re-syncs it with a status request
// — the same recovery a takeover gives it.
func TestStalledClientIsDropped(t *testing.T) {
	h := newListenerHarness(t)
	// Production waits half a minute before writing a client off (a UI is allowed
	// to be busy); the behaviour under test is the same at any value.
	h.daemon.clientWriteTimeout = 150 * time.Millisecond

	a := h.dialSilent()
	a.send(Request{ID: 1, Cmd: CmdStatus}) // its response can never be delivered

	err := a.awaitClosed(3 * time.Second)
	if err == nil {
		t.Fatal("read returned data on a stream nobody was draining")
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("stalled client was never dropped: %v", err)
	}
}

// TestSlowClientKeepsItsResponses: shedding under pressure must cost the client
// only events, never a reply it is waiting on. The flood here overruns the queue
// several times over with one response mixed into each round.
func TestSlowClientKeepsItsResponses(t *testing.T) {
	d, _ := newTestDaemon(t)
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	srv := NewServer(d, server, server)
	t.Cleanup(srv.stopWriter)

	const rounds = 4
	go func() {
		for i := 0; i < rounds; i++ {
			for j := 0; j < outQueueDepth; j++ {
				srv.emit(EventLog, LogEvent{Level: LogInfo, Msg: "flood"})
			}
			srv.writeResponse(Response{ID: int64(i + 1), Ok: true})
		}
	}()

	seen := make(map[int64]bool)
	sc := bufio.NewScanner(client)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	_ = client.SetReadDeadline(time.Now().Add(20 * time.Second))
	for len(seen) < rounds && sc.Scan() {
		var probe map[string]any
		if err := json.Unmarshal(sc.Bytes(), &probe); err != nil {
			continue
		}
		if _, isEvent := probe["event"]; isEvent {
			continue
		}
		var r Response
		if err := json.Unmarshal(sc.Bytes(), &r); err == nil {
			seen[r.ID] = true
		}
	}
	for i := int64(1); i <= rounds; i++ {
		if !seen[i] {
			t.Errorf("response %d was dropped under pressure; only events may be shed", i)
		}
	}
}
