package securefs

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
)

// This filesystem receipt is NOT readiness or permission to signal a PID.
// A durable owner record and an ended kernel lifetime are separate checks.
type SocketIdentity struct {
	ID  string `json:"id"`
	Pin string `json:"pin"`
}

func (v SocketIdentity) Validate() error {
	if !v.valid() {
		return ErrUntrusted
	}
	return nil
}

func (v SocketIdentity) valid() bool {
	if !socketObjectID(v.ID) || len(v.Pin) != 36 || !strings.HasPrefix(v.Pin, "pin_") {
		return false
	}
	for _, c := range v.Pin[4:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func socketObjectID(value string) bool {
	pair := strings.Split(value, ":")
	if len(pair) != 2 {
		return false
	}
	for i, part := range pair {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != part || i == 1 && n == 0 {
			return false
		}
	}
	return true
}

type privateSocketFacts struct {
	identity SocketIdentity
	owner    string
	mode     uint32
	links    uint32
	known    bool
	grants   []grant
}
type privateSocketOps interface {
	bindPrivateSocket(handle, string) (net.Listener, privateSocketFacts, error)
	privateSocketFacts(handle, string) (privateSocketFacts, error)
	publishPrivateSocket(handle, string, string, SocketIdentity) (CommitState, error)
	removePrivateSocket(handle, []string, SocketIdentity) error
}

func socketName(name string) bool {
	parts, err := relativeParts(name)
	return err == nil && len(parts) == 1 && len(name) <= 64 && !strings.HasPrefix(name, ".")
}
func socketRoot(s *rootState) (privateSocketOps, error) {
	if s.policy.Mode != MachinePrivate && s.policy.Mode != SameAccountPrivate {
		return nil, ErrUnsupported
	}
	ops, ok := s.ops.(privateSocketOps)
	if !ok {
		return nil, ErrUnsupported
	}
	return ops, nil
}
func verifySocketFacts(f privateSocketFacts, s *rootState) error {
	if !socketObjectID(f.identity.ID) || !f.known || f.mode != 0600 || f.links < 1 || f.links > 3 || !trustedPrincipal(f.owner, s.policy, s.actor) {
		return ErrUntrusted
	}
	return verifyGrants(objectInfo{owner: f.owner, securityKnown: true, grants: f.grants}, s.policy, s.actor, false)
}

// BindPrivateSocket creates only a new private temporary name. Publish happens
// separately, after the caller durably recorded this socket's exact owner.
func (r *Root) BindPrivateSocket(ctx context.Context, name string) (*SocketLease, error) {
	if r == nil || r.state == nil {
		return nil, ErrClosed
	}
	if !socketName(name) {
		return nil, ErrUntrusted
	}
	s := r.state
	ops, err := socketRoot(s)
	if err != nil {
		return nil, err
	}
	lock, err := r.lockWriterNamed(ctx, ".socket-writer.lock")
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(true); err != nil {
		return nil, err
	}
	names, err := s.ops.list(s.h)
	if err != nil {
		return nil, err
	}
	if len(names) >= 64 {
		return nil, ErrLimit
	}
	listener, facts, err := ops.bindPrivateSocket(s.h, name)
	if err != nil {
		if listener != nil {
			listener.Close()
		}
		return nil, err
	}
	if err = verifySocketFacts(facts, s); err == nil {
		if !facts.identity.valid() || facts.links != 2 {
			err = ErrUntrusted
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		closeErr := listener.Close()
		return nil, errors.Join(err, closeErr, ops.removePrivateSocket(s.h, []string{name}, facts.identity))
	}
	s.children++ // existing resource-retention rules also block Close/Detach/tree leases
	return &SocketLease{root: r, ops: ops, listener: listener, name: name, identity: facts.identity}, nil
}

type SocketLease struct {
	mu       sync.Mutex
	root     *Root
	ops      privateSocketOps
	listener net.Listener
	name     string
	identity SocketIdentity
	closed   bool
	closeErr error
}

func (s *SocketLease) Accept() (net.Conn, error) {
	if s == nil || s.listener == nil {
		return nil, net.ErrClosed
	}
	return s.listener.Accept()
}
func (s *SocketLease) Addr() net.Addr {
	if s == nil || s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}
func (s *SocketLease) Identity() SocketIdentity {
	if s == nil {
		return SocketIdentity{}
	}
	return s.identity
}
func (s *SocketLease) Publish(ctx context.Context, target string) (CommitState, error) {
	if s == nil || s.root == nil || s.root.state == nil {
		return Unchanged, ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Unchanged, ErrClosed
	}
	if !socketName(target) || target == s.name {
		return Unchanged, ErrUntrusted
	}
	lock, err := s.root.lockWriterNamed(ctx, ".socket-writer.lock")
	if err != nil {
		return Unchanged, err
	}
	defer lock.Close()
	root := s.root.state
	root.mu.Lock()
	defer root.mu.Unlock()
	if err := root.ready(true); err != nil {
		return Unchanged, err
	}
	if err := ctx.Err(); err != nil {
		return Unchanged, err
	}
	outcome, err := s.ops.publishPrivateSocket(root.h, s.name, target, s.identity)
	if outcome != Unchanged {
		s.name = target
	}
	return outcome, err
}
func (s *SocketLease) Close() error {
	if s == nil || s.root == nil || s.root.state == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	closeErr := s.listener.Close()
	if errors.Is(closeErr, net.ErrClosed) {
		closeErr = nil
	}
	lock, err := s.root.lockWriterNamed(context.Background(), ".socket-writer.lock")
	if err == nil {
		root := s.root.state
		root.mu.Lock()
		err = root.ready(true)
		if err == nil {
			err = s.ops.removePrivateSocket(root.h, []string{s.name}, s.identity)
		}
		root.mu.Unlock()
		err = errors.Join(err, lock.Close())
	}
	root := s.root.state
	root.mu.Lock()
	root.children--
	root.mu.Unlock()
	s.closeErr = errors.Join(closeErr, err)
	return s.closeErr
}

// verifyEnded must establish definite old process/job termination using the
// trusted owner record, not merely a PID or absent status projection. It runs
// outside every C2/lifecycle lock. Unknown node identity is always preserved.
func (r *Root) RemoveVerifiedSocket(ctx context.Context, name string, expected SocketIdentity, verifyEnded func(context.Context) error) error {
	return r.removeVerifiedSocket(ctx, []string{name}, expected, verifyEnded)
}
func (r *Root) RemoveVerifiedSocketPair(ctx context.Context, final, temporary string, expected SocketIdentity, verifyEnded func(context.Context) error) error {
	return r.removeVerifiedSocket(ctx, []string{final, temporary}, expected, verifyEnded)
}
func (r *Root) removeVerifiedSocket(ctx context.Context, names []string, expected SocketIdentity, verifyEnded func(context.Context) error) error {
	if r == nil || r.state == nil {
		return ErrClosed
	}
	if len(names) < 1 || len(names) > 2 || !expected.valid() || verifyEnded == nil {
		return ErrUntrusted
	}
	for i, name := range names {
		if !socketName(name) || name == expected.Pin || i == 1 && name == names[0] {
			return ErrUntrusted
		}
	}
	ops, err := socketRoot(r.state)
	if err != nil {
		return err
	}
	if err := verifyEnded(ctx); err != nil {
		return err
	}
	lock, err := r.lockWriterNamed(ctx, ".socket-writer.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	root := r.state
	root.mu.Lock()
	defer root.mu.Unlock()
	if err := root.ready(true); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return ops.removePrivateSocket(root.h, names, expected)
}
