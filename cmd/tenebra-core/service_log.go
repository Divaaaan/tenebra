package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"sync"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// Service logs use the same object-bound atomic writes as other private files.
// This bounded tail does not reopen an ambient pathname or stamp existing ACLs.
const serviceLogLimit = 512 << 10

type privateServiceLog struct {
	mu     sync.Mutex
	root   *securefs.Root
	writer io.Closer
	data   []byte
	closed bool
	failed error
}

func openServiceLog(filesystem daemonFilesystem) (*privateServiceLog, error) {
	if filesystem.State == nil || filesystem.Private == nil {
		return nil, securefs.ErrUntrusted
	}
	// Retain the explicit policy of the already gated private root. Production
	// callers only receive MachinePrivate; SameAccount is fixture/dev only.
	policy := securefs.Policy{Mode: securefs.MachinePrivate}
	if filesystem.privatePolicy.Mode != 0 {
		policy = filesystem.privatePolicy
	}
	root, err := filesystem.Private.OpenDir("logs", policy, true)
	if err != nil {
		return nil, err
	}
	writer, err := root.LockWriter(context.Background())
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	data, err := root.ReadFile("service.log", serviceLogLimit)
	if errors.Is(err, fs.ErrNotExist) {
		data, err = nil, nil
	}
	if err != nil {
		return nil, errors.Join(err, writer.Close(), root.Close())
	}
	log := &privateServiceLog{root: root, writer: writer, data: data}
	if _, err := log.Write([]byte("\n--- tenebra-core service start ---\n")); err != nil {
		return nil, errors.Join(err, log.Close())
	}
	return log, nil
}

func (w *privateServiceLog) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, securefs.ErrClosed
	}
	if w.failed != nil {
		return 0, w.failed
	}
	part := data
	if len(part) > serviceLogLimit {
		part = part[len(part)-serviceLogLimit:]
	}
	combined := append(bytes.Clone(w.data), part...)
	if len(combined) > serviceLogLimit {
		combined = combined[len(combined)-serviceLogLimit:]
		if newline := bytes.IndexByte(combined, '\n'); newline >= 0 {
			combined = combined[newline+1:]
		}
	}
	state, err := w.root.ReplaceFile("service.log", combined)
	if err == nil && state != securefs.Committed {
		err = securefs.ErrChanged
	}
	if err != nil {
		// An uncertain log commit is never followed by more writes.
		w.failed = err
		return 0, err
	}
	w.data = combined
	return len(data), nil
}

func (w *privateServiceLog) Tail(n int) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || n <= 0 {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(string(w.data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (w *privateServiceLog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if err := w.writer.Close(); err != nil {
		return err
	}
	if err := w.root.Close(); err != nil {
		return err
	}
	w.closed = true
	return nil
}
