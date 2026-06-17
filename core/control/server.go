package control

import (
	"context"
	"io"
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
func (s *Server) Serve(ctx context.Context) error {
	scan := newRequestScanner(s.r)

	// Reading from s.r blocks, so cancellation can't interrupt a blocked Scan
	// directly. We run the read loop in a goroutine and select on ctx so Serve
	// returns promptly on cancel; the goroutine ends when the reader closes.
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
			_ = s.daemon.Close()
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				_ = s.daemon.Close()
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
	resp := s.daemon.Handle(ctx, req)
	s.writeResponse(resp)
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
