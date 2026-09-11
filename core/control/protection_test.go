package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/protection"
)

type fakeHostProtection struct {
	mu                              sync.Mutex
	applyErr, resolveErr, removeErr error
	installed                       bool
	applied                         []uint64
	removed                         int
	resolvedLUID                    uint64
	resolveCalls                    int
}

func (f *fakeHostProtection) Inspect() (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installed, f.installed, nil
}
func (f *fakeHostProtection) Replace(luid uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return f.applyErr
	}
	f.installed = true
	f.applied = append(f.applied, luid)
	return nil
}
func (f *fakeHostProtection) ResolveTunnel(string, string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolveCalls++
	if f.resolvedLUID != 0 {
		return f.resolvedLUID, f.resolveErr
	}
	return 42, f.resolveErr
}
func (f *fakeHostProtection) Remove() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed++
	if f.removeErr != nil {
		return f.removeErr
	}
	f.installed = false
	return nil
}

func TestHostProtectionApplyFailureStartsNoEngine(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	d.SetProtection(protection.New(&fakeHostProtection{applyErr: errors.New("denied")}))
	d.routing.KillSwitch = true
	d.connMu.Lock()
	_, err := d.startConnect(context.Background(), p, "", false, false, "")
	d.connMu.Unlock()
	if err == nil || r.starts() != 0 || d.snapshotState().Protection.Enforced {
		t.Fatalf("err=%v starts=%d state=%+v", err, r.starts(), d.snapshotState())
	}
}

func TestHostProtectionTunFailureDoesNotAcceptNodeOrFallback(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	d.SetProtection(protection.New(&fakeHostProtection{resolveErr: errors.New("wrong TUN")}))
	d.routing.KillSwitch = true
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
	if s.State != StateError || s.Protection.Status != "error" || !s.Protection.Enforced || r.starts() != 1 {
		t.Fatalf("starts=%d state=%+v", r.starts(), s)
	}
	if _, ok := d.lastGood.Get(p.ID); ok {
		t.Fatal("local protection failure recorded last-good")
	}
}

func TestHostProtectionTeardownRetainsAndExplicitDisconnectReleases(t *testing.T) {
	d, _, p := coreAuditDaemon(t)
	f := &fakeHostProtection{}
	d.SetProtection(protection.New(f))
	d.routing.KillSwitch = true
	s := coreAuditConnect(t, d, p, "")
	if s.Protection.Status != "active" {
		t.Fatal(s.Protection)
	}
	d.connMu.Lock()
	d.teardown(StateIdle, "", "")
	d.connMu.Unlock()
	if s = d.snapshotState(); s.Protection.Status != "blocked" || !s.Protection.Enforced {
		t.Fatal(s.Protection)
	}
	if resp := d.handleDisconnect(Request{ID: 1}); !resp.Ok {
		t.Fatal(resp)
	}
	if s = d.snapshotState(); s.Protection.Status != "off" || s.Protection.Enforced {
		t.Fatal(s.Protection)
	}
}

func TestHostProtectionOffFailureRemainsVisibleAndRetryWorks(t *testing.T) {
	d, _, p := coreAuditDaemon(t)
	f := &fakeHostProtection{}
	d.SetProtection(protection.New(f))
	d.routing.KillSwitch = true
	coreAuditConnect(t, d, p, "")
	f.mu.Lock()
	f.removeErr = errors.New("locked")
	f.mu.Unlock()
	if resp := d.handleSetKillSwitch(Request{ID: 1, On: false}); resp.Ok {
		t.Fatal("failed cleanup acknowledged")
	}
	if s := d.snapshotState(); s.Protection.Status != "error" || !s.Protection.Enforced {
		t.Fatal(s.Protection)
	}
	f.mu.Lock()
	f.removeErr = nil
	f.mu.Unlock()
	if resp := d.handleSetKillSwitch(Request{ID: 2, On: false}); !resp.Ok {
		t.Fatal(resp)
	}
	if s := d.snapshotState(); s.Protection.Status != "off" || s.Protection.Enforced {
		t.Fatal(s.Protection)
	}
}

func TestHostProtectionDefaultConstructorCannotApplyNative(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	if s := d.snapshotState().Protection; s.Status != "unavailable" {
		t.Fatal(s)
	}
	d.routing.KillSwitch = true
	d.connMu.Lock()
	_, err := d.startConnect(context.Background(), p, "", false, false, "")
	d.connMu.Unlock()
	if err == nil || r.starts() != 0 {
		t.Fatal("missing injected backend accepted", err)
	}
}

func TestHostProtectionRepeatedOnKeepsVerifiedTunnel(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	f := &fakeHostProtection{}
	d.SetProtection(protection.New(f))
	d.routing.KillSwitch = true
	coreAuditConnect(t, d, p, "")
	if resp := d.handleSetKillSwitch(Request{ID: 1, On: true}); !resp.Ok {
		t.Fatal(resp)
	}
	if s := d.snapshotState(); s.Protection.Status != "active" || r.starts() != 1 {
		t.Fatalf("repeated ON revoked live TUN: %+v", s)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.applied) != 2 || f.applied[1] != 42 {
		t.Fatal(f.applied)
	}
}

func TestHostProtectionAcceptanceSerializesToggle(t *testing.T) {
	for _, offFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "repeated-on", true: "off-on"}[offFirst], func(t *testing.T) {
			d, _, p := coreAuditDaemon(t)
			f := &fakeHostProtection{}
			d.SetProtection(protection.New(f))
			d.routing.KillSwitch = true
			verified, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			d.logSink = func(_, msg string) {
				if strings.HasPrefix(msg, "connect: up on ") {
					once.Do(func() { close(verified); <-release })
				}
			}
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			d.connMu.Lock()
			_, err := d.startConnect(context.Background(), p, "", false, false, "")
			d.connMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-verified:
			case <-time.After(time.Second):
				t.Fatal("verification barrier not reached")
			}
			done := make(chan Response, 1)
			go func() {
				if offFirst {
					if resp := d.handleSetKillSwitch(Request{ID: 1, On: false}); !resp.Ok {
						done <- resp
						return
					}
				}
				done <- d.handleSetKillSwitch(Request{ID: 2, On: true})
			}()
			select {
			case <-done:
				t.Fatal("toggle replaced verified policy before acceptance")
			case <-time.After(20 * time.Millisecond):
			}
			unblock()
			select {
			case resp := <-done:
				if !resp.Ok {
					t.Fatal(resp)
				}
			case <-time.After(time.Second):
				t.Fatal("toggle stayed blocked")
			}
			deadline := time.Now().Add(time.Second)
			for d.snapshotState().Protection.Status != "active" && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if s := d.snapshotState(); s.State != StateConnected || s.Protection.Status != "active" {
				t.Fatal(s)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.applied) == 0 || f.applied[len(f.applied)-1] != 42 {
				t.Fatalf("active without verified TUN: %v", f.applied)
			}
		})
	}
}

func TestHostProtectionRetryOnAfterInitialApplyFailure(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	f := &fakeHostProtection{applyErr: errors.New("injected")}
	d.SetProtection(protection.New(f))
	d.routing.KillSwitch = true
	d.connMu.Lock()
	_, err := d.startConnect(context.Background(), p, "", false, false, "")
	d.connMu.Unlock()
	if err == nil {
		t.Fatal("initial apply should fail")
	}
	f.mu.Lock()
	f.applyErr = nil
	f.mu.Unlock()
	if resp := d.handleSetKillSwitch(Request{ID: 1, On: true}); !resp.Ok {
		t.Fatal(resp)
	}
	if s := d.snapshotState(); s.Protection.Status != "blocked" || !s.Protection.Enforced || r.starts() != 0 {
		t.Fatalf("retry did not apply idle lockdown: %+v", s)
	}
}

type refusingStopRunner struct {
	*fakeRunner
	refuse atomic.Bool
}

func (r *refusingStopRunner) Stop() error {
	if r.refuse.Load() {
		return errors.New("injected stop refusal")
	}
	return r.fakeRunner.Stop()
}

func TestHostProtectionLostVerifiedTunDemotesEvenWhenStopFails(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	wrapped := &refusingStopRunner{fakeRunner: r}
	d.runner = wrapped
	f := &fakeHostProtection{}
	d.SetProtection(protection.New(f))
	d.routing.KillSwitch = true
	d.tunWatchInterval = time.Millisecond
	d.ifacePresent = func(string) bool { return true } // an identically named replacement is present
	coreAuditConnect(t, d, p, "")
	deadline := time.Now().Add(time.Second)
	for {
		f.mu.Lock()
		checked := f.resolveCalls > 1
		f.mu.Unlock()
		if checked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher did not verify original TUN identity")
		}
		time.Sleep(time.Millisecond)
	}
	wrapped.refuse.Store(true)
	defer wrapped.refuse.Store(false)
	f.mu.Lock()
	f.resolvedLUID = 99
	f.mu.Unlock()
	deadline = time.Now().Add(time.Second)
	for d.snapshotState().State != StateError && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s := d.snapshotState(); s.State != StateError || s.Protection.Status != "blocked" || !s.Protection.Enforced || r.starts() != 1 {
		t.Fatalf("lost TUN still accepted: %+v", s)
	}
}

func TestHostProtectionCrashBudgetStillBlocks(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	f := &fakeHostProtection{}
	d.SetProtection(protection.New(f))
	d.routing.KillSwitch = true
	coreAuditConnect(t, d, p, "")
	d.mu.Lock()
	d.relaunches = maxRelaunches
	d.mu.Unlock()
	r.exit(errors.New("engine crash"))
	deadline := time.Now().Add(time.Second)
	for d.snapshotState().State != StateError && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s := d.snapshotState(); s.State != StateError || s.Protection.Status != "blocked" || !s.Protection.Persistent || r.starts() != 1 {
		t.Fatal(s)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removed != 0 {
		t.Fatal("crash cap removed persistent guard")
	}
}

func TestHostProtectionStateEventCarriesConfirmedResult(t *testing.T) {
	s := State{State: StateError, Protection: protection.State{Status: "error", Enforced: true, Persistent: true, Error: "injected"}}
	if event := stateEventBody(s); event.Protection != s.Protection {
		t.Fatal(event)
	}
}

func TestHostProtectionPlainBootstrapRefusalPreservesSettingAndNoNetwork(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	d.SetProtection(protection.New(&fakeHostProtection{}))
	d.routing.KillSwitch = true
	d.routing.DNSDirect = "udp://192.0.2.53"
	for _, lookup := range d.nodeLookups() {
		if _, err := lookup(context.Background(), "example.test"); err == nil {
			t.Fatal("bootstrap fell back to plain DNS")
		}
	}
	d.connMu.Lock()
	_, err := d.startConnect(context.Background(), p, "", false, false, "")
	d.connMu.Unlock()
	if err == nil || r.starts() != 0 || d.snapshotState().Protection.Status != "error" {
		t.Fatal("plaintext bootstrap did not fail closed", err)
	}
	if d.snapshotRouting().DNSDirect != "udp://192.0.2.53" {
		t.Fatal("saved resolver was silently overwritten")
	}
	if resp := d.handleSetDNS(Request{ID: 1, DNSDirect: "https://77.88.8.8/dns-query"}); !resp.Ok {
		t.Fatal(resp)
	}
	before := d.snapshotRouting().DNSDirect
	if resp := d.handleSetDNS(Request{ID: 2, DNSDirect: "udp://192.0.2.53"}); resp.Ok {
		t.Fatal("protected settings accepted plaintext")
	}
	if d.snapshotRouting().DNSDirect != before {
		t.Fatal("rejected setting overwrote current resolver")
	}
}
