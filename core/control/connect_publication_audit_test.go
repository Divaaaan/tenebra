package control

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectInitialPublicationPrecedesFallbackCompletion(t *testing.T) {
	d, _, p := coreAuditDaemon(t)
	initial, release, connected := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce, connectedOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var connecting atomic.Int32
	var mu sync.Mutex
	var published []ConnState
	d.SetEmitter(func(name string, body any) {
		state, ok := body.(stateEvent)
		if name != EventState || !ok {
			return
		}
		// The first Connecting comes from teardown. Park the connect's own
		// initial publication before delivery, as a preemption before enqueue
		// can do; no production state or scheduler hooks are changed.
		if state.State == StateConnecting && connecting.Add(1) == 2 {
			close(initial)
			<-release
		}
		mu.Lock()
		published = append(published, state.State)
		mu.Unlock()
		if state.State == StateConnected {
			connectedOnce.Do(func() { close(connected) })
		}
	})
	done := make(chan error, 1)
	go func() {
		d.connMu.Lock()
		_, err := d.startConnect(context.Background(), p, "", false, false, "")
		d.connMu.Unlock()
		done <- err
	}()
	select {
	case <-initial:
	case <-time.After(time.Second):
		t.Fatal("initial publication barrier not reached")
	}
	select {
	case <-connected:
	case <-time.After(50 * time.Millisecond):
		// Correct ordering may not start fallback until this publication returns.
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("connect did not return")
	}
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("fallback never connected")
	}
	mu.Lock()
	states := append([]ConnState(nil), published...)
	mu.Unlock()
	finished := false
	for _, state := range states {
		if state == StateConnected {
			finished = true
		} else if finished && state == StateConnecting {
			t.Fatalf("initial state overtook fallback result: events=%v final=%s", states, d.snapshotState().State)
		}
	}
}
