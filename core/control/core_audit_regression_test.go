package control

import (
	"context"
	"errors"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/fallback"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/zapret"
)

// No Server/Daemon.Close: those cleanups can stop the host's real bypass.
// All engine, network and bypass operations used below are injected.
func coreAuditDaemon(t *testing.T) (*Daemon, *fakeRunner, profile.Profile) {
	t.Helper()
	s, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.NewProfile("audit", profile.SourceManual, "", []model.Node{
		{Protocol: model.VLESS, Name: "Entry", Server: "192.0.2.1", Port: 443, UUID: "123e4567-e89b-12d3-a456-426614174000"},
		{Protocol: model.VLESS, Name: "Exit", Server: "192.0.2.2", Port: 443, UUID: "123e4567-e89b-12d3-a456-426614174001"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Add(p); err != nil {
		t.Fatal(err)
	}
	r := newFakeRunner()
	d := NewDaemon(s, r)
	d.localAddrs = func() []net.Addr { return nil }
	d.tunWatchInterval, d.healthInterval, d.bypassVerifyDelay = 0, 0, 0
	d.proxy = &fakeProxyController{}
	d.classify = func(context.Context, model.Node, bool) fallback.FailureClass { return fallback.Unknown }
	d.probeWarmup, d.probeRetry, d.probeTimeout, d.probeBudget = time.Millisecond, time.Millisecond, 20*time.Millisecond, 30*time.Millisecond
	d.lastGood = fallback.NewMemLastGood()
	d.netFingerprint = func() string { return "test-network" }
	d.zapretExclude = func(string, []string, []zapret.Lookup) (zapret.ExcludeReport, error) {
		return zapret.ExcludeReport{}, nil
	}
	stubStarts(d)
	t.Cleanup(func() {
		d.connMu.Lock()
		d.teardown(StateIdle, "", "")
		d.connMu.Unlock()
		d.relaunchWG.Wait()
		d.entCancel()
	})
	return d, r, p
}

func coreAuditConnect(t *testing.T, d *Daemon, p profile.Profile, node string) State {
	t.Helper()
	d.connMu.Lock()
	_, err := d.startConnect(context.Background(), p, node, false, false, "")
	d.connMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s := d.snapshotState()
		if s.State == StateConnected {
			return s
		}
		if s.State == StateError {
			t.Fatal(s.Error)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no connected state")
	return State{}
}

func TestCoreAuditMultihopUsesEffectiveExit(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	d.multihop = model.Multihop{Enabled: true, EntryID: p.Servers[0].ID, ExitID: p.Servers[1].ID}
	s := coreAuditConnect(t, d, p, p.Servers[0].ID)
	exit, entry := selectedOutboundDetour(t, r.startCfgs()[0])
	if exit != "Exit" || entry != "Entry" {
		t.Fatalf("wrong topology %s via %s", exit, entry)
	}
	if s.Node != p.Servers[1].ID {
		t.Errorf("state node=%s, want exit %s", s.Node, p.Servers[1].ID)
	}
	if id, _ := d.lastGood.Get(p.ID); id != p.Servers[1].ID {
		t.Errorf("last-good=%s, want exit", id)
	}
	if got := d.exitServer(s); got != "192.0.2.2" {
		t.Errorf("leak-check exit=%s, want 192.0.2.2", got)
	}
}

func TestCoreAuditMultihopRejectsStaleOrUnsupportedConnect(t *testing.T) {
	for _, bad := range []string{"missing-entry", "missing-exit", "unsupported-entry", "unsupported-exit"} {
		t.Run(bad, func(t *testing.T) {
			d, r, p := coreAuditDaemon(t)
			d.multihop = model.Multihop{Enabled: true, EntryID: p.Servers[0].ID, ExitID: p.Servers[1].ID}
			switch bad {
			case "missing-entry":
				p.Servers = p.Servers[1:]
			case "missing-exit":
				p.Servers = p.Servers[:1]
			case "unsupported-entry":
				p.Servers[0].Protocol = model.AmneziaWG
			case "unsupported-exit":
				p.Servers[1].Protocol = model.AmneziaWG
			}
			d.connMu.Lock()
			_, err := d.startConnect(context.Background(), p, "", false, false, "")
			d.connMu.Unlock()
			if err == nil {
				t.Error("connect accepted an invalid multihop chain")
			}
			if r.starts() != 0 || r.stops() != 0 {
				t.Errorf("invalid chain touched engine: starts=%d stops=%d", r.starts(), r.stops())
			}
		})
	}
}

func TestCoreAuditMultihopRejectsUnsupportedCommand(t *testing.T) {
	d, _, p := coreAuditDaemon(t)
	p.Servers[0].Protocol = model.AmneziaWG
	if err := d.store.Update(p); err != nil {
		t.Fatal(err)
	}
	r := d.handleSetMultihop(Request{ID: 1, Enabled: true, Profile: p.ID, EntryID: p.Servers[0].ID, ExitID: p.Servers[1].ID})
	if r.Ok || d.multihop.Enabled {
		t.Fatal("unsupported entry accepted and persisted")
	}
}

func TestCoreAuditRefreshRejectsLossOfSelectedMultihop(t *testing.T) {
	d, _, p := coreAuditDaemon(t)
	p.Source, p.URL = profile.SourceSubscription, "https://example.test/sub"
	if err := d.store.Update(p); err != nil {
		t.Fatal(err)
	}
	d.multihop = model.Multihop{Enabled: true, EntryID: p.Servers[0].ID, ExitID: p.Servers[1].ID}
	d.fetch = func(context.Context, string) ([]byte, http.Header, error) {
		return []byte("vless://123e4567-e89b-12d3-a456-426614174001@192.0.2.2:443#Exit"), http.Header{}, nil
	}
	r := d.handleRefreshSubscription(context.Background(), Request{ID: 1, Profile: p.ID})
	if r.Ok {
		t.Error("refresh accepted loss of enabled entry")
	}
	stored, _ := d.store.Get(p.ID)
	if len(stored.Servers) != 2 {
		t.Errorf("refresh replaced valid stored chain: nodes=%d", len(stored.Servers))
	}
}

func TestCoreAuditSelectorPinMustSucceed(t *testing.T) {
	d, r, _ := coreAuditDaemon(t)
	_ = r.Start(context.Background(), nil)
	r.selectErr = errors.New("selector unavailable")
	up, _ := d.probeUntilUp(context.Background(), 0, "desired-node")
	if up {
		t.Fatal("successful probe of unconfirmed selector accepted")
	}
	if len(r.selectCalls()) < 2 {
		t.Error("failed selector was not retried")
	}
}

func TestCoreAuditStartupBypassHonorsOffAfterMutexWait(t *testing.T) {
	d, _, _ := coreAuditDaemon(t)
	seedBypassBundle(t, d.store.Dir(), "general (FAKE TLS AUTO)")
	starts := stubStarts(d)
	d.zapretOpMu.Lock()
	done := make(chan bool, 1)
	go func() { done <- d.autoStartZapret(context.Background(), false) }()
	deadline := time.Now().Add(time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		b := make([]byte, 65536)
		n := runtime.Stack(b, true)
		s := string(b[:n])
		if strings.Contains(s, "(*Daemon).acquireZapretOp") && strings.Contains(s, "(*Daemon).autoStartZapret") {
			waiting = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !waiting {
		d.zapretOpMu.Unlock()
		t.Fatal("startup did not wait for operation mutex")
	}
	d.recordZapretWish(false)
	d.zapretOpMu.Unlock()
	select {
	case up := <-done:
		if up || len(starts.names()) != 0 {
			t.Fatal("startup raised bypass after authoritative OFF")
		}
	case <-time.After(time.Second):
		t.Fatal("startup remained blocked")
	}
	if !d.zapretSwitchedOff() {
		t.Fatal("OFF wish lost")
	}
}

func TestCoreAuditHealthRecoveryRespectsCooldownAndBudget(t *testing.T) {
	for _, steerable := range []bool{false, true} {
		for _, exhausted := range []bool{false, true} {
			d, r, p := coreAuditDaemon(t)
			_ = r.Start(context.Background(), nil)
			d.generation = 1
			d.setState(State{State: StateConnected, Profile: p.ID, Node: p.Servers[0].ID})
			if steerable {
				d.setLiveConfig(1, p.ID, serverTags(p), selectorShape{Default: "Entry", Members: []string{"Entry", "Exit"}})
			}
			d.autoSwitches = []time.Time{d.now()}
			if exhausted {
				d.autoSwitches = nil
				for i := 0; i < d.maxAutoSwitches; i++ {
					d.autoSwitches = append(d.autoSwitches, d.now().Add(-d.autoSwitchCooldown-time.Second))
				}
			}
			d.healthInterval, d.healthFailThreshold = time.Millisecond, 1
			d.healthProbe = func(context.Context) error { return errors.New("synthetic unhealthy tunnel") }
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			d.healthWatch(ctx, 1, p.ID, p.Servers[0].ID)
			cancel()
			d.relaunchWG.Wait()
			if r.starts() != 1 {
				t.Errorf("steerable=%v exhausted=%v: recovery bypassed policy, starts=%d", steerable, exhausted, r.starts())
			}
			if d.snapshotState().Node != p.Servers[0].ID {
				t.Errorf("steerable=%v exhausted=%v: recovery switched exit despite policy", steerable, exhausted)
			}
		}
	}
}

func TestCoreAuditFallbackReconnectConsumesSharedBudget(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	_ = r.Start(context.Background(), nil)
	r.selectErr, r.selectErrThroughStart = errors.New("old API unavailable"), 1
	d.generation = 1
	d.setState(State{State: StateConnected, Profile: p.ID, Node: p.Servers[0].ID})
	if got := d.healthFailover(1, p.ID, p.Servers[0].ID); got != failoverStarted {
		t.Fatalf("reconnect not scheduled: %v", got)
	}
	d.relaunchWG.Wait()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d.snapshotState().State == StateConnected {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s := d.snapshotState()
	if s.State != StateConnected || s.Node != p.Servers[1].ID {
		t.Fatalf("fallback did not connect alternative: %+v", s)
	}
	d.mu.Lock()
	gen := d.generation
	spent := len(d.autoSwitches)
	d.mu.Unlock()
	if spent != 1 {
		t.Errorf("fallback consumed %d budget entries, want 1", spent)
	}
	if d.allowAutoSwitch(gen, p.ID, p.Servers[1].ID) {
		t.Error("fallback reconnect did not constrain subsequent live switch")
	}
	if got := d.healthFailover(gen, p.ID, p.Servers[1].ID); got == failoverStarted {
		t.Error("second fallback scheduled inside cooldown")
	}
}

func TestCoreAuditQueuedRecoveryYieldsToManualSwitch(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	_ = r.Start(context.Background(), nil)
	d.generation = 1
	d.setState(State{State: StateConnected, Profile: p.ID, Node: p.Servers[0].ID})
	queued, release := make(chan struct{}), make(chan struct{})
	d.beforeReconnect = func() { close(queued); <-release }
	if d.healthFailover(1, p.ID, p.Servers[0].ID) != failoverStarted {
		t.Fatal("recovery not scheduled")
	}
	<-queued
	d.setState(State{State: StateConnected, Profile: p.ID, Node: p.Servers[1].ID})
	close(release)
	d.relaunchWG.Wait()
	if r.starts() != 1 || d.snapshotState().Node != p.Servers[1].ID {
		t.Fatal("queued recovery overruled manual switch")
	}
	if len(d.autoSwitches) != 0 {
		t.Fatal("cancelled recovery spent a budget entry")
	}
}

func TestCoreAuditMultihopHasNoAutomaticAlternativeExit(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	d.multihop = model.Multihop{Enabled: true, EntryID: p.Servers[0].ID, ExitID: p.Servers[1].ID}
	_ = r.Start(context.Background(), nil)
	d.generation = 1
	d.setState(State{State: StateConnected, Profile: p.ID, Node: p.Servers[1].ID})
	if got := d.healthFailover(1, p.ID, p.Servers[1].ID); got != failoverNoAlternative {
		t.Errorf("fixed chain scheduled another exit: %v", got)
	}
	d.relaunchWG.Wait()
	if len(d.autoSwitches) != 0 {
		t.Error("unavailable chain failover spent budget")
	}
}

type retryPinRunner struct {
	*fakeRunner
	failures int
}

func (r *retryPinRunner) Select(ctx context.Context, group, tag string) error {
	if err := r.fakeRunner.Select(ctx, group, tag); err != nil {
		return err
	}
	if r.failures > 0 {
		r.failures--
		return errors.New("selector not ready")
	}
	return nil
}

func TestCoreAuditSelectorRetriesBeforeTestingTraffic(t *testing.T) {
	d, r, _ := coreAuditDaemon(t)
	_ = r.Start(context.Background(), nil)
	d.runner = &retryPinRunner{fakeRunner: r, failures: 2}
	up, _ := d.probeUntilUp(context.Background(), 0, "Exit")
	if !up {
		t.Fatal("eventual successful selector pin did not connect")
	}
	if got := len(r.selectCalls()); got != 3 {
		t.Errorf("pin calls=%d, want 3", got)
	}
	r.mu.Lock()
	probes := r.probeN
	r.mu.Unlock()
	if probes != 1 {
		t.Errorf("traffic tested %d times, want only after confirmed pin", probes)
	}
}

func TestCoreAuditHealthWatchSurvivesCancelledQueuedRecovery(t *testing.T) {
	d, r, p := coreAuditDaemon(t)
	_ = r.Start(context.Background(), nil)
	d.generation = 1
	d.setState(State{State: StateConnected, Profile: p.ID, Node: p.Servers[0].ID})
	d.healthInterval, d.healthFailThreshold = time.Millisecond, 1
	queued, release, probedAgain := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	d.beforeReconnect = func() { close(queued); <-release }
	calls := 0
	d.healthProbe = func(context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("unhealthy")
		}
		select {
		case probedAgain <- struct{}{}:
		default:
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.healthWatch(ctx, 1, p.ID, p.Servers[0].ID); close(done) }()
	<-queued
	d.setState(State{State: StateConnected, Profile: p.ID, Node: p.Servers[1].ID})
	close(release)
	d.relaunchWG.Wait()
	select {
	case <-probedAgain:
	case <-time.After(40 * time.Millisecond):
		t.Error("watchdog stopped after queued recovery yielded to user")
	}
	cancel()
	<-done
}
