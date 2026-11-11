package securefs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

type PathLease struct{ state *leaseState }
type leaseState struct {
	mu      sync.Mutex
	root    *rootState
	closed  bool
	readers int
	files   []string
	handles []handle
}

func validLimits(limits Limits) bool { return limits.MaxFiles > 0 && limits.MaxBytes > 0 }

func (r *Root) LeaseTree(ctx context.Context, limits Limits, verify Verify) (*PathLease, error) {
	if r == nil || r.state == nil {
		return nil, ErrClosed
	}
	if !validLimits(limits) {
		return nil, ErrLimit
	}
	s := r.state
	s.mu.Lock()
	if err := s.ready(false); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if s.policy.Mode == OperatorInput {
		s.mu.Unlock()
		return nil, ErrUnsupported
	}
	if s.children > 0 {
		s.mu.Unlock()
		return nil, ErrBusy
	}
	ls := &leaseState{root: s}
	var total int64
	var visit func(handle, string, int) error
	visit = func(directory handle, prefix string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 32 {
			return ErrLimit
		}
		names, err := s.ops.list(directory)
		if err != nil {
			return err
		}
		sort.Strings(names)
		for _, name := range names {
			rel := name
			if prefix != "" {
				rel = prefix + "/" + name
			}
			pathError := func(err error) error { return &fs.PathError{Op: "verify", Path: rel, Err: err} }
			if name == sealName {
				sealed, err := readSeal(s.ops, directory, s.policy, s.actor)
				if err != nil {
					return err
				}
				if !sealed {
					return ErrUntrusted
				}
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := relativeParts(name); err != nil || strings.Contains(name, "/") {
				return pathError(ErrUntrusted)
			}
			if len(ls.handles) >= limits.MaxFiles {
				return ErrLimit
			}
			h, err := s.ops.openAt(directory, name, openRequest{any: true, freeze: true})
			if err != nil {
				return pathError(err)
			}
			ls.handles = append(ls.handles, h)
			info, err := s.ops.inspect(h)
			if err != nil {
				return pathError(err)
			}
			if err := verifyObject(info, s.policy, s.actor, false); err != nil {
				return pathError(err)
			}
			if info.kind == kindDirectory {
				if err := visit(h, rel, depth+1); err != nil {
					return err
				}
				continue
			}
			if info.kind != kindRegular || info.size < 0 || info.size > limits.MaxBytes-total {
				return ErrLimit
			}
			total += info.size
			ls.files = append(ls.files, rel)
		}
		return nil
	}
	if err := visit(s.h, "", 0); err != nil {
		for i := len(ls.handles) - 1; i >= 0; i-- {
			s.ops.close(ls.handles[i])
		}
		s.mu.Unlock()
		return nil, err
	}
	s.leases++
	sort.Strings(ls.files)
	s.mu.Unlock()
	lease := &PathLease{state: ls}
	if verify != nil {
		if err := verify(ctx, lease); err != nil {
			return nil, errors.Join(err, lease.Close())
		}
	}
	return lease, nil
}

func (l *PathLease) Path() (string, error) {
	if l == nil || l.state == nil {
		return "", ErrClosed
	}
	s := l.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrClosed
	}
	return s.root.ops.path(s.root.h)
}
func (l *PathLease) Files() []string {
	if l == nil || l.state == nil {
		return nil
	}
	s := l.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return append([]string(nil), s.files...)
}
func (l *PathLease) Close() error {
	if l == nil || l.state == nil {
		return nil
	}
	s := l.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if s.readers > 0 {
		return ErrBusy
	}
	s.closed = true
	var err error
	for i := len(s.handles) - 1; i >= 0; i-- {
		err = errors.Join(err, s.root.ops.close(s.handles[i]))
	}
	s.root.mu.Lock()
	s.root.leases--
	s.root.mu.Unlock()
	return err
}

type boundedStream struct {
	mu        sync.Mutex
	lease     *leaseState
	h         handle
	parents   []handle
	before    objectInfo
	remaining int64
	closed    bool
	terminal  error
}

func (l *PathLease) OpenFile(rel string, maxBytes int64) (io.ReadCloser, error) {
	if l == nil || l.state == nil {
		return nil, ErrClosed
	}
	parts, err := relativeParts(rel)
	if err != nil {
		return nil, err
	}
	if maxBytes < 0 {
		return nil, ErrLimit
	}
	ls := l.state
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.closed {
		return nil, ErrClosed
	}
	if index := sort.SearchStrings(ls.files, rel); index >= len(ls.files) || ls.files[index] != rel {
		return nil, ErrUntrusted
	}
	r := ls.root
	r.mu.Lock()
	defer r.mu.Unlock()
	h, parents, err := r.open(parts, openRequest{freeze: true})
	if err != nil {
		return nil, err
	}
	before, err := r.ops.inspect(h)
	if err == nil && (before.size < 0 || before.size > maxBytes) {
		err = ErrLimit
	}
	if err != nil {
		r.ops.close(h)
		for _, p := range parents {
			r.ops.close(p)
		}
		return nil, err
	}
	ls.readers++
	return &boundedStream{lease: ls, h: h, parents: parents, before: before, remaining: maxBytes}, nil
}
func (s *boundedStream) Read(buffer []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	if s.terminal != nil {
		return 0, s.terminal
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if int64(len(buffer)) > s.remaining {
		if s.remaining == 0 {
			var extra [1]byte
			n, err := s.lease.root.ops.read(s.h, extra[:])
			if n > 0 {
				s.terminal = ErrLimit
				return 0, ErrLimit
			}
			if err != nil {
				return s.finish(0, err)
			}
			return 0, io.ErrNoProgress
		}
		buffer = buffer[:s.remaining]
	}
	n, err := s.lease.root.ops.read(s.h, buffer)
	if n < 0 || n > len(buffer) {
		s.terminal = ErrChanged
		return 0, ErrChanged
	}
	s.remaining -= int64(n)
	if err != nil {
		return s.finish(n, err)
	}
	if n == 0 {
		return 0, io.ErrNoProgress
	}
	return n, nil
}
func (s *boundedStream) finish(n int, err error) (int, error) {
	if errors.Is(err, io.EOF) {
		after, check := s.lease.root.ops.inspect(s.h)
		if check != nil {
			err = check
		} else if after.id != s.before.id || after.change != s.before.change || after.size != s.before.size {
			err = ErrChanged
		}
	}
	s.terminal = err
	return n, err
}
func (s *boundedStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.lease.root.ops.close(s.h)
	for _, p := range s.parents {
		err = errors.Join(err, s.lease.root.ops.close(p))
	}
	s.lease.mu.Lock()
	s.lease.readers--
	s.lease.mu.Unlock()
	return err
}
func (l *PathLease) ReadFile(rel string, maxBytes int64) ([]byte, error) {
	stream, err := l.OpenFile(rel, maxBytes)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	return data, errors.Join(readErr, closeErr)
}
