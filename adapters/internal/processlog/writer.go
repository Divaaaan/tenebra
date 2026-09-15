// Package processlog adapts process stdout and stderr to line-oriented log
// callbacks without taking ownership of the process pipes. os/exec can then
// bound pipe-copy completion with Cmd.WaitDelay while callers still receive an
// unterminated final line before process completion is published.
package processlog

import (
	"bytes"
	"sync"
)

const maxPendingBytes = 1 << 20

// Writer buffers a partial line across Write calls and emits complete lines.
// It is safe for concurrent Write and Flush calls, although each process stream
// normally has its own Writer.
type Writer struct {
	mu          sync.Mutex
	pending     []byte
	lineHasData bool
	emit        func(string)
}

// New creates a line writer that calls emit once for each completed line.
func New(emit func(string)) *Writer {
	return &Writer{emit: emit}
}

// Write implements io.Writer. A pathological line is emitted in bounded chunks
// so a child process cannot grow the pending buffer without limit.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	written := len(p)
	for len(p) > 0 {
		newline := bytes.IndexByte(p, '\n')
		if newline >= 0 {
			w.appendBounded(p[:newline])
			if len(w.pending) > 0 || !w.lineHasData {
				w.emitPending()
			}
			w.lineHasData = false
			p = p[newline+1:]
			continue
		}
		w.appendBounded(p)
		break
	}
	return written, nil
}

func (w *Writer) appendBounded(p []byte) {
	if len(p) > 0 {
		w.lineHasData = true
	}
	for len(p) > 0 {
		room := maxPendingBytes - len(w.pending)
		if room > len(p) {
			room = len(p)
		}
		w.pending = append(w.pending, p[:room]...)
		p = p[room:]
		if len(w.pending) == maxPendingBytes {
			w.emitPending()
		}
	}
}

// Flush emits an unterminated final line. Call it only after Cmd.Wait returns,
// when os/exec's writer-copy goroutines have completed or WaitDelay closed their
// pipes.
func (w *Writer) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 {
		w.emitPending()
	}
	w.lineHasData = false
}

func (w *Writer) emitPending() {
	w.emitBytes(w.pending)
	w.pending = w.pending[:0]
}

func (w *Writer) emitBytes(line []byte) {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	if w.emit != nil {
		w.emit(string(line))
	}
}
