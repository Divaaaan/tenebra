package maintenance

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func leaseSpec() LeaseSpec {
	return LeaseSpec{OperationID: "test-operation", SessionID: "test-session", ProfileID: "test-profile",
		ProfileRevision: 1, EngineRunID: "test-run", AcceptedEpoch: 1, ProtectionEpoch: 1,
		Egress: VPNOnly, Deadline: time.Now().Add(5 * time.Second)}
}

func TestLeaseRejectsLateSocketAdmission(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	raw, peer := net.Pipe()
	defer peer.Close()
	var protocolBytes atomic.Int64
	lease, err := NewLease(context.Background(), leaseSpec(), func(ctx context.Context, network string, address netip.AddrPort, register func(net.Conn) (net.Conn, error)) (net.Conn, error) {
		close(entered)
		<-release // A dial implementation may finish after cancellation.
		connection, err := register(raw)
		if err != nil {
			return nil, err
		}
		protocolBytes.Add(1)
		return connection, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Revoke(nil)
	result := make(chan error, 1)
	go func() { _, err := lease.DialContext(context.Background(), "tcp", "203.0.113.1:443"); result <- err }()
	<-entered
	lease.Revoke(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := lease.WaitDrained(ctx); !errors.Is(err, ErrDrainPending) {
		t.Fatalf("late dial was acknowledged drained: %v", err)
	}
	close(release)
	if err := <-result; !errors.Is(err, ErrRevoked) {
		t.Fatal(err)
	}
	if protocolBytes.Load() != 0 {
		t.Fatal("protocol ran after late admission")
	}
	if _, err := raw.Write([]byte{1}); err == nil {
		t.Fatal("late raw connection remained open")
	}
	if err := lease.WaitDrained(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseRevokeClosesNetworkBeforeCommitFinishes(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	lease, err := NewLease(context.Background(), leaseSpec(), func(ctx context.Context, network string, address netip.AddrPort, register func(net.Conn) (net.Conn, error)) (net.Conn, error) {
		return register(raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Revoke(nil)
	connection, err := lease.DialContext(context.Background(), "tcp", "203.0.113.1:443")
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	entered, release, result := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		result <- lease.WithCommitFence(context.Background(), func() error { close(entered); <-release; return nil })
	}()
	<-entered
	revoked := make(chan struct{})
	go func() { lease.Revoke(nil); close(revoked) }()
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("revoke waited for commit")
	}
	if _, err := raw.Write([]byte{1}); err == nil {
		t.Fatal("active raw socket not closed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := lease.WaitDrained(ctx); !errors.Is(err, ErrCommitPending) {
		t.Fatal(err)
	}
	var later atomic.Int64
	if err := lease.WithCommitFence(context.Background(), func() error { later.Add(1); return nil }); !errors.Is(err, ErrRevoked) {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("already admitted commit lost its result: %v", err)
	}
	if err := lease.WaitDrained(context.Background()); err != nil {
		t.Fatal(err)
	}
	if later.Load() != 0 {
		t.Fatal("late commit callback ran")
	}
}

func TestLeaseResolverSingleBind(t *testing.T) {
	var calls atomic.Int64
	lease, err := NewLease(context.Background(), leaseSpec(), func(context.Context, string, netip.AddrPort, func(net.Conn) (net.Conn, error)) (net.Conn, error) {
		t.Error("unexpected network")
		return nil, errors.New("unexpected")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Revoke(nil)
	if _, err := lease.LookupIP(context.Background(), "provider.test"); !errors.Is(err, ErrResolverUnavailable) {
		t.Fatal(err)
	}
	if err := lease.BindResolver(nil); err == nil {
		t.Fatal("nil resolver accepted")
	}
	resolver := func(context.Context, string) ([]net.IP, error) {
		calls.Add(1)
		return []net.IP{net.ParseIP("203.0.113.1")}, nil
	}
	if err := lease.BindResolver(resolver); err != nil {
		t.Fatal(err)
	}
	if err := lease.BindResolver(resolver); !errors.Is(err, ErrResolverAlreadyBound) {
		t.Fatal(err)
	}
	lease.Revoke(nil)
	if _, err := lease.LookupIP(context.Background(), "provider.test"); !errors.Is(err, ErrRevoked) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("revoked lookup invoked resolver")
	}
}

func TestLeaseRequiresNumericRegisteredConnection(t *testing.T) {
	var calls atomic.Int64
	lease, err := NewLease(context.Background(), leaseSpec(), func(context.Context, string, netip.AddrPort, func(net.Conn) (net.Conn, error)) (net.Conn, error) {
		calls.Add(1)
		raw, peer := net.Pipe()
		peer.Close()
		return raw, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Revoke(nil)
	for _, address := range []string{"provider.test:443", "127.0.0.1:0", "[fe80::1%en0]:443", "203.0.113.1"} {
		if _, err := lease.DialContext(context.Background(), "tcp", address); !errors.Is(err, ErrNumericAddressRequired) {
			t.Fatalf("%q: %v", address, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid target reached dialer")
	}
	if _, err := lease.DialContext(context.Background(), "tcp", "203.0.113.1:443"); !errors.Is(err, ErrRawRegistrationRequired) {
		t.Fatal(err)
	}
}

func TestLeaseParentCancellationClosesRawSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	raw, peer := net.Pipe()
	defer peer.Close()
	lease, err := NewLease(ctx, leaseSpec(), func(ctx context.Context, network string, address netip.AddrPort, register func(net.Conn) (net.Conn, error)) (net.Conn, error) {
		return register(raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Revoke(nil)
	if _, err := lease.DialContext(context.Background(), "tcp", "203.0.113.1:443"); err != nil {
		t.Fatal(err)
	}
	cancel()
	drain, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := lease.WaitDrained(drain); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(lease.Err(), ErrRevoked) {
		t.Fatal(lease.Err())
	}
	if _, err := raw.Write([]byte{1}); err == nil {
		t.Fatal("parent cancellation left raw open")
	}
}

func TestLeaseCancelledCommitWaiterCannotRun(t *testing.T) {
	lease, err := NewLease(context.Background(), leaseSpec(), func(context.Context, string, netip.AddrPort, func(net.Conn) (net.Conn, error)) (net.Conn, error) {
		return nil, errors.New("unused")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Revoke(nil)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- lease.WithCommitFence(context.Background(), func() error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lease.WithCommitFence(ctx, func() error { t.Error("cancelled callback ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLeasePendingResolverParticipatesInDrain(t *testing.T) {
	lease, err := NewLease(context.Background(), leaseSpec(), func(context.Context, string, netip.AddrPort, func(net.Conn) (net.Conn, error)) (net.Conn, error) {
		return nil, errors.New("unused")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Revoke(nil)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	if err := lease.BindResolver(func(context.Context, string) ([]net.IP, error) {
		close(entered)
		<-release
		return []net.IP{net.ParseIP("203.0.113.1")}, nil
	}); err != nil {
		t.Fatal(err)
	}
	go func() { _, err := lease.LookupIP(context.Background(), "provider.test"); done <- err }()
	<-entered
	lease.Revoke(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := lease.WaitDrained(ctx); !errors.Is(err, ErrDrainPending) {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrRevoked) {
		t.Fatal(err)
	}
	if err := lease.WaitDrained(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseDialErrorDoesNotExposePrivateDialReason(t *testing.T) {
	private := errors.New("https://synthetic-user:SECRET_CANARY@provider.test/private?token=SECRET_CANARY")
	lease, err := NewLease(context.Background(), leaseSpec(), func(context.Context, string, netip.AddrPort, func(net.Conn) (net.Conn, error)) (net.Conn, error) {
		return nil, private
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Revoke(nil)
	_, err = lease.DialContext(context.Background(), "tcp", "203.0.113.1:443")
	if err == nil || !errors.Is(err, private) {
		t.Fatal("dial cause lost")
	}
	if strings.Contains(err.Error(), "SECRET_CANARY") {
		t.Fatal("private dial reason was exposed")
	}
}

func TestLeaseCloseAndCommitCallbacksRunOutsideMetadataLock(t *testing.T) {
	var lease *Lease
	raw, peer := net.Pipe()
	defer peer.Close()
	var err error
	lease, err = NewLease(context.Background(), leaseSpec(), func(ctx context.Context, network string, address netip.AddrPort, register func(net.Conn) (net.Conn, error)) (net.Conn, error) {
		return register(&closeHookConn{Conn: raw, hook: func() { _ = lease.Err() }})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Revoke(nil)
	if _, err := lease.DialContext(context.Background(), "tcp", "203.0.113.1:443"); err != nil {
		t.Fatal(err)
	}
	if err := lease.WithCommitFence(context.Background(), func() error { return lease.Err() }); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { lease.Revoke(nil); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close callback held metadata lock")
	}
}

type closeHookConn struct {
	net.Conn
	hook func()
}

func (c *closeHookConn) Close() error { c.hook(); return c.Conn.Close() }

func TestLeaseCommitCallbacksAreSingleFlight(t *testing.T) {
	lease, err := NewLease(context.Background(), leaseSpec(), func(context.Context, string, netip.AddrPort, func(net.Conn) (net.Conn, error)) (net.Conn, error) {
		return nil, errors.New("unused")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Revoke(nil)
	var active, completed atomic.Int64
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := lease.WithCommitFence(context.Background(), func() error {
				if active.Add(1) != 1 {
					t.Error("concurrent commit callbacks")
				}
				completed.Add(1)
				active.Add(-1)
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if completed.Load() != 32 {
		t.Fatal("admitted commit disappeared")
	}
}
