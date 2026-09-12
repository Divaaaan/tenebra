package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/fallback"
	"github.com/Divaaaan/tenebra/core/protection"
	"github.com/Divaaaan/tenebra/core/singbox"
)

func TestHostProtectionDisconnectCannotPublishCancelledAcceptance(t *testing.T) {
	d, _, p := coreAuditDaemon(t)
	d.SetProtection(protection.New(&fakeHostProtection{}))
	d.routing.KillSwitch = true
	d.tun.Mode = singbox.ModeSystemProxy
	accepting, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	d.logSink = func(_, msg string) {
		if strings.HasPrefix(msg, "connect: up on ") {
			close(accepting)
			<-release // all gates passed, but Active/Connected has not been published
		}
	}
	var cancelled, publishedAfterCancel atomic.Bool
	d.SetEmitter(func(name string, body any) {
		if state, ok := body.(stateEvent); name == EventState && ok && cancelled.Load() {
			// Interrupted can report blocked with the previous connection phase
			// while teardown drains. Only an active publication claims acceptance.
			if state.Protection.Status == "active" {
				publishedAfterCancel.Store(true)
			}
		}
	})
	d.connMu.Lock()
	_, err := d.startConnect(context.Background(), p, "", false, false, "")
	d.connMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-accepting:
	case <-time.After(time.Second):
		t.Fatal("acceptance barrier not reached")
	}
	// Observe real teardown cancellation. In the broken ordering it happens
	// while the acceptance goroutine is still parked in the log callback.
	cancelledEvent := make(chan struct{})
	d.mu.Lock()
	cancel := d.cancel
	d.cancel = func() {
		cancel()
		cancelled.Store(true)
		close(cancelledEvent)
	}
	d.mu.Unlock()
	done := make(chan Response, 1)
	go func() { done <- d.handleDisconnect(Request{ID: 1}) }()
	select {
	case <-cancelledEvent:
	case <-time.After(50 * time.Millisecond):
		// A fenced teardown waits until acceptance has finished publishing.
	}
	unblock()
	select {
	case resp := <-done:
		if !resp.Ok {
			t.Fatal(resp)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not drain acceptance")
	}
	if publishedAfterCancel.Load() {
		t.Fatal("cancelled connection published Active/Connected during disconnect")
	}
	if s := d.snapshotState(); s.State != StateIdle || s.Protection.Status != "off" {
		t.Fatalf("disconnect did not finish cleanup: %+v", s)
	}
}

func TestHostProtectionSupersededAcceptanceSkipsLocalGates(t *testing.T) {
	for _, cause := range []string{"cancelled", "generation"} {
		t.Run(cause, func(t *testing.T) {
			d, _, _ := coreAuditDaemon(t)
			native := &fakeHostProtection{}
			d.SetProtection(protection.New(native))
			d.routing.KillSwitch = true
			d.tun.Mode = singbox.ModeSystemProxy
			proxy := &fakeProxyController{}
			d.proxy = proxy
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			loop := fallbackLoop{gen: d.generation, ro: d.routing, tun: d.tun}
			if cause == "cancelled" {
				cancel()
			} else {
				loop.gen++
			}
			err := d.recordSuccess(ctx, loop, fallback.Attempt{}, nil, fallback.Strategy{}, selectorShape{})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("superseded acceptance returned %v", err)
			}
			if s := d.snapshotState(); s.Protection.Status != "off" || proxy.enables() != 0 {
				t.Fatalf("superseded acceptance applied local gates: proxy=%d state=%+v", proxy.enables(), s)
			}
			native.mu.Lock()
			defer native.mu.Unlock()
			if len(native.applied) != 0 {
				t.Fatalf("superseded acceptance replaced native policy: %v", native.applied)
			}
		})
	}
}

func TestHostProtectionProxyFailureCannotPublishActive(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	d.SetProtection(protection.New(&fakeHostProtection{}))
	d.routing.KillSwitch = true
	d.tun.Mode = singbox.ModeSystemProxy
	d.proxy = &fakeProxyController{enableErr: errors.New("interactive proxy denied")}
	var accepted atomic.Bool
	d.SetEmitter(func(name string, body any) {
		if state, ok := body.(stateEvent); name == EventState && ok {
			if state.State == StateConnected || state.Protection.Status == "active" {
				accepted.Store(true)
			}
		}
	})
	d.connMu.Lock()
	_, err := d.startConnect(context.Background(), p, "", false, false, "")
	d.connMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for d.snapshotState().State != StateError && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s := d.snapshotState()
	if accepted.Load() || s.State != StateError || s.Protection.Status != "blocked" || !s.Protection.Enforced || r.starts() != 1 || !strings.Contains(s.Error, "interactive proxy denied") {
		t.Fatalf("accepted=%v starts=%d state=%+v", accepted.Load(), r.starts(), s)
	}
	if _, ok := d.lastGood.Get(p.ID); ok {
		t.Fatal("failed local proxy gate recorded last-good")
	}
}

func TestHostProtectionDisconnectReportsBothCleanupFailuresAndRetries(t *testing.T) {
	d, _, p := coreAuditDaemon(t)
	native := &fakeHostProtection{}
	d.SetProtection(protection.New(native))
	d.routing.KillSwitch = true
	d.tun.Mode = singbox.ModeSystemProxy
	proxy := &fakeProxyController{}
	d.proxy = proxy
	coreAuditConnect(t, d, p, "")
	proxy.mu.Lock()
	proxy.disableErr = errors.New("proxy rollback denied")
	proxy.mu.Unlock()
	native.mu.Lock()
	native.removeErr = errors.New("WFP removal denied")
	native.mu.Unlock()
	resp := d.handleDisconnect(Request{ID: 1})
	if resp.Ok || !strings.Contains(resp.Error, "proxy rollback denied") || !strings.Contains(resp.Error, "WFP removal denied") {
		t.Fatalf("cleanup failures lost: %+v", resp)
	}
	if s := d.snapshotState(); !s.Protection.Enforced || s.Protection.Status != "error" {
		t.Fatal(s)
	}
	proxy.mu.Lock()
	proxy.disableErr = nil
	proxy.mu.Unlock()
	native.mu.Lock()
	native.removeErr = nil
	native.mu.Unlock()
	if resp = d.handleDisconnect(Request{ID: 2}); !resp.Ok {
		t.Fatal(resp)
	}
	if s := d.snapshotState(); s.Protection.Enforced || s.Protection.Status != "off" || s.State != StateIdle {
		t.Fatal(s)
	}
}
