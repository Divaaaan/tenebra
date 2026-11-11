package maintenance

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"
)

const maxPendingDials = 16
const maxActiveSockets = 32
const maxPendingLookups = 16

// Lease has two independent gates: mu (L) protects short metadata sections;
// commit (F) serializes callbacks that may enter the Store writer (S). No I/O,
// close, callback, or drain wait runs under L. Revoke never acquires F.
type Lease struct {
	mu                           sync.Mutex
	spec                         LeaseSpec
	ctx                          context.Context
	cancel                       context.CancelFunc
	err                          error
	changed                      chan struct{}
	commit                       chan struct{}
	committing                   bool
	pendingDials, pendingLookups int
	connections                  map[*rawConnection]struct{}
	resolver                     func(context.Context, string) ([]net.IP, error)
	dial                         NumericDialer
}

type dialAttempt struct {
	ctx         context.Context
	finished    bool // protected by Lease.mu
	connections []*rawConnection
}

type rawConnection struct {
	net.Conn
	lease *Lease
	once  sync.Once
	err   error
}

func (c *rawConnection) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.lease.mu.Lock()
		delete(c.lease.connections, c)
		c.lease.signalLocked()
		c.lease.mu.Unlock()
	})
	return c.err
}

func NewLease(parent context.Context, spec LeaseSpec, dial NumericDialer) (*Lease, error) {
	if parent == nil || dial == nil || spec.OperationID == "" || spec.ProfileID == "" ||
		spec.Deadline.IsZero() || !spec.Deadline.After(time.Now()) ||
		(spec.Egress != VPNOnly && spec.Egress != DirectOnce && spec.Egress != UnprotectedDirect) ||
		(spec.Egress == VPNOnly && spec.EngineRunID == "") {
		return nil, ErrInvalidLease
	}
	if deadline, ok := parent.Deadline(); ok && deadline.Before(spec.Deadline) {
		spec.Deadline = deadline
	}
	if parent.Err() != nil {
		return nil, &causeError{ErrRevoked, parent.Err()}
	}
	ctx, cancel := context.WithDeadline(parent, spec.Deadline)
	l := &Lease{spec: spec, ctx: ctx, cancel: cancel, changed: make(chan struct{}),
		commit: make(chan struct{}, 1), connections: make(map[*rawConnection]struct{}), dial: dial}
	l.commit <- struct{}{}
	context.AfterFunc(ctx, func() { l.Revoke(context.Cause(ctx)) })
	return l, nil
}

func (l *Lease) Spec() LeaseSpec { return l.spec }

func (l *Lease) signalLocked() { close(l.changed); l.changed = make(chan struct{}) }

func (l *Lease) errLocked() error {
	if l.err != nil {
		return l.err
	}
	if err := l.ctx.Err(); err != nil {
		return &causeError{ErrRevoked, err}
	}
	return nil
}

func (l *Lease) Err() error { l.mu.Lock(); defer l.mu.Unlock(); return l.errLocked() }

func (l *Lease) Revoke(reason error) {
	l.mu.Lock()
	if l.err != nil {
		l.mu.Unlock()
		return
	}
	l.err = ErrRevoked
	if reason != nil {
		l.err = &causeError{ErrRevoked, reason}
	}
	connections := make([]*rawConnection, 0, len(l.connections))
	for connection := range l.connections {
		connections = append(connections, connection)
	}
	l.signalLocked()
	l.mu.Unlock()
	l.cancel()
	for _, connection := range connections {
		connection.Close()
	}
}

// WaitDrained acknowledges only after all raw sockets, in-flight work and an
// already admitted commit have finished. An expired wait is never cancel ACK.
func (l *Lease) WaitDrained(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidLease
	}
	for {
		l.mu.Lock()
		busy := l.committing || l.pendingDials != 0 || l.pendingLookups != 0 || len(l.connections) != 0
		changed, committing := l.changed, l.committing
		l.mu.Unlock()
		if !busy {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			// Recheck the current state rather than reporting an obsolete commit.
			l.mu.Lock()
			committing = l.committing
			busy = committing || l.pendingDials != 0 || l.pendingLookups != 0 || len(l.connections) != 0
			l.mu.Unlock()
			if !busy {
				return nil
			}
			if committing {
				return &causeError{ErrCommitPending, ctx.Err()}
			}
			return &causeError{ErrDrainPending, ctx.Err()}
		}
	}
}

func (l *Lease) WithCommitFence(ctx context.Context, callback func() error) error {
	if ctx == nil || callback == nil {
		return ErrInvalidLease
	}
	if err := l.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.ctx.Done():
		return l.Err()
	case <-l.commit:
	}
	defer func() { l.commit <- struct{}{} }()
	l.mu.Lock()
	err := l.errLocked()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		l.mu.Unlock()
		return err
	}
	l.committing = true
	l.signalLocked()
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.committing = false; l.signalLocked(); l.mu.Unlock() }()
	// The caller publishes its atomic outcome inside this callback, before F is
	// released. Revoke may close the network while this admitted commit finishes.
	return callback()
}

func (l *Lease) BindResolver(resolver func(context.Context, string) ([]net.IP, error)) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.errLocked(); err != nil {
		return err
	}
	if l.resolver != nil {
		return ErrResolverAlreadyBound
	}
	if resolver == nil {
		return ErrResolverUnavailable
	}
	l.resolver = resolver
	return nil
}

func (l *Lease) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(l.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}

func (l *Lease) LookupIP(parent context.Context, hostname string) ([]net.IP, error) {
	if parent == nil {
		return nil, ErrInvalidLease
	}
	l.mu.Lock()
	err := l.errLocked()
	if err == nil {
		err = parent.Err()
	}
	if err == nil && l.resolver == nil {
		err = ErrResolverUnavailable
	}
	if err == nil && l.pendingLookups >= maxPendingLookups {
		err = ErrResourceLimit
	}
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	resolver := l.resolver
	l.pendingLookups++
	l.signalLocked()
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.pendingLookups--; l.signalLocked(); l.mu.Unlock() }()
	ctx, cancel := l.operationContext(parent)
	defer cancel()
	ips, err := resolver(ctx, hostname)
	if revoked := l.Err(); revoked != nil {
		return nil, revoked
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, &causeError{ErrResolverUnavailable, err}
	}
	result := make([]net.IP, len(ips))
	for index, address := range ips {
		result[index] = append(net.IP(nil), address...)
	}
	return result, nil
}

func (l *Lease) closeAttempt(attempt *dialAttempt) {
	l.mu.Lock()
	connections := append([]*rawConnection(nil), attempt.connections...)
	l.mu.Unlock()
	for _, connection := range connections {
		connection.Close()
	}
}

func (l *Lease) DialContext(parent context.Context, network, address string) (net.Conn, error) {
	if parent == nil {
		return nil, ErrInvalidLease
	}
	target, err := netip.ParseAddrPort(address)
	if err != nil || target.Port() == 0 || target.Addr().Zone() != "" ||
		(network != "tcp" && network != "tcp4" && network != "tcp6") ||
		(network == "tcp4" && !target.Addr().Unmap().Is4()) || (network == "tcp6" && !target.Addr().Is6()) {
		return nil, ErrNumericAddressRequired
	}
	l.mu.Lock()
	err = l.errLocked()
	if err == nil {
		err = parent.Err()
	}
	if err == nil && l.pendingDials >= maxPendingDials {
		err = ErrResourceLimit
	}
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	l.pendingDials++ // Registered before waiting for the dial implementation.
	l.signalLocked()
	l.mu.Unlock()
	ctx, cancel := l.operationContext(parent)
	attempt := &dialAttempt{ctx: ctx}
	stopClose := context.AfterFunc(ctx, func() { l.closeAttempt(attempt) })
	register := func(raw net.Conn) (net.Conn, error) {
		if raw == nil {
			return nil, ErrRawRegistrationRequired
		}
		l.mu.Lock()
		err := l.errLocked()
		if err == nil {
			err = ctx.Err()
		}
		if err == nil && attempt.finished {
			err = ErrRevoked
		}
		if err == nil && (len(l.connections) >= maxActiveSockets || len(attempt.connections) >= maxActiveSockets) {
			err = ErrResourceLimit
		}
		if err != nil {
			l.mu.Unlock()
			raw.Close()
			return nil, err
		}
		connection := &rawConnection{Conn: raw, lease: l}
		l.connections[connection] = struct{}{}
		attempt.connections = append(attempt.connections, connection)
		l.signalLocked()
		l.mu.Unlock()
		return connection, nil
	}
	connection, err := l.dial(ctx, network, target, register)
	if err != nil {
		err = &causeError{ErrNetworkUnavailable, err}
	}
	l.mu.Lock()
	attempt.finished = true
	if revoked := l.errLocked(); revoked != nil {
		err = revoked
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && (connection == nil || len(attempt.connections) == 0) {
		err = ErrRawRegistrationRequired
	}
	l.mu.Unlock()
	if err != nil {
		stopClose()
		cancel()
		l.closeAttempt(attempt)
		if connection != nil {
			connection.Close()
		}
	}
	l.mu.Lock()
	l.pendingDials--
	l.signalLocked()
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// Keep operation cancellation attached until the returned stream closes.
	return &leasedStream{Conn: connection, cause: l.Err, close: func() { stopClose(); cancel(); l.closeAttempt(attempt) }}, nil
}

type leasedStream struct {
	net.Conn
	once  sync.Once
	close func()
	cause func() error
	err   error
}

// Raw shutdown must keep its policy identity through TLS and HTTP body reads.
// A generic closed-socket error can otherwise be mistaken for a panel failure.
func (c *leasedStream) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	if err != nil && c.cause != nil {
		if cause := c.cause(); cause != nil {
			return n, cause
		}
	}
	return n, err
}
func (c *leasedStream) Write(buffer []byte) (int, error) {
	n, err := c.Conn.Write(buffer)
	if err != nil && c.cause != nil {
		if cause := c.cause(); cause != nil {
			return n, cause
		}
	}
	return n, err
}

func (c *leasedStream) Close() error {
	c.once.Do(func() {
		// Raw shutdown precedes any wrapper close (TLS close_notify may wait).
		c.close()
		c.err = c.Conn.Close()
	})
	return c.err
}
