package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/protection"
	"github.com/Divaaaan/tenebra/core/state"
)

// Done is evaluated when Transact waits for the writer. This observable context
// barrier lets the test pause a real Store without sleeps or filesystem hooks.
type canonicalWriterWaitContext struct {
	context.Context
	once    sync.Once
	reached chan struct{}
}

func (c *canonicalWriterWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.reached) })
	return c.Context.Done()
}

func TestCanonicalPreferenceWriterDoesNotBlockDisconnect(t *testing.T) {
	for _, request := range []Request{
		{Cmd: CmdSetDNS, DNSRemote: "https://remote.test/dns-query", DNSDirect: "https://direct.test/dns-query"},
		{Cmd: CmdSetKillSwitch, On: true},
	} {
		t.Run(string(request.Cmd), func(t *testing.T) {
			d, store, _ := newCanonicalTestDaemon(t)
			entered, release, writerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				_, err := store.Transact(context.Background(), state.AnyRevision, func(*state.Snapshot) error {
					close(entered)
					<-release
					return nil
				})
				writerDone <- err
			}()
			<-entered
			waiting := &canonicalWriterWaitContext{Context: context.Background(), reached: make(chan struct{})}
			preference := make(chan Response, 1)
			go func() { preference <- d.Handle(waiting, request) }()
			<-waiting.reached
			disconnected := make(chan Response, 1)
			go func() { disconnected <- d.Handle(context.Background(), Request{Cmd: CmdDisconnect}) }()
			responded := false
			select {
			case result := <-disconnected:
				responded = result.Ok
			case <-time.After(2 * time.Second):
			}
			// Always drain both paths before reporting a failure.
			close(release)
			if err := <-writerDone; err != nil {
				t.Fatal(err)
			}
			if result := <-preference; !result.Ok {
				t.Fatal(result.Error)
			}
			if !responded {
				if result := <-disconnected; !result.Ok {
					t.Fatal(result.Error)
				}
				t.Fatal("disconnect waited for a preference filesystem writer")
			}
		})
	}
}

func TestCanonicalLastGoodWriterDoesNotBlockAcceptanceOrDisconnect(t *testing.T) {
	d, _, p, releaseProbe := canonicalRuntimeProbeGate(t)
	entered, release, writerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() {
		_, err := d.canonical.Transact(context.Background(), state.AnyRevision, func(*state.Snapshot) error {
			close(entered)
			<-release
			return nil
		})
		writerDone <- err
	}()
	<-entered
	releaseProbe()
	deadline := time.Now().Add(2 * time.Second)
	accepted := false
	for time.Now().Before(deadline) {
		if d.snapshotState().State == StateConnected {
			accepted = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	disconnected := make(chan Response, 1)
	go func() { disconnected <- d.Handle(context.Background(), Request{Cmd: CmdDisconnect}) }()
	responded := false
	select {
	case r := <-disconnected:
		responded = r.Ok
	case <-time.After(2 * time.Second):
	}
	unblock()
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if !responded {
		if r := <-disconnected; !r.Ok {
			t.Fatal(r.Error)
		}
	}
	if !accepted {
		t.Fatal("optional last-good persistence held a successful VPN in connecting")
	}
	if !responded {
		t.Fatal("disconnect waited for an optional last-good filesystem writer")
	}
	waitFor(t, func() bool {
		ref, ok := d.canonical.View().LastGood[p.ID]
		return ok && ref.NodeID == p.Servers[0].ID
	}, "queued accepted last-good hint")
}

func TestCanonicalProtectionConvergesToNewerSavedPreference(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	host := &fakeHostProtection{}
	d.SetProtection(protection.New(host))
	d.mu.Lock()
	d.state.State = StateConnected
	d.mu.Unlock()
	d.protectionOp.Lock()
	var once sync.Once
	unlock := func() { once.Do(d.protectionOp.Unlock) }
	t.Cleanup(unlock)
	results := make(chan Response, 2)
	go func() { results <- d.Handle(context.Background(), Request{Cmd: CmdSetKillSwitch, On: true}) }()
	waitFor(t, func() bool { return store.View().Preferences.KillSwitch && d.snapshotRouting().KillSwitch }, "ON saved while native lane is occupied")
	go func() { results <- d.Handle(context.Background(), Request{Cmd: CmdSetKillSwitch, On: false}) }()
	waitFor(t, func() bool { return !store.View().Preferences.KillSwitch && !d.snapshotRouting().KillSwitch }, "newer OFF saved before native actions")
	unlock()
	for i := 0; i < 2; i++ {
		if r := <-results; !r.Ok {
			t.Fatal(r.Error)
		}
	}
	host.mu.Lock()
	applied, installed := len(host.applied), host.installed
	host.mu.Unlock()
	if applied != 0 || installed || store.View().Preferences.KillSwitch {
		t.Fatal("an older ON request overrode the newer committed OFF preference")
	}
}

func TestCanonicalLastGoodQueueCoalescesWithoutPublishingUnsavedHints(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	p := addCanonicalProfile(t, store)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() {
		_, err := store.Transact(context.Background(), state.AnyRevision, func(*state.Snapshot) error { close(entered); <-release; return nil })
		done <- err
	}()
	<-entered
	before := store.View().Revision
	for i := 0; i < 10; i++ {
		d.lastGood.Set(p.ID, p.Servers[0].ID)
	}
	if _, ok := d.lastGood.Get(p.ID); ok {
		unblock()
		<-done
		t.Fatal("unsaved optional hint escaped the canonical store")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		ref, ok := store.View().LastGood[p.ID]
		return ok && ref.NodeID == p.Servers[0].ID
	}, "coalesced last-good hint")
	if store.View().Revision != before+1 {
		t.Fatal("duplicate hints made redundant repository commits")
	}
}

func TestCanonicalRepeatedKillOnPreservesAcceptedProtection(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	p := addCanonicalProfile(t, store)
	host := &fakeHostProtection{}
	d.SetProtection(protection.New(host))
	d.probeWarmup = time.Millisecond
	d.healthInterval = time.Hour
	d.tunWatchInterval = time.Hour
	d.ifacePresent = func(string) bool { return true }
	t.Cleanup(func() { d.handleDisconnect(Request{Cmd: CmdDisconnect}); d.relaunchWG.Wait() })
	if r := d.Handle(context.Background(), Request{Cmd: CmdSetKillSwitch, On: true}); !r.Ok {
		t.Fatal(r.Error)
	}
	r := d.Handle(context.Background(), Request{Cmd: CmdConnectV2, Profile: p.ID,
		Selection: &SelectionView{Kind: "pinned", Ref: &NodeRefView{ProfileID: p.ID, NodeID: p.Servers[0].ID, Resolution: "resolved"}}})
	if !r.Ok {
		t.Fatal(r.Error)
	}
	waitFor(t, func() bool {
		return d.snapshotState().State == StateConnected && d.protection.Snapshot().Status == "active"
	}, "protected fake-engine acceptance")
	_, before := d.protection.SnapshotWithEpoch()
	if r := d.Handle(context.Background(), Request{Cmd: CmdSetKillSwitch, On: true}); !r.Ok {
		t.Fatal(r.Error)
	}
	current, after := d.protection.SnapshotWithEpoch()
	if current.Status != "active" || before != after {
		t.Fatal("repeated ON replaced accepted tunnel protection with lockdown", current)
	}
}
