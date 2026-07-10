package control

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

// TestPingServersBoundsConcurrency feeds pingServers a large server list and,
// through an injected dial that counts how many probes run at once, asserts the
// worker pool never exceeds pingFanout in flight while still returning exactly one
// result per server in input order. This is the DoS bound: a crafted subscription
// with thousands of nodes must not fan out thousands of concurrent dials from the
// privileged daemon.
func TestPingServersBoundsConcurrency(t *testing.T) {
	const n = 500

	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	d := NewDaemon(store, newFakeRunner())

	var inFlight int64
	var maxInFlight int64
	var totalDials int64

	// The injected dial holds each probe briefly so several overlap — otherwise a
	// fast dial could serialise and hide an unbounded spawn. It records the peak
	// concurrency seen, then fails the dial so pingOne returns ok=false (the dial
	// counter, not the RTT, is what the test asserts on).
	d.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		atomic.AddInt64(&totalDials, 1)
		cur := atomic.AddInt64(&inFlight, 1)
		for {
			prev := atomic.LoadInt64(&maxInFlight)
			if cur <= prev || atomic.CompareAndSwapInt64(&maxInFlight, prev, cur) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt64(&inFlight, -1)
		return nil, fmt.Errorf("test dial: refused")
	}

	servers := make([]profile.Server, n)
	for i := range servers {
		servers[i] = profile.Server{
			ID: "node-" + strconv.Itoa(i),
			Node: model.Node{
				Protocol: model.VLESS,
				Server:   "10.0.0." + strconv.Itoa(i%256),
				Port:     443,
			},
		}
	}

	results := d.pingServers(context.Background(), servers)

	if len(results) != n {
		t.Fatalf("got %d results, want %d", len(results), n)
	}
	// One result per server, in input order.
	for i := range servers {
		if results[i].Node != servers[i].ID {
			t.Errorf("result[%d].Node = %q, want %q (order not preserved)", i, results[i].Node, servers[i].ID)
		}
	}
	if got := atomic.LoadInt64(&totalDials); got != n {
		t.Errorf("dialled %d times, want %d (one per server)", got, n)
	}
	if peak := atomic.LoadInt64(&maxInFlight); peak > pingFanout {
		t.Errorf("peak concurrency %d exceeded cap %d", peak, pingFanout)
	}
	// Sanity: with 500 slow dials and a cap of 16 the pool should actually fill,
	// otherwise the test isn't exercising the bound.
	if peak := atomic.LoadInt64(&maxInFlight); peak < 2 {
		t.Errorf("peak concurrency %d too low; test did not exercise concurrency", peak)
	}
}

// TestPingServersEmpty confirms the bounded pool degrades cleanly to a no-op on an
// empty server list: no dials, an empty (non-nil) result slice.
func TestPingServersEmpty(t *testing.T) {
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	d := NewDaemon(store, newFakeRunner())

	var dials int64
	d.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		atomic.AddInt64(&dials, 1)
		return nil, fmt.Errorf("unexpected dial")
	}

	results := d.pingServers(context.Background(), nil)
	if len(results) != 0 {
		t.Errorf("got %d results for empty list, want 0", len(results))
	}
	if atomic.LoadInt64(&dials) != 0 {
		t.Errorf("dialled %d times for empty list, want 0", atomic.LoadInt64(&dials))
	}
}
