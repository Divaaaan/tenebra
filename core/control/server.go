package control

import (
	"context"
	"fmt"
	"io"
	"log"
	"runtime/debug"
	"sync"
)

// Server runs the control loop: it reads request lines from an io.Reader,
// dispatches each to the Daemon, and writes responses plus asynchronous events
// to an io.Writer. All writes go through a single mutex because the daemon's
// traffic poller and process watcher emit events from their own goroutines while
// the request loop writes responses — without serialisation two JSON objects
// could interleave on one line.
type Server struct {
	daemon *Daemon

	r io.Reader

	wmu sync.Mutex
	w   io.Writer
}

// NewServer wires a daemon to a reader/writer pair. It installs the daemon's
// event emitter so events flow out through the same serialised writer as
// responses.
func NewServer(d *Daemon, r io.Reader, w io.Writer) *Server {
	s := &Server{daemon: d, r: r, w: w}
	d.SetEmitter(s.emit)
	return s
}

// Serve reads and handles requests until the reader hits EOF or ctx is
// cancelled, then tears down any live connection. It returns the scanner's error
// (nil on clean EOF). Each request is handled synchronously in order; commands
// that do network I/O (subscription fetch, ping) honour ctx.
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
	return err
}

// serveStream runs the request loop over the server's stream until the reader
// ends or ctx is cancelled, returning ctx.Err() on cancellation and the
// scanner's error (nil on clean EOF) otherwise. It owns only the stream: the
// daemon's connection state and background work belong to the caller, which is
// what lets ServeListener run many streams against one daemon.
func (s *Server) serveStream(ctx context.Context) error {
	scan := newRequestScanner(s.r)

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
			s.handleLine(ctx, line)
		}
	}
}

// handleLine parses one request line and writes its response. A malformed line
// gets an error response with id 0 (the id is unknown), so the UI still sees a
// reply rather than silence.
func (s *Server) handleLine(ctx context.Context, line []byte) {
	req, err := decodeRequest(line)
	if err != nil {
		s.writeResponse(newError(0, err.Error()))
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

// writeResponse serialises a response to the writer under the write lock.
func (s *Server) writeResponse(resp Response) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	// A write error means the UI side is gone; nothing useful to do but stop
	// trying. The read loop will hit EOF and Serve will return.
	_ = writeMessage(s.w, resp)
}

// emit is the daemon's event sink. It renders the event as one merged JSON line
// and writes it under the same lock as responses.
func (s *Server) emit(name string, body any) {
	line, err := marshalEvent(name, body)
	if err != nil {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, _ = s.w.Write(line)
}
