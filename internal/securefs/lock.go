package securefs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sync"
)

type writerLock struct {
	mu     sync.Mutex
	root   *rootState
	h      handle
	closed bool
	name   string
}

func (r *Root) LockWriter(ctx context.Context) (io.Closer, error) {
	return r.lockWriterNamed(ctx, ".writer.lock")
}

// Separate kernel locks protect independent protocols in the same private
// vault. Neither a process-local mutex nor the canonical state's writer lock
// can serialize forensic-index updates across processes.
func (r *Root) lockWriterNamed(ctx context.Context, name string) (io.Closer, error) {
	if r == nil || r.state == nil {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(true); err != nil {
		return nil, err
	}
	if name != ".writer.lock" && name != ".quarantine-writer.lock" && name != ".socket-writer.lock" {
		return nil, ErrUnsupported
	}
	h, _, err := s.open([]string{name}, openRequest{write: true})
	if errors.Is(err, fs.ErrNotExist) {
		h, err = s.ops.openAt(s.h, name, openRequest{write: true, create: true, policy: s.policy})
		if err == nil {
			info, check := s.ops.inspect(h)
			err = check
			if err == nil {
				err = verifyObject(info, s.policy, s.actor, false)
				if err == nil {
					s.inventory.record(s.prefix+name, info.id)
				}
			}
		}
	}
	if err != nil {
		if h != 0 {
			s.ops.close(h)
		}
		return nil, err
	}
	if err := s.ops.lock(h); err != nil {
		s.ops.close(h)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		s.ops.unlock(h)
		s.ops.close(h)
		return nil, err
	}
	s.writers++
	if name == ".writer.lock" {
		s.canonicalWriters++
	}
	return &writerLock{root: s, h: h, name: name}, nil
}
func (l *writerLock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	err := errors.Join(l.root.ops.unlock(l.h), l.root.ops.close(l.h))
	l.root.mu.Lock()
	l.root.writers--
	if l.name == ".writer.lock" {
		l.root.canonicalWriters--
	}
	l.root.mu.Unlock()
	return err
}
