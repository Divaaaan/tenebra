package securefs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"sync"
)

type Stage struct{ state *stageState }
type stageState struct {
	mu                          sync.Mutex
	root, parent                *Root
	name                        string
	closed, published, poisoned bool
}

// Serialize acquisition, then release this mutex before I/O. Individual roots
// remain independently usable, and parent/child copy/publish cannot invert locks.
var rootPairAcquisition sync.Mutex

func (r *Root) NewStage(prefix string) (*Stage, error) {
	if r == nil || r.state == nil {
		return nil, ErrClosed
	}
	if prefix == "" || len(prefix) > 128 {
		return nil, ErrUntrusted
	}
	if parts, err := relativeParts(prefix); err != nil || len(parts) != 1 {
		return nil, ErrUntrusted
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(true); err != nil {
		return nil, err
	}
	name, err := privateName(prefix)
	if err != nil {
		return nil, err
	}
	h, err := s.ops.openAt(s.h, name, openRequest{directory: true, create: true, delete: true, policy: s.policy})
	if err != nil {
		return nil, err
	}
	info, err := s.ops.inspect(h)
	if err == nil {
		err = verifyObject(info, s.policy, s.actor, false)
	}
	if err != nil {
		s.ops.close(h)
		return nil, err
	}
	s.children++
	s.inventory.record(s.prefix+name, info.id)
	child := &Root{state: bindRoot(rootState{ops: s.ops, h: h, parent: s, policy: s.policy, actor: s.actor, inventory: &ownedInventory{items: map[string]string{}}}, info.id, false)}
	return &Stage{state: &stageState{root: child, parent: r, name: name}}, nil
}
func (s *Stage) Root() *Root {
	if s == nil || s.state == nil {
		return nil
	}
	return s.state.root
}

func (s *Stage) Publish(ctx context.Context, name string, limits Limits, verify Verify) (*Root, CommitState, error) {
	if s == nil || s.state == nil {
		return nil, Unchanged, ErrClosed
	}
	parts, err := relativeParts(name)
	if err != nil || len(parts) != 1 {
		return nil, Unchanged, ErrUntrusted
	}
	if verify == nil {
		return nil, Unchanged, ErrUntrusted
	}
	ss := s.state
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed || ss.published {
		return nil, Unchanged, ErrClosed
	}
	if ss.poisoned {
		return nil, Unchanged, ErrUntrusted
	}
	r := ss.root.state
	r.mu.Lock()
	if err := r.ready(true); err != nil {
		r.mu.Unlock()
		return nil, Unchanged, err
	}
	if r.children > 0 || r.writers > 0 {
		r.mu.Unlock()
		return nil, Unchanged, ErrBusy
	}
	r.publishing = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.publishing = false; r.mu.Unlock() }()
	lease, err := ss.root.LeaseTree(ctx, limits, verify)
	if err != nil {
		return nil, Unchanged, err
	}
	if err := lease.Close(); err != nil {
		return nil, Unchanged, err
	}
	if err := ctx.Err(); err != nil {
		return nil, Unchanged, err
	}
	parent := ss.parent.state
	rootPairAcquisition.Lock()
	parent.mu.Lock()
	r.mu.Lock()
	rootPairAcquisition.Unlock()
	defer parent.mu.Unlock()
	defer r.mu.Unlock()
	if r.children > 0 || r.leases > 0 {
		return nil, Unchanged, ErrBusy
	}
	if parent.closed {
		return nil, Unchanged, ErrClosed
	}
	// Even a verified stage never overwrites another generation.
	target, err := parent.ops.openAt(parent.h, name, openRequest{any: true})
	if err == nil {
		parent.ops.close(target)
		return nil, Unchanged, fs.ErrExist
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, Unchanged, err
	}
	before, err := r.ops.inspect(r.h)
	if err != nil {
		return nil, Unchanged, err
	}
	if err := writeSeal(r); err != nil {
		ss.poisoned = true
		return nil, Unchanged, err
	}
	if err := r.ops.rename(r.h, parent.h, name, false); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, Unchanged, err
		}
		ss.poisoned = true
		r.poisoned = true
		return nil, Indeterminate, err
	}
	check, err := r.ops.openAt(parent.h, name, openRequest{directory: true})
	if err != nil {
		ss.poisoned = true
		r.poisoned = true
		return nil, Indeterminate, err
	}
	after, err := r.ops.inspect(check)
	r.ops.close(check)
	if err != nil || after.id != before.id {
		ss.poisoned = true
		r.poisoned = true
		if err == nil {
			err = ErrChanged
		}
		return nil, Indeterminate, err
	}
	r.frozen = true
	ss.published = true
	ss.name = name
	parent.inventory.record(parent.prefix+name, before.id)
	return ss.root, Committed, nil
}

type cleanupEntry struct {
	h, parent handle
	name      string
}

func (s *Stage) Close() error {
	if s == nil || s.state == nil {
		return nil
	}
	ss := s.state
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed || ss.published {
		return nil
	}
	r := ss.root.state
	r.mu.Lock()
	if r.closed {
		ss.closed = true
		r.mu.Unlock()
		return nil
	}
	if r.children > 0 || r.leases > 0 || r.writers > 0 || r.publishing {
		r.mu.Unlock()
		return ErrBusy
	}
	owned := r.inventory.snapshot()
	var entries []cleanupEntry
	var collect func(handle, string, int) error
	collect = func(parent handle, prefix string, depth int) error {
		if depth > 32 {
			return ErrLimit
		}
		names, err := r.ops.list(parent)
		if err != nil {
			return err
		}
		for _, name := range names {
			if len(entries) > 100000 {
				return ErrLimit
			}
			h, err := r.ops.openAt(parent, name, openRequest{any: true, delete: true})
			if err != nil {
				return err
			}
			info, err := r.ops.inspect(h)
			rel := prefix + name
			if err == nil {
				err = verifyObject(info, r.policy, r.actor, false)
			}
			if err != nil || owned[rel] != info.id {
				r.ops.close(h)
				if err != nil {
					return err
				}
				return ErrChanged
			}
			if info.kind == kindDirectory {
				if err := collect(h, rel+"/", depth+1); err != nil {
					r.ops.close(h)
					return err
				}
			}
			entries = append(entries, cleanupEntry{h, parent, name})
		}
		return nil
	}
	err := collect(r.h, "", 0)
	if err != nil {
		for _, entry := range entries {
			r.ops.close(entry.h)
		}
		r.mu.Unlock()
		return err
	}
	for index, entry := range entries {
		if err := r.ops.removeOwned(entry.h, entry.parent, entry.name); err != nil {
			for _, remaining := range entries {
				if remaining.h != 0 {
					r.ops.close(remaining.h)
				}
			}
			r.poisoned = true
			r.mu.Unlock()
			return err
		}
		r.ops.close(entry.h)
		entries[index].h = 0
	}
	parent := ss.parent.state
	if err := r.ops.removeOwned(r.h, parent.h, ss.name); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	if err := ss.root.Close(); err != nil {
		return err
	}
	ss.closed = true
	return nil
}

func CopyToStage(ctx context.Context, source *Root, stage *Stage, limits Limits) (result error) {
	return copySelectedToStage(ctx, source, stage, limits, nil)
}
func copySelectedToStage(ctx context.Context, source *Root, stage *Stage, limits Limits, selected []string) (result error) {
	if source == nil || source.state == nil || stage == nil || stage.state == nil {
		return ErrClosed
	}
	if !validLimits(limits) {
		return ErrLimit
	}
	ss := stage.state
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed || ss.published {
		return ErrClosed
	}
	if ss.poisoned {
		return ErrUntrusted
	}
	src, dst := source.state, ss.root.state
	if src.objectControl == dst.objectControl {
		return ErrUntrusted
	}
	rootPairAcquisition.Lock()
	src.mu.Lock()
	dst.mu.Lock()
	rootPairAcquisition.Unlock()
	defer src.mu.Unlock()
	defer dst.mu.Unlock()
	if err := src.ready(false); err != nil {
		return err
	}
	if err := dst.ready(true); err != nil {
		return err
	}
	if src.children > 0 || dst.children > 0 {
		return ErrBusy
	}
	names, err := dst.ops.list(dst.h)
	if err != nil {
		return err
	}
	if len(names) != 0 {
		return ErrUntrusted
	}
	defer func() {
		if result != nil {
			ss.poisoned = true
			dst.poisoned = true
		}
	}()
	var count int
	var total int64
	var copyTree func(handle, handle, string, int) error
	copyTree = func(from, to handle, prefix string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 32 {
			return ErrLimit
		}
		before, err := src.ops.inspect(from)
		if err != nil {
			return err
		}
		var names []string
		if depth == 0 && selected != nil {
			names = selected
		} else {
			names, err = src.ops.list(from)
			if err != nil {
				return err
			}
		}
		for _, name := range names {
			if name == sealName {
				if src.policy.Mode == OperatorInput {
					return ErrUntrusted
				}
				sealed, err := readSeal(src.ops, from, src.policy, src.actor)
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
			count++
			if count > limits.MaxFiles {
				return ErrLimit
			}
			if _, err := relativeParts(name); err != nil || strings.Contains(name, "/") {
				return ErrUntrusted
			}
			input, err := src.ops.openAt(from, name, openRequest{any: true})
			if err != nil {
				return err
			}
			info, err := src.ops.inspect(input)
			if err == nil {
				err = verifyObject(info, src.policy, src.actor, false)
			}
			if err != nil {
				src.ops.close(input)
				return err
			}
			directory := info.kind == kindDirectory
			if depth == 0 && selected != nil && directory {
				src.ops.close(input)
				return ErrUntrusted
			}
			if !directory && (info.size < 0 || info.size > limits.MaxBytes-total) {
				src.ops.close(input)
				return ErrLimit
			}
			output, err := dst.ops.openAt(to, name, openRequest{directory: directory, create: true, write: !directory, delete: true, policy: dst.policy})
			if err != nil {
				src.ops.close(input)
				return err
			}
			outInfo, err := dst.ops.inspect(output)
			if err == nil {
				err = verifyObject(outInfo, dst.policy, dst.actor, false)
			}
			if err != nil {
				src.ops.close(input)
				dst.ops.close(output)
				return err
			}
			dst.inventory.record(prefix+name, outInfo.id)
			if directory {
				err = copyTree(input, output, prefix+name+"/", depth+1)
			} else {
				buffer := make([]byte, 32<<10)
				for {
					if err = ctx.Err(); err != nil {
						break
					}
					n, readErr := src.ops.read(input, buffer)
					if n < 0 || n > len(buffer) {
						err = ErrChanged
						break
					}
					if int64(n) > limits.MaxBytes-total {
						err = ErrLimit
						break
					}
					total += int64(n)
					for offset := 0; offset < n; {
						written, writeErr := dst.ops.write(output, buffer[offset:n])
						if writeErr != nil {
							err = writeErr
							break
						}
						if written <= 0 || written > n-offset {
							err = io.ErrShortWrite
							break
						}
						offset += written
					}
					if err != nil {
						break
					}
					if readErr != nil {
						if !errors.Is(readErr, io.EOF) {
							err = readErr
						}
						break
					}
					if n == 0 {
						err = io.ErrNoProgress
						break
					}
				}
				if err == nil {
					err = dst.ops.flush(output)
				}
				if err == nil {
					after, check := src.ops.inspect(input)
					err = check
					if err == nil && (info.id != after.id || info.change != after.change || info.size != after.size) {
						err = ErrChanged
					}
				}
			}
			src.ops.close(input)
			dst.ops.close(output)
			if err != nil {
				return err
			}
		}
		after, err := src.ops.inspect(from)
		if err != nil {
			return err
		}
		if before.id != after.id || before.change != after.change {
			return ErrChanged
		}
		return nil
	}
	return copyTree(src.h, dst.h, "", 0)
}
