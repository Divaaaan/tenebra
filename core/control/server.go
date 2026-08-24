package control

import (
	"context"
	"fmt"
	"io"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// outQueueDepth is how many outbound frames may sit undelivered before the
// server starts shedding. It is generous on purpose — a UI that pauses for a
// moment (a GC, a modal, a slow render) must not lose anything — but bounded,
// because a client that never drains would otherwise grow this without limit
// inside a privileged daemon. At the daemon's own event rate (a traffic tick per
// second, plus bursts during a fallback walk) it is minutes of backlog.
const outQueueDepth = 512

// writeDrainGrace bounds how long a teardown waits for the outbound queue to
// flush when the underlying stream cannot be aborted. The listener path closes
// the connection first, so its writer returns at once and never spends this; it
// exists for the sidecar's stdout, which has no deadline and no Close of ours —
// a parent that stopped reading must not keep the core from exiting.
const writeDrainGrace = 2 * time.Second

// outFrame is one already-marshalled line waiting to go out. event marks the
// frames that may be shed under pressure: an event is a snapshot of something
// the client can re-derive (it re-syncs with a status request on reconnect),
// whereas a response is the other half of a request someone is waiting on.
type outFrame struct {
	line  []byte
	event bool
}

// deadlineWriter is the part of net.Conn the writer uses when the transport
// offers it. Both transports that carry this protocol to a detached client (the
// Windows named pipe and the unix socket) do; the sidecar's stdout does not, and
// falls back to an undeadlined write.
type deadlineWriter interface {
	SetWriteDeadline(t time.Time) error
}

// Server runs the control loop: it reads request lines from an io.Reader,
// dispatches each to the Daemon, and writes responses plus asynchronous events
// to an io.Writer.
//
// Outbound frames do NOT go straight to the writer. They are queued and drained
// by one goroutine, for two reasons:
//
//   - ordering: the daemon's traffic poller, health watchdog and fallback walk
//     emit events from their own goroutines while the request loop writes
//     responses; a single writer is what keeps two JSON objects off one line;
//   - isolation: the client's stream is not the daemon's to wait on. A client
//     that stops reading stalls a write indefinitely (the Windows pipe buffers
//     nothing, so the very first frame to a stalled client blocks), and every
//     emitter that touched that write used to stall with it — including the
//     accept loop in ServeListener, which emits peer-authentication lines before
//     it displaces the previous session and so wedged the whole service off the
//     air with nothing left to close the stream.
//
// A client that cannot keep up is dropped rather than waited on: a frame gets
// writeTimeout to leave, and the backlog gets outQueueDepth frames of slack.
// Past either, the stream is declared failed, its queue is discarded and
// stalled is closed so the session owner can end it. A timed-out write may have
// left half a line on the wire, so continuing to write to that client would
// corrupt its protocol — the only correct move is to close it and let it
// reconnect, which re-syncs it with a status request.
type Server struct {
	daemon *Daemon

	r io.Reader
	w io.Writer

	// writeTimeout bounds one frame's write, when the writer supports deadlines.
	// Zero disables the bound.
	writeTimeout time.Duration

	// mu guards the outbound queue and its state flags; cond wakes the writer.
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []outFrame
	closed bool // no more frames accepted; drain what is left and stop
	failed bool // the stream is unusable; drop everything

	// stalled is closed once the stream is declared failed, so the session owner
	// can tear the client down. drained is closed when the writer goroutine has
	// returned.
	stalled chan struct{}
	drained chan struct{}
}

// NewServer wires a daemon to a reader/writer pair. It installs the daemon's
// event emitter so events flow out through the same ordered writer as responses,
// and starts that writer. Every Server therefore owns a goroutine from
// construction and must be finished with stopWriter — Serve does that for the
// sidecar, startSession for a listener session.
func NewServer(d *Daemon, r io.Reader, w io.Writer) *Server {
	s := &Server{
		daemon:       d,
		r:            r,
		w:            w,
		writeTimeout: d.clientWriteTimeout,
		stalled:      make(chan struct{}),
		drained:      make(chan struct{}),
	}
	s.cond = sync.NewCond(&s.mu)
	go s.writeLoop()
	d.SetEmitter(s.emit)
	return s
}

// stalledStream is closed when the client stopped taking delivery and the stream
// was given up on. The listener's session watches it so a stalled client is
// dropped instead of holding the daemon's only event sink.
func (s *Server) stalledStream() <-chan struct{} { return s.stalled }

// writeLoop drains the outbound queue in order until the queue is closed and
// empty, or a write fails. It is the only goroutine that touches s.w.
func (s *Server) writeLoop() {
	defer close(s.drained)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed && !s.failed {
			s.cond.Wait()
		}
		if s.failed || (s.closed && len(s.queue) == 0) {
			s.mu.Unlock()
			return
		}
		fr := s.queue[0]
		s.queue[0] = outFrame{} // drop the reference so the line can be collected
		s.queue = s.queue[1:]
		if len(s.queue) == 0 {
			s.queue = nil // release the backing array once the burst has passed
		}
		s.mu.Unlock()

		if err := s.writeFrame(fr.line); err != nil {
			// The client is gone or has stopped taking delivery. Either way this
			// stream is finished; say so once and stop.
			s.mu.Lock()
			s.failStreamLocked()
			s.mu.Unlock()
			return
		}
	}
}

// writeFrame writes one line, bounded by writeTimeout when the transport
// supports deadlines. The deadline is re-armed per frame, so a queue that sat
// idle isn't judged by an old one.
func (s *Server) writeFrame(line []byte) error {
	if dw, ok := s.w.(deadlineWriter); ok && s.writeTimeout > 0 {
		_ = dw.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	}
	_, err := s.w.Write(line)
	return err
}

// enqueue puts a frame on the outbound queue. It never blocks the caller: that
// is the whole point — the daemon's event path must be independent of whether a
// client is reading. A full queue sheds the oldest event first; if the backlog
// is all responses there is nothing safe to drop and the client is given up on.
func (s *Server) enqueue(line []byte, event bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed {
		return
	}
	if len(s.queue) >= outQueueDepth && !s.dropOldestEventLocked() {
		s.failStreamLocked()
		return
	}
	s.queue = append(s.queue, outFrame{line: line, event: event})
	s.cond.Signal()
}

// dropOldestEventLocked removes the oldest sheddable frame, reporting whether it
// found one. Dropping an event is safe by protocol: events carry no id, nothing
// waits on one, and a client that missed some re-syncs with a status request.
func (s *Server) dropOldestEventLocked() bool {
	for i, fr := range s.queue {
		if fr.event {
			copy(s.queue[i:], s.queue[i+1:])
			s.queue[len(s.queue)-1] = outFrame{} // don't retain the dropped line
			s.queue = s.queue[:len(s.queue)-1]
			return true
		}
	}
	return false
}

// failStreamLocked marks the stream unusable, discards the backlog and signals
// the owner. Idempotent: a failure can be reached from the writer and from an
// overflowing enqueue at the same time.
func (s *Server) failStreamLocked() {
	if s.failed {
		return
	}
	s.failed = true
	s.queue = nil
	close(s.stalled)
	s.cond.Broadcast()
}

// stopWriter stops accepting frames, lets the writer flush whatever is already
// queued, and waits for it to finish. The wait is bounded by writeDrainGrace
// because not every writer can be aborted from here (see writeDrainGrace); a
// caller that owns the connection should close it first, which makes the wait
// immediate.
func (s *Server) stopWriter() {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()

	select {
	case <-s.drained:
	case <-time.After(writeDrainGrace):
	}
}

// Serve reads and handles requests until the reader hits EOF or ctx is
// cancelled, then tears down any live connection. It returns the scanner's error
// (nil on clean EOF). Requests are handled in order and one at a time, except
// for the few that would hold the loop too long to be served that way (see
// runsInBackground); commands that do network I/O (subscription fetch, ping)
// honour ctx.
//
// Serve is the single-client transport: the stream IS the daemon's lifetime, so
// the end of the stream (the UI closing the sidecar's stdin) closes the daemon.
// ServeListener is the multi-session alternative where the daemon outlives any
// one stream.
//
// Serve also owns the daemon's background work: it starts the subscription
// auto-refresh ticker under a child context and waits for it to stop before
// returning, so the ticker's lifetime is exactly the serving session.
func (s *Server) Serve(ctx context.Context) error {
	// The sidecar's "peer" is the process that spawned it and owns its stdio: the
	// UI, running as the user, with the core as its child. A child cannot hold
	// more privilege than the parent that created it, so there is no boundary to
	// cross here and the commands that place code in the daemon's directory are
	// open — this is the case where the core is an ordinary process of the user
	// rather than a service, and importing a bundle must keep working.
	ctx = withPeerPrivilege(ctx, true)
	bgCtx, stopBg := context.WithCancel(ctx)
	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		s.daemon.runAutoRefresh(bgCtx)
	}()
	// Stop the ticker, then wait for it: deferred LIFO, so stopBg runs before
	// bg.Wait. The serve loop can return on a clean stdin EOF without ctx being
	// cancelled, so cancelling bgCtx here is what releases the ticker goroutine.
	defer bg.Wait()
	defer stopBg()

	err := s.serveStream(ctx)
	_ = s.daemon.Close()
	// After Close, so the shutdown's final state events still reach the UI, and
	// before returning, so the process doesn't exit on top of a half-written line.
	s.stopWriter()
	return err
}

// serveStream runs the request loop over the server's stream until the reader
// ends or ctx is cancelled, returning ctx.Err() on cancellation and the
// scanner's error (nil on clean EOF) otherwise. It owns only the stream: the
// daemon's connection state and background work belong to the caller, which is
// what lets ServeListener run many streams against one daemon.
func (s *Server) serveStream(ctx context.Context) error {
	scan := newRequestScanner(s.r)

	// The lane for the few commands that must not hold the loop (see
	// backgroundLane). Cancel first, then join: deferred LIFO, so a stream that
	// ended without ctx being cancelled — a sidecar's stdin reaching EOF — still
	// tells a running command to unwind instead of waiting out its budget.
	bgCtx, cancelBg := context.WithCancel(ctx)
	bg := backgroundLane{ctx: bgCtx}
	defer bg.wg.Wait()
	defer cancelBg()

	// Reading from s.r blocks, so cancellation can't interrupt a blocked Scan
	// directly. We run the read loop in a goroutine and select on ctx so
	// serveStream returns promptly on cancel; the goroutine ends when the reader
	// closes.
	lines := make(chan []byte)
	scanErr := make(chan error, 1)
	go func() {
		defer close(lines)
		for {
			line, ok := scan.next()
			if !ok {
				scanErr <- scan.err()
				return
			}
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				return <-scanErr
			}
			s.handleLine(ctx, &bg, line)
		}
	}
}

// backgroundLane is where the request loop puts a command that must not hold it
// (see runsInBackground). Its context ends with the session and its WaitGroup is
// joined before the loop returns, so nothing it starts outlives the stream that
// asked for it — the invariant the listener's shutdown leans on when it waits
// every session out.
type backgroundLane struct {
	ctx context.Context
	wg  sync.WaitGroup
}

// run hands one command to its own goroutine and returns at once.
func (b *backgroundLane) run(fn func(context.Context)) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		fn(b.ctx)
	}()
}

// runsInBackground reports whether a command is served off the request loop
// instead of in it.
//
// The loop is strictly serial otherwise, and that is worth keeping: every
// handler then runs alone, with no handler written for concurrent callers and no
// two commands interleaving on the tunnel. It costs nothing while handlers
// answer in milliseconds.
//
// check_nodes does not answer in milliseconds. It opens real connections through
// every node in a profile and is budgeted in tens of seconds (see
// defaultCheckBudget), and the loop being serial meant that whole time was time
// in which a status went unanswered, a disconnect went unactioned and the connect
// the measurement was taken *for* sat waiting behind it — so measuring a profile
// looked like an app that had stopped responding, right up until it caught up and
// did everything at once.
//
// Only a handler that touches no daemon state belongs here. handleCheckNodes
// reads the profile store and a state snapshot, both already synchronised, and
// writes nothing back; its own single-flight guard keeps two runs from
// overlapping, including across a session displacement. Anything else that grows
// a long budget wants its own audit before it joins this list, not an entry.
func runsInBackground(cmd string) bool { return cmd == CmdCheckNodes }

// handleLine parses one request line and writes its response. A malformed line
// gets an error response with id 0 (the id is unknown), so the UI still sees a
// reply rather than silence.
//
// A response for a background command arrives after responses to requests that
// were sent later. That is within the protocol as written — a response is
// correlated by its id, never by its position — and the UI's bridge keeps its
// in-flight requests in a map keyed by exactly that.
func (s *Server) handleLine(ctx context.Context, bg *backgroundLane, line []byte) {
	req, err := decodeRequest(line)
	if err != nil {
		s.writeResponse(newError(0, err.Error()))
		return
	}
	if runsInBackground(req.Cmd) {
		bg.run(func(bgCtx context.Context) {
			s.writeResponse(dispatchRequest(bgCtx, req, s.daemon.Handle))
		})
		return
	}
	s.writeResponse(dispatchRequest(ctx, req, s.daemon.Handle))
}

// dispatchRequest hands one request to handle, guarding the call with a recover
// so a panic in a command handler becomes an error response for that one request
// instead of an unrecovered panic that unwinds the serve goroutine and takes the
// daemon — and the live tunnel — down with it. The panic value and stack go to
// the core log (the process's standard logger, captured to the sidecar/service
// log file) for diagnosis; the client sees an internal-error reply correlated by
// the request id, and the next request is served normally. handle is a parameter
// so the recover can be unit-tested with a panicking stub.
func dispatchRequest(ctx context.Context, req Request, handle func(context.Context, Request) Response) (resp Response) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("control: recovered from panic handling %q (id %d): %v\n%s", req.Cmd, req.ID, r, debug.Stack())
			resp = newError(req.ID, fmt.Sprintf("internal error: %v", r))
		}
	}()
	return handle(ctx, req)
}

// writeResponse queues a response for delivery. It does not wait for the write:
// a client that stopped reading must not hold the request loop, which would in
// turn hold whatever is waiting for this session to end. A response is never
// shed under pressure — someone is waiting on its id — so a backlog that leaves
// no event to drop ends the stream instead.
func (s *Server) writeResponse(resp Response) {
	line, err := marshalLine(resp)
	if err != nil {
		// Unmarshallable response data is a programming error in the handler, not a
		// transport failure; there is nothing to send and nothing to retry.
		return
	}
	s.enqueue(line, false)
}

// emit is the daemon's event sink. It renders the event as one merged JSON line
// and queues it behind whatever is already outbound, so events and responses
// keep their order on the wire without the emitter ever waiting on the client.
func (s *Server) emit(name string, body any) {
	line, err := marshalEvent(name, body)
	if err != nil {
		return
	}
	s.enqueue(line, true)
}
