package control

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

func TestRemovedQueuedAutoconnectNeverStarts(t *testing.T) {
	d, runner, p := coreAuditDaemon(t)
	d.mu.Lock()
	d.autoconnect = true
	d.lastProfile = p.ID
	d.lastNode = p.Servers[0].ID
	d.mu.Unlock()
	queued, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	d.beforeReconnect = func() { close(queued); <-release }
	if !d.AutoconnectOnStart() {
		t.Fatal("valid intent was not queued")
	}
	<-queued
	if result := d.handleRemoveProfile(Request{Cmd: CmdRemoveProfile, Profile: p.ID}); !result.Ok {
		t.Fatal(result.Error)
	}
	unblock()
	d.relaunchWG.Wait()
	waitFor(t, func() bool { return d.snapshotState().State != StateConnecting }, "queued start decision")
	if runner.starts() != 0 || d.snapshotState().State != StateIdle {
		t.Fatal("removed queued profile started", runner.starts(), d.snapshotState().State)
	}
}

func TestRefreshLabelOnlySignalsOnce(t *testing.T) {
	d, _, p := coreAuditDaemon(t)
	p.Source = profile.SourceSubscription
	p.URL = "https://provider.test/private"
	if err := d.store.Update(p); err != nil {
		t.Fatal(err)
	}
	d.fetch = func(context.Context, string) ([]byte, http.Header, error) {
		return []byte("vless://123e4567-e89b-12d3-a456-426614174000@192.0.2.1:443#Renamed\nvless://123e4567-e89b-12d3-a456-426614174001@192.0.2.2:443#Exit"), http.Header{}, nil
	}
	var events atomic.Int64
	d.SetEmitter(func(event string, _ any) {
		if event == EventProfiles {
			events.Add(1)
		}
	})
	d.refreshSubscriptionsTick(context.Background())
	if events.Load() != 1 {
		t.Fatal("label-only update did not emit exactly one batch event", events.Load())
	}
	d.refreshSubscriptionsTick(context.Background())
	if events.Load() != 1 {
		t.Fatal("unchanged batch emitted a redundant profiles event", events.Load())
	}
	d.Handle(context.Background(), Request{Cmd: CmdRefreshSub, Profile: p.ID})
	if events.Load() != 2 {
		t.Fatal("unchanged manual refresh failed to notify", events.Load())
	}
}

func TestQueuedAutoconnectReadsLatestProfilePayload(t *testing.T) {
	d, runner, p := coreAuditDaemon(t)
	d.mu.Lock()
	d.autoconnect = true
	d.lastProfile = p.ID
	d.lastNode = p.Servers[0].ID
	d.mu.Unlock()
	queued, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	d.beforeReconnect = func() { close(queued); <-release }
	if !d.AutoconnectOnStart() {
		t.Fatal("autoconnect not queued")
	}
	<-queued
	latest := p
	latest.Servers = append([]profile.Server(nil), p.Servers...)
	latest.Servers[0].TLS = &model.TLS{Enabled: true, ServerName: "queued.latest.test"}
	if err := d.store.Update(latest); err != nil {
		t.Fatal(err)
	}
	unblock()
	d.relaunchWG.Wait()
	waitFor(t, func() bool { return d.snapshotState().State == StateConnected }, "queued current profile acceptance")
	runner.mu.Lock()
	body := append([]byte(nil), runner.lastCfg...)
	runner.mu.Unlock()
	if !strings.Contains(string(body), "queued.latest.test") {
		t.Fatal("queued connect used captured old profile payload")
	}
}

func TestQueuedAutoconnectSurvivesUnrelatedProfileRemoval(t *testing.T) {
	d, runner, p := coreAuditDaemon(t)
	other, err := profile.NewProfile("Unrelated", profile.SourceManual, "", []model.Node{p.Servers[0].Node})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.store.Add(other); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.autoconnect = true
	d.lastProfile = p.ID
	d.lastNode = p.Servers[0].ID
	d.mu.Unlock()
	queued, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	d.beforeReconnect = func() { close(queued); <-release }
	if !d.AutoconnectOnStart() {
		t.Fatal("autoconnect not queued")
	}
	<-queued
	if result := d.handleRemoveProfile(Request{Cmd: CmdRemoveProfile, Profile: other.ID}); !result.Ok {
		t.Fatal(result.Error)
	}
	unblock()
	d.relaunchWG.Wait()
	waitFor(t, func() bool { return d.snapshotState().State == StateConnected }, "unrelated removal leaves queued intent valid")
	if runner.starts() != 1 || d.snapshotState().Profile != p.ID {
		t.Fatal("unrelated removal invalidated another profile")
	}
}

type controlledProfileStopFailure struct {
	*fakeRunner
	fail atomic.Bool
}

func (r *controlledProfileStopFailure) Stop() error {
	err := r.fakeRunner.Stop()
	if r.fail.Load() {
		return errors.New("controlled profile stop failure")
	}
	return err
}

func TestProfileRemovalKeepsDataWhenTeardownFails(t *testing.T) {
	d, base, p := coreAuditDaemon(t)
	runner := &controlledProfileStopFailure{fakeRunner: base}
	d.runner = runner
	coreAuditConnect(t, d, p, p.Servers[0].ID)
	runner.fail.Store(true)
	t.Cleanup(func() { runner.fail.Store(false) })
	response := d.handleRemoveProfile(Request{Cmd: CmdRemoveProfile, Profile: p.ID})
	if response.Ok || response.Error != "profile_teardown_failed" {
		t.Fatal("failed teardown reported removal success", response.Error)
	}
	if _, exists := d.store.Get(p.ID); !exists {
		t.Fatal("failed teardown deleted profile data")
	}
}
