//go:build windows

package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// testPipeName returns a per-run unique pipe name so tests never collide with
// the production pipe or with a parallel test process.
func testPipeName() string {
	return fmt.Sprintf(`\\.\pipe\tenebra-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
}

// requirePipeAccess skips the test when the current token holds none of the
// identities the pipe DACL admits (INTERACTIVE, Administrators, SYSTEM).
// Normal dev shells and CI runners are interactive; a bare network-logon
// context (some ssh setups) is not, and its dial would fail with ACCESS_DENIED
// through no fault of the code under test.
func requirePipeAccess(t *testing.T) {
	t.Helper()
	token := windows.GetCurrentProcessToken()
	for _, s := range []string{"S-1-5-4", "S-1-5-32-544", "S-1-5-18"} { // INTERACTIVE, BA, SYSTEM
		sid, err := windows.StringToSid(s)
		if err != nil {
			continue
		}
		if member, err := token.IsMember(sid); err == nil && member {
			return
		}
	}
	t.Skip("token holds none of the pipe DACL identities (INTERACTIVE/Administrators/SYSTEM)")
}

// pipeHarness runs ServeListener over a real named pipe.
type pipeHarness struct {
	t      *testing.T
	name   string
	daemon *Daemon
	runner *fakeRunner
	cancel context.CancelFunc
	done   chan error

	doneOnce sync.Once
	doneErr  error
}

func newPipeHarness(t *testing.T) *pipeHarness {
	t.Helper()
	requirePipeAccess(t)
	name := testPipeName()
	l, err := ListenPipe(name)
	if err != nil {
		t.Fatalf("ListenPipe(%s): %v", name, err)
	}
	d, runner := newTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	h := &pipeHarness{t: t, name: name, daemon: d, runner: runner, cancel: cancel, done: make(chan error, 1)}
	go func() { h.done <- ServeListener(ctx, d, l) }()
	t.Cleanup(func() {
		cancel()
		h.awaitDone()
	})
	return h
}

// awaitDone waits for ServeListener to return, caching the result so cleanup
// can call it again after a test already has.
func (h *pipeHarness) awaitDone() error {
	h.t.Helper()
	h.doneOnce.Do(func() {
		select {
		case h.doneErr = <-h.done:
		case <-time.After(3 * time.Second):
			h.t.Error("ServeListener did not return")
		}
	})
	return h.doneErr
}

// dial connects a client to the pipe.
func (h *pipeHarness) dial() *lineClient {
	h.t.Helper()
	timeout := 3 * time.Second
	conn, err := winio.DialPipe(h.name, &timeout)
	if err != nil {
		h.t.Fatalf("DialPipe(%s): %v", h.name, err)
	}
	return newLineClient(h.t, conn)
}

// TestPipeRoundTrip: the protocol works end to end over a real named pipe with
// the production security descriptor.
func TestPipeRoundTrip(t *testing.T) {
	h := newPipeHarness(t)
	c := h.dial()
	c.send(Request{ID: 3, Cmd: CmdStatus})
	r := c.await()
	if r.ID != 3 || !r.Ok {
		t.Fatalf("status response = %+v, want ok with id 3", r)
	}
	var st State
	c.dataInto(r, &st)
	if st.State != StateIdle {
		t.Errorf("initial state = %q, want idle", st.State)
	}
}

// TestPipeTakeover: over the real pipe, a second client displaces the first,
// inherits the daemon's state, and receives its events.
func TestPipeTakeover(t *testing.T) {
	h := newPipeHarness(t)

	a := h.dial()
	a.send(Request{ID: 1, Cmd: CmdSetRouting, Mode: "global"})
	a.await()
	a.importAndConnect()
	stops := h.runner.stops() // see TestServeListenerTakeover

	b := h.dial()
	a.awaitGone()

	b.send(Request{ID: 2, Cmd: CmdStatus})
	var st State
	b.dataInto(b.await(), &st)
	if st.State != StateConnected {
		t.Errorf("state after takeover = %q, want connected", st.State)
	}
	if st.Routing != "global" {
		t.Errorf("routing after takeover = %q, want global", st.Routing)
	}
	if got := h.runner.stops(); got != stops {
		t.Errorf("takeover stopped the runner (%d -> %d stops), want none", stops, got)
	}

	b.send(Request{ID: 3, Cmd: CmdDisconnect})
	b.await()
	b.awaitState(StateIdle)
}

// TestPipeClientEOFKeepsServing: a client dropping off the pipe leaves the
// daemon (and a live tunnel) alone, and the listener accepts the next client.
func TestPipeClientEOFKeepsServing(t *testing.T) {
	h := newPipeHarness(t)

	a := h.dial()
	a.importAndConnect()
	stops := h.runner.stops()
	_ = a.conn.Close()

	b := h.dial()
	b.send(Request{ID: 4, Cmd: CmdStatus})
	var st State
	b.dataInto(b.await(), &st)
	if st.State != StateConnected {
		t.Errorf("state after client EOF = %q, want connected", st.State)
	}
	if got := h.runner.stops(); got != stops {
		t.Errorf("client EOF stopped the runner (%d -> %d stops), want none", stops, got)
	}
}

// TestPipeCancelTearsDown: cancelling the serving context (service stop) tears
// the tunnel down, ends the client's stream, and unbinds the pipe name.
func TestPipeCancelTearsDown(t *testing.T) {
	h := newPipeHarness(t)

	a := h.dial()
	a.importAndConnect()

	h.cancel()
	if err := h.awaitDone(); !errors.Is(err, context.Canceled) {
		t.Errorf("ServeListener returned %v, want context.Canceled", err)
	}
	if h.runner.stops() == 0 {
		t.Error("runner was not stopped on shutdown")
	}
	a.awaitGone()

	// The name must be free again for the next service start.
	l, err := ListenPipe(h.name)
	if err != nil {
		t.Fatalf("relisten after shutdown: %v", err)
	}
	_ = l.Close()
}

// TestPipeNameCannotBeSquatted: the listener claims the name exclusively
// (FILE_FLAG_FIRST_PIPE_INSTANCE under the hood), so a second listener — e.g.
// another process trying to stand in for the service — is refused.
func TestPipeNameCannotBeSquatted(t *testing.T) {
	requirePipeAccess(t)
	name := testPipeName()
	l, err := ListenPipe(name)
	if err != nil {
		t.Fatalf("ListenPipe(%s): %v", name, err)
	}
	defer l.Close()
	if l2, err := ListenPipe(name); err == nil {
		_ = l2.Close()
		t.Fatal("second ListenPipe on the same name succeeded, want refusal")
	}
}
