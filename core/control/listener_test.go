package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/profile"
)

// memListener is an in-memory net.Listener: dial hands the server half of a
// net.Pipe to Accept. It lets the listener loop be tested without any OS
// transport; the Windows named-pipe tests exercise the same loop over real
// pipes.
type memListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newMemListener() *memListener {
	return &memListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case c := <-l.conns:
		return c, nil
	}
}

func (l *memListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *memListener) Addr() net.Addr { return fakeAddr{} }

// listenerHarness runs ServeListener over a memListener with a fake runner, so
// tests can dial sessions and watch the daemon survive them.
type listenerHarness struct {
	t      *testing.T
	daemon *Daemon
	runner *fakeRunner
	lis    *memListener
	cancel context.CancelFunc
	done   chan error

	// awaitDone is called by both tests and the cleanup, but ServeListener
	// returns exactly once; cache its result.
	doneOnce sync.Once
	doneErr  error
}

func newListenerHarness(t *testing.T) *listenerHarness {
	t.Helper()
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	runner := newFakeRunner()
	d := NewDaemon(store, runner)
	// Same shrunk fallback timings as the Server harness, for the same reason.
	d.probeWarmup = time.Millisecond
	d.probeRetry = time.Millisecond
	d.probeTimeout = 200 * time.Millisecond
	d.probeBudget = 60 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	h := &listenerHarness{
		t:      t,
		daemon: d,
		runner: runner,
		lis:    newMemListener(),
		cancel: cancel,
		done:   make(chan error, 1),
	}
	go func() { h.done <- ServeListener(ctx, d, h.lis) }()
	t.Cleanup(func() {
		cancel()
		h.awaitDone()
	})
	return h
}

// dial opens a new client session against the listener.
func (h *listenerHarness) dial() *lineClient {
	h.t.Helper()
	c, s := net.Pipe()
	select {
	case h.lis.conns <- s:
	case <-time.After(3 * time.Second):
		h.t.Fatal("listener never accepted the connection")
	}
	return newLineClient(h.t, c)
}

// awaitDone waits for ServeListener to return and reports its error, caching
// the result so cleanup can call it again after a test already has.
func (h *listenerHarness) awaitDone() error {
	h.t.Helper()
	h.doneOnce.Do(func() {
		select {
		case h.doneErr = <-h.done:
		case <-time.After(3 * time.Second):
			h.t.Fatal("ServeListener did not return")
		}
	})
	return h.doneErr
}

// lineClient is one client end of a session: it sends request lines and
// demultiplexes the response/event stream, mirroring what a UI does. gone is
// closed when the server side ends the stream.
type lineClient struct {
	t      *testing.T
	conn   net.Conn
	resp   chan Response
	events chan map[string]any
	gone   chan struct{}
}

func newLineClient(t *testing.T, conn net.Conn) *lineClient {
	c := &lineClient{
		t:      t,
		conn:   conn,
		resp:   make(chan Response, 64),
		events: make(chan map[string]any, 256),
		gone:   make(chan struct{}),
	}
	go c.readLoop()
	t.Cleanup(func() { _ = conn.Close() })
	return c
}

func (c *lineClient) readLoop() {
	defer close(c.gone)
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		if _, isEvent := probe["event"]; isEvent {
			// Best-effort, as in the Server harness: never block the reader on a
			// full buffer, or a live connection's traffic ticker would wedge it.
			select {
			case c.events <- probe:
			default:
			}
			continue
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err == nil {
			c.resp <- resp
		}
	}
}

func (c *lineClient) send(req Request) {
	c.t.Helper()
	line, err := marshalLine(req)
	if err != nil {
		c.t.Fatalf("marshal request: %v", err)
	}
	if _, err := c.conn.Write(line); err != nil {
		c.t.Fatalf("write request: %v", err)
	}
}

func (c *lineClient) await() Response {
	c.t.Helper()
	select {
	case r := <-c.resp:
		return r
	case <-time.After(3 * time.Second):
		c.t.Fatal("timed out waiting for response")
		return Response{}
	}
}

func (c *lineClient) awaitState(want ConnState) map[string]any {
	c.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-c.events:
			if ev["event"] == EventState && ev["state"] == string(want) {
				return ev
			}
		case <-deadline:
			c.t.Fatalf("timed out waiting for state=%s event", want)
			return nil
		}
	}
}

// awaitGone waits for the server to end this client's stream.
func (c *lineClient) awaitGone() {
	c.t.Helper()
	select {
	case <-c.gone:
	case <-time.After(3 * time.Second):
		c.t.Fatal("session was not closed by the server")
	}
}

// dataInto decodes a response's data payload into v.
func (c *lineClient) dataInto(r Response, v any) {
	c.t.Helper()
	if !r.Ok {
		c.t.Fatalf("response not ok: %s", r.Error)
	}
	if err := json.Unmarshal(r.Data, v); err != nil {
		c.t.Fatalf("decode data: %v", err)
	}
}

// importAndConnect imports the fake one-node profile and brings the tunnel up
// through this client, returning once the connected state event arrived.
func (c *lineClient) importAndConnect() profile.Profile {
	c.t.Helper()
	c.send(Request{ID: 1, Cmd: CmdImportLink, Link: fakeVLESSLink(), Name: "Test"})
	var out struct {
		Profile profile.Profile `json:"profile"`
	}
	c.dataInto(c.await(), &out)
	c.send(Request{ID: 2, Cmd: CmdConnect, Profile: out.Profile.ID})
	c.await()
	c.awaitState(StateConnected)
	return out.Profile
}

func TestServeListenerRoundTrip(t *testing.T) {
	h := newListenerHarness(t)
	c := h.dial()
	c.send(Request{ID: 7, Cmd: CmdStatus})
	r := c.await()
	if r.ID != 7 || !r.Ok {
		t.Fatalf("status response = %+v, want ok with id 7", r)
	}
	var st State
	c.dataInto(r, &st)
	if st.State != StateIdle {
		t.Errorf("initial state = %q, want idle", st.State)
	}
}

// TestServeListenerTakeover: a second connection displaces the first — the old
// stream is closed, the new one gets responses and events — and the daemon's
// state (settings and the live tunnel) carries over untouched.
func TestServeListenerTakeover(t *testing.T) {
	h := newListenerHarness(t)

	a := h.dial()
	a.send(Request{ID: 1, Cmd: CmdSetRouting, Mode: "global"})
	a.await()
	a.importAndConnect()
	// Every connect includes one Stop (startConnect's teardown of the previous,
	// possibly absent, process); snapshot the count so the assertions below
	// measure only what the takeover itself did.
	stops := h.runner.stops()

	b := h.dial()
	// The server must close A's stream once B is in.
	a.awaitGone()

	b.send(Request{ID: 10, Cmd: CmdStatus})
	var st State
	b.dataInto(b.await(), &st)
	if st.State != StateConnected {
		t.Errorf("state after takeover = %q, want connected (tunnel must survive)", st.State)
	}
	if st.Routing != "global" {
		t.Errorf("routing after takeover = %q, want global (settings must survive)", st.Routing)
	}
	if got := h.runner.stops(); got != stops {
		t.Errorf("takeover stopped the runner (%d -> %d stops), want none", stops, got)
	}

	// Events must now flow to B: a disconnect emits an idle state event.
	b.send(Request{ID: 11, Cmd: CmdDisconnect})
	b.await()
	b.awaitState(StateIdle)
}

// TestServeListenerClientEOFKeepsTunnel: a client going away (UI exit or crash)
// must not tear the tunnel down; the next client finds it still connected.
func TestServeListenerClientEOFKeepsTunnel(t *testing.T) {
	h := newListenerHarness(t)

	a := h.dial()
	a.importAndConnect()
	stops := h.runner.stops() // see TestServeListenerTakeover
	_ = a.conn.Close()

	b := h.dial()
	b.send(Request{ID: 5, Cmd: CmdStatus})
	var st State
	b.dataInto(b.await(), &st)
	if st.State != StateConnected {
		t.Errorf("state after client EOF = %q, want connected", st.State)
	}
	if got := h.runner.stops(); got != stops {
		t.Errorf("client EOF stopped the runner (%d -> %d stops), want none", stops, got)
	}
}

// TestServeListenerCancelClosesDaemon: cancelling the listener context is the
// service stopping — the tunnel is torn down and ServeListener returns.
func TestServeListenerCancelClosesDaemon(t *testing.T) {
	h := newListenerHarness(t)

	a := h.dial()
	a.importAndConnect()

	h.cancel()
	if err := h.awaitDone(); !errors.Is(err, context.Canceled) {
		t.Errorf("ServeListener returned %v, want context.Canceled", err)
	}
	if h.runner.stops() == 0 {
		t.Error("runner was not stopped on listener shutdown")
	}
	a.awaitGone()
}

// TestServeListenerAcceptFailureClosesDaemon: a listener that dies out from
// under the loop must still tear the daemon down before the error surfaces.
func TestServeListenerAcceptFailureClosesDaemon(t *testing.T) {
	h := newListenerHarness(t)

	a := h.dial()
	a.importAndConnect()

	_ = h.lis.Close()
	err := h.awaitDone()
	if err == nil || errors.Is(err, context.Canceled) {
		t.Errorf("ServeListener returned %v, want the listener failure", err)
	}
	if h.runner.stops() == 0 {
		t.Error("runner was not stopped on listener failure")
	}
}
