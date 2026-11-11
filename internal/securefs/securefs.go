package securefs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
)

type CommitState uint8

const (
	Unchanged CommitState = iota
	Committed
	Indeterminate
)

type Limits struct {
	MaxFiles int
	MaxBytes int64
}
type Receipt struct{ ID string }
type Verify func(context.Context, *PathLease) error

// Root values are aliases. Copying a wrapper cannot copy its lifetime or trust.
type Root struct{ state *rootState }
type rootState struct {
	*objectControl
	ops             nativeOps
	h               handle
	policy          Policy
	actor           string
	anchors         []handle
	parent          *rootState
	closed          bool
	readOnlyBinding bool
	inventory       *ownedInventory
	prefix          string
}

type objectControl struct {
	mu                           sync.Mutex
	key                          string
	frozen, publishing, poisoned bool
	children, leases, writers    int
	canonicalWriters             int
	references                   int
}

var objectControls = struct {
	sync.Mutex
	byID map[string]*objectControl
}{byID: map[string]*objectControl{}}

func bindRoot(state rootState, id string, frozen bool) *rootState {
	objectControls.Lock()
	control := objectControls.byID[id]
	if control == nil {
		control = &objectControl{key: id}
		objectControls.byID[id] = control
	}
	control.references++
	objectControls.Unlock()
	state.objectControl = control
	state.readOnlyBinding = frozen
	return &state
}
func releaseRoot(control *objectControl) {
	objectControls.Lock()
	defer objectControls.Unlock()
	control.references--
	if control.references == 0 {
		delete(objectControls.byID, control.key)
	}
}

type ownedInventory struct {
	mu    sync.Mutex
	items map[string]string
}

func (i *ownedInventory) record(name, id string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.items[name] = id
}
func (i *ownedInventory) snapshot() map[string]string {
	i.mu.Lock()
	defer i.mu.Unlock()
	result := map[string]string{}
	for key, value := range i.items {
		result[key] = value
	}
	return result
}

func OpenRoot(path string, policy Policy, create bool) (*Root, error) {
	ops, err := newNativeOps()
	if err != nil {
		return nil, err
	}
	return openRootWithOps(path, policy, create, ops)
}

func openRootWithOps(path string, policy Policy, create bool, ops nativeOps) (*Root, error) {
	if !validPolicy(policy) || !filepath.IsAbs(path) || (create && policy.Mode == OperatorInput) {
		return nil, ErrUntrusted
	}
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	remaining := strings.TrimLeft(strings.TrimPrefix(clean, volume), "/\\")
	var parts []string
	if remaining != "" {
		var err error
		parts, err = relativeParts(filepath.ToSlash(remaining))
		if err != nil {
			return nil, err
		}
	}
	actor, err := ops.principal()
	if err != nil || actor == "" {
		return nil, ErrUnsupported
	}
	h, err := ops.openVolume(volume + string(filepath.Separator))
	if err != nil {
		return nil, nativeError(err)
	}
	anchors := []handle{h}
	inheritedReadOnly := false
	success := false
	defer func() {
		if !success {
			for i := len(anchors) - 1; i >= 0; i-- {
				ops.close(anchors[i])
			}
		}
	}()
	for index, part := range parts {
		info, err := ops.inspect(h)
		if err != nil {
			return nil, err
		}
		if err := verifyObject(info, policy, actor, true); err != nil {
			return nil, err
		}
		child, err := ops.openAt(h, part, openRequest{directory: true})
		if errors.Is(err, fs.ErrNotExist) && create {
			if inheritedReadOnly {
				return nil, ErrUnsupported
			}
			child, err = ops.openAt(h, part, openRequest{directory: true, create: true, policy: policy})
		}
		if err != nil {
			return nil, nativeError(err)
		}
		anchors = append(anchors, child)
		h = child
		info, err = ops.inspect(h)
		if err != nil {
			return nil, err
		}
		if info.kind != kindDirectory {
			return nil, ErrUntrusted
		}
		if err := verifyObject(info, policy, actor, index != len(parts)-1 || policy.Mode == MachineContainer); err != nil {
			return nil, err
		}
		if policy.Mode != OperatorInput && verifyObject(info, policy, actor, false) == nil {
			sealed, check := readSeal(ops, h, policy, actor)
			if check != nil {
				return nil, check
			}
			inheritedReadOnly = inheritedReadOnly || sealed
		}
	}
	if len(parts) == 0 {
		info, err := ops.inspect(h)
		if err != nil {
			return nil, err
		}
		if err := verifyObject(info, policy, actor, policy.Mode == MachineContainer); err != nil {
			return nil, err
		}
	}
	finalInfo, err := ops.inspect(h)
	if err != nil {
		return nil, err
	}
	root := &Root{state: bindRoot(rootState{ops: ops, h: h, anchors: anchors[:len(anchors)-1], policy: policy, actor: actor, inventory: &ownedInventory{items: map[string]string{}}}, finalInfo.id, inheritedReadOnly)}
	success = true
	if policy.Mode == MachinePrivate || policy.Mode == SameAccountPrivate {
		root.state.mu.Lock()
		err = root.state.ready(false)
		root.state.mu.Unlock()
		if err != nil {
			// A rejected new wrapper owns no live work, even if another wrapper
			// currently holds this object's lease or publication fence.
			root.state.closed = true
			err = errors.Join(err, ops.close(h))
			for _, anchor := range root.state.anchors {
				err = errors.Join(err, ops.close(anchor))
			}
			releaseRoot(root.state.objectControl)
			return nil, err
		}
	}
	return root, nil
}

func (s *rootState) ready(mutation bool) error {
	if s.closed {
		return ErrClosed
	}
	if s.policy.Mode == MachineContainer {
		return ErrUnsupported
	}
	if mutation && (s.policy.Mode == OperatorInput || s.frozen || s.readOnlyBinding) {
		return ErrUnsupported
	}
	if mutation && (s.leases > 0 || s.publishing) {
		return ErrBusy
	}
	if s.poisoned {
		return ErrUntrusted
	}
	info, err := s.ops.inspect(s.h)
	if err != nil {
		return err
	}
	if info.kind != kindDirectory {
		return ErrUntrusted
	}
	if err := verifyObject(info, s.policy, s.actor, false); err != nil {
		return err
	}
	if s.policy.Mode != OperatorInput && !s.frozen {
		sealed, err := readSeal(s.ops, s.h, s.policy, s.actor)
		if err != nil {
			return err
		}
		s.frozen = sealed
		if mutation && sealed {
			return ErrUnsupported
		}
	}
	return nil
}

func (r *Root) Path() (string, error) {
	if r == nil || r.state == nil {
		return "", ErrClosed
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(false); err != nil {
		return "", err
	}
	if s.policy.Mode == OperatorInput {
		return "", ErrUnsupported
	}
	return s.ops.path(s.h)
}

func (r *Root) Close() error {
	if r == nil || r.state == nil {
		return nil
	}
	s := r.state
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if s.children > 0 || s.leases > 0 || s.writers > 0 || s.publishing {
		s.mu.Unlock()
		return ErrBusy
	}
	s.closed = true
	err := s.ops.close(s.h)
	for i := len(s.anchors) - 1; i >= 0; i-- {
		err = errors.Join(err, s.ops.close(s.anchors[i]))
	}
	s.mu.Unlock()
	if s.parent != nil {
		s.parent.mu.Lock()
		s.parent.children--
		s.parent.mu.Unlock()
	}
	releaseRoot(s.objectControl)
	return err
}

func (s *rootState) open(parts []string, req openRequest) (handle, []handle, error) {
	h := s.h
	var ancestors []handle
	for index, part := range parts {
		request := openRequest{directory: true}
		if index == len(parts)-1 {
			request = req
		}
		child, err := s.ops.openAt(h, part, request)
		if err != nil {
			for _, a := range ancestors {
				s.ops.close(a)
			}
			return 0, nil, nativeError(err)
		}
		info, err := s.ops.inspect(child)
		if err == nil {
			err = verifyObject(info, s.policy, s.actor, false)
		}
		if err == nil && ((request.directory && info.kind != kindDirectory) || (!request.directory && info.kind != kindRegular)) {
			err = ErrUntrusted
		}
		if err != nil {
			s.ops.close(child)
			for _, a := range ancestors {
				s.ops.close(a)
			}
			return 0, nil, err
		}
		if index == len(parts)-1 {
			return child, ancestors, nil
		}
		ancestors = append(ancestors, child)
		h = child
	}
	return 0, nil, ErrUntrusted
}

func (r *Root) OpenDir(rel string, policy Policy, create bool) (*Root, error) {
	if r == nil || r.state == nil {
		return nil, ErrClosed
	}
	parts, err := relativeParts(rel)
	if err != nil {
		return nil, err
	}
	if !validPolicy(policy) {
		return nil, ErrUntrusted
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	parentInfo, err := s.ops.inspect(s.h)
	if err != nil {
		return nil, err
	}
	if err := verifyObject(parentInfo, s.policy, s.actor, s.policy.Mode == MachineContainer); err != nil {
		return nil, err
	}
	if create {
		if err := s.ready(true); err != nil && s.policy.Mode != MachineContainer {
			return nil, err
		}
	}
	if s.policy.Mode == OperatorInput && (create || policy != s.policy) {
		return nil, ErrUnsupported
	}
	h := s.h
	var anchors []handle
	readOnly := s.frozen || s.readOnlyBinding || s.leases > 0 || s.publishing
	if create && !readOnly && s.policy.Mode == MachineContainer && verifyObjectInfoPrivate(s) {
		sealed, check := readSeal(s.ops, s.h, s.policy, s.actor)
		if check != nil {
			return nil, check
		}
		readOnly = sealed
	}
	cleanup := func() {
		for _, a := range anchors {
			s.ops.close(a)
		}
	}
	for index, part := range parts {
		created := false
		child, err := s.ops.openAt(h, part, openRequest{directory: true})
		if errors.Is(err, fs.ErrNotExist) && create {
			if readOnly {
				cleanup()
				return nil, ErrUnsupported
			}
			child, err = s.ops.openAt(h, part, openRequest{directory: true, create: true, policy: policy})
			created = err == nil
		}
		if err != nil {
			cleanup()
			return nil, nativeError(err)
		}
		anchors = append(anchors, child)
		h = child
		info, err := s.ops.inspect(h)
		if err == nil {
			err = verifyObject(info, policy, s.actor, index != len(parts)-1 || policy.Mode == MachineContainer)
		}
		if err != nil || info.kind != kindDirectory {
			cleanup()
			if err != nil {
				return nil, err
			}
			return nil, ErrUntrusted
		}
		if created {
			s.inventory.record(s.prefix+strings.Join(parts[:index+1], "/"), info.id)
		}
		if policy.Mode != OperatorInput && verifyObject(info, policy, s.actor, false) == nil {
			sealed, check := readSeal(s.ops, h, policy, s.actor)
			if check != nil {
				cleanup()
				return nil, check
			}
			readOnly = readOnly || sealed
		}
	}
	s.children++
	info, err := s.ops.inspect(h)
	if err != nil {
		cleanup()
		s.children--
		return nil, err
	}
	if info.id == s.objectControl.key {
		cleanup()
		s.children--
		return nil, ErrUntrusted
	}
	// The parent mutex is held. Constructor registration never locks the child
	// control here; root use performs the private-marker check before mutation.
	child := bindRoot(rootState{ops: s.ops, h: h, anchors: anchors[:len(anchors)-1], parent: s, policy: policy, actor: s.actor, inventory: s.inventory, prefix: s.prefix + strings.Join(parts, "/") + "/"}, info.id, readOnly)
	return &Root{state: child}, nil
}

func readBounded(ops nativeOps, h handle, maxBytes int64) ([]byte, error) {
	return readBoundedChecked(ops, h, maxBytes, nil)
}

func readBoundedChecked(ops nativeOps, h handle, maxBytes int64, check func(objectInfo) error) ([]byte, error) {
	if maxBytes < 0 {
		return nil, ErrLimit
	}
	before, err := ops.inspect(h)
	if err != nil {
		return nil, err
	}
	if check != nil {
		if err := check(before); err != nil {
			return nil, err
		}
	}
	if before.size < 0 || before.size > maxBytes {
		return nil, ErrLimit
	}
	var output bytes.Buffer
	chunk := make([]byte, 32<<10)
	for {
		remaining := maxBytes - int64(output.Len())
		buffer := chunk
		if remaining < int64(len(buffer)) {
			buffer = buffer[:remaining+1]
		}
		n, err := ops.read(h, buffer)
		if n < 0 || n > len(buffer) {
			return nil, ErrChanged
		}
		if int64(n) > remaining {
			return nil, ErrLimit
		}
		output.Write(buffer[:n])
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			break
		}
		if n == 0 {
			return nil, io.ErrNoProgress
		}
	}
	after, err := ops.inspect(h)
	if err != nil {
		return nil, err
	}
	if check != nil {
		if err := check(after); err != nil {
			return nil, err
		}
	}
	if before.id != after.id || before.change != after.change || before.size != after.size {
		return nil, ErrChanged
	}
	return output.Bytes(), nil
}

func (r *Root) ReadFile(rel string, maxBytes int64) ([]byte, error) {
	if r == nil || r.state == nil {
		return nil, ErrClosed
	}
	parts, err := relativeParts(rel)
	if err != nil {
		return nil, err
	}
	if hasSealComponent(parts) {
		return nil, ErrUnsupported
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(false); err != nil {
		return nil, err
	}
	h, parents, err := s.open(parts, openRequest{})
	if err != nil {
		return nil, err
	}
	defer s.ops.close(h)
	defer func() {
		for _, p := range parents {
			s.ops.close(p)
		}
	}()
	return readBounded(s.ops, h, maxBytes)
}

var errDetachedRead = errors.New("private metadata was detached while reading")

// ReadAtomicFile reads private metadata whose writers replace a single name
// atomically. If the open inode loses its last link, discard that attempt and
// verify the newly opened name. No writer lock is held while reading.
func (r *Root) ReadAtomicFile(ctx context.Context, name string, maxBytes int64) ([]byte, error) {
	if r == nil || r.state == nil {
		return nil, ErrClosed
	}
	parts, err := relativeParts(name)
	if err != nil {
		return nil, err
	}
	s := r.state
	if len(parts) != 1 || hasSealComponent(parts) || s.policy.Mode != MachinePrivate && s.policy.Mode != SameAccountPrivate {
		return nil, ErrUnsupported
	}
	for attempt := 0; attempt < 4; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, err := s.readAtomicFile(parts[0], maxBytes)
		if err == errDetachedRead {
			continue
		}
		if attempt > 0 && errors.Is(err, fs.ErrNotExist) {
			return nil, ErrChanged
		}
		return body, err
	}
	return nil, ErrChanged
}

func (s *rootState) readAtomicFile(name string, maxBytes int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(false); err != nil {
		return nil, err
	}
	h, err := s.ops.openAt(s.h, name, openRequest{})
	if err != nil {
		return nil, nativeError(err)
	}
	defer s.ops.close(h)
	return readBoundedChecked(s.ops, h, maxBytes, s.checkAtomicFile)
}

func (s *rootState) checkAtomicFile(info objectInfo) error {
	if info.kind != kindRegular {
		return ErrUntrusted
	}
	if info.links == 0 {
		// Check every other policy fact before classifying the detached inode.
		// A detached inode never supplies data to the caller.
		info.links = 1
		if err := verifyObject(info, s.policy, s.actor, false); err != nil {
			return err
		}
		return errDetachedRead
	}
	return verifyObject(info, s.policy, s.actor, false)
}

func privateName(prefix string) (string, error) {
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

func (r *Root) ReplaceFile(rel string, data []byte) (CommitState, error) {
	if r == nil || r.state == nil {
		return Unchanged, ErrClosed
	}
	parts, err := relativeParts(rel)
	if err != nil {
		return Unchanged, err
	}
	if hasSealComponent(parts) {
		return Unchanged, ErrUnsupported
	}
	if len(parts) > 1 {
		parent, err := r.OpenDir(strings.Join(parts[:len(parts)-1], "/"), r.state.policy, false)
		if err != nil {
			return Unchanged, err
		}
		defer parent.Close()
		return parent.ReplaceFile(parts[len(parts)-1], data)
	}
	s := r.state
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaceFileLocked(parts[0], data)
}

// Caller holds the object mutex; metadata protocols use the same replacement
// engine as public ReplaceFile, never a second persistence implementation.
func (s *rootState) replaceFileLocked(target string, data []byte) (CommitState, error) {
	return s.replaceStreamLocked(context.Background(), target, bytes.NewReader(data), int64(len(data)), 0600)
}
func (s *rootState) replaceStreamLocked(ctx context.Context, target string, input io.Reader, maxBytes int64, mode uint32) (CommitState, error) {
	if err := s.ready(true); err != nil {
		return Unchanged, err
	}
	old, _, err := s.open([]string{target}, openRequest{})
	if err == nil {
		s.ops.close(old)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Unchanged, err
	}
	name, err := privateName(".tmp-")
	if err != nil {
		return Unchanged, err
	}
	temp, err := s.ops.openAt(s.h, name, openRequest{create: true, write: true, policy: s.policy, permissions: mode})
	if err != nil {
		return Unchanged, err
	}
	info, err := s.ops.inspect(temp)
	if err == nil {
		err = verifyObject(info, s.policy, s.actor, false)
	}
	if err != nil {
		s.ops.close(temp)
		return Unchanged, err
	}
	state := Unchanged
	defer func() {
		if state == Unchanged {
			_ = s.ops.removeOwned(temp, s.h, name)
		}
		s.ops.close(temp)
	}()
	expected, size, err := writeStream(ctx, s.ops, temp, input, maxBytes)
	if err != nil {
		return Unchanged, err
	}
	if err := s.ops.flush(temp); err != nil {
		return Unchanged, err
	}
	check, _, err := s.open([]string{name}, openRequest{})
	if err != nil {
		return Unchanged, err
	}
	checkInfo, err := s.ops.inspect(check)
	if err != nil {
		s.ops.close(check)
		return Unchanged, err
	}
	if info.id != checkInfo.id {
		s.ops.close(check)
		return Unchanged, ErrChanged
	}
	readback, count, err := readStreamHash(ctx, s.ops, check, size)
	s.ops.close(check)
	if err != nil {
		return Unchanged, err
	}
	if count != size || readback != expected {
		return Unchanged, ErrChanged
	}
	if err := ctx.Err(); err != nil {
		return Unchanged, err
	}
	state, err = s.ops.replacePrivate(s.h, temp, name, target)
	if err != nil {
		return state, err
	}
	if state != Committed {
		return state, ErrChanged
	}
	canonical, _, err := s.open([]string{target}, openRequest{})
	if err != nil {
		return Indeterminate, err
	}
	canonicalInfo, err := s.ops.inspect(canonical)
	if err != nil {
		s.ops.close(canonical)
		return Indeterminate, err
	}
	if canonicalInfo.id != info.id {
		s.ops.close(canonical)
		return Indeterminate, ErrChanged
	}
	readback, count, err = readStreamHash(ctx, s.ops, canonical, size)
	s.ops.close(canonical)
	if err != nil {
		return Indeterminate, err
	}
	if count != size || readback != expected {
		return Indeterminate, ErrChanged
	}
	s.inventory.record(s.prefix+target, info.id)
	return Committed, nil
}
