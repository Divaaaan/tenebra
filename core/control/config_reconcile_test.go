package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/protection"
	"github.com/Divaaaan/tenebra/core/state"
)

func canonicalRuntimeProbeGate(t *testing.T) (*Daemon, *fakeRunner, profile.Profile, func()) {
	t.Helper()
	d, store, _ := newCanonicalTestDaemon(t)
	p, err := profile.NewProfile("Synthetic reconcile", profile.SourceSubscription, "https://provider.test/private", []model.Node{
		{Protocol: model.Trojan, Name: "entry", Server: "entry.test", Port: 443, Password: "SYNTHETIC_ONLY"},
		{Protocol: model.Trojan, Name: "exit", Server: "exit.test", Port: 443, Password: "SYNTHETIC_ONLY"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Profiles().Add(p); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Profiles().Get(p.ID)
	d.UseLegacyEngineProtection()
	d.probeWarmup = time.Millisecond
	d.probeTimeout = 30 * time.Second
	d.probeBudget = 35 * time.Second
	d.healthInterval = time.Hour
	d.tunWatchInterval = time.Hour
	d.ifacePresent = func(string) bool { return true }
	runner := d.runner.(*fakeRunner)
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	runner.onProbe = func(ctx context.Context, n int) {
		if n == 1 {
			close(blocked)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}
	t.Cleanup(func() { unblock(); d.handleDisconnect(Request{Cmd: CmdDisconnect}); d.relaunchWG.Wait() })
	response := d.Handle(context.Background(), Request{Cmd: CmdConnectV2, Profile: p.ID, Selection: &SelectionView{Kind: "pinned", Ref: &NodeRefView{ProfileID: p.ID, NodeID: p.Servers[0].ID, Resolution: "resolved"}}})
	if !response.Ok {
		t.Fatal(response.Error)
	}
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("engine probe never reached the gate")
	}
	return d, runner, p, unblock
}

func TestAcceptedConfigRevisionMatchesTheActualFirstLoop(t *testing.T) {
	d, runner, _, release := canonicalRuntimeProbeGate(t)
	release()
	waitFor(t, func() bool { return d.snapshotState().State == StateConnected }, "initial accepted connection")
	view := d.snapshotState()
	if view.SettingsPending || view.ConfigRevision != view.AppliedConfigRevision || runner.starts() != 1 {
		t.Fatal("accepted current config was reported pending or given the wrong applied revision")
	}
}

func TestReconcileEveryConnectionPreference(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name    string
		request Request
	}{
		{"routing", Request{Cmd: CmdSetRouting, Mode: "direct"}},
		{"split", Request{Cmd: CmdSetSplit, Mode: "include", Apps: []string{"literal[app].exe"}}},
		{"games", Request{Cmd: CmdSetPresets, Games: &on}},
		{"voice", Request{Cmd: CmdSetPresets, Voice: &on}},
		{"services", Request{Cmd: CmdSetPresets, Services: &off}},
		{"kill_switch", Request{Cmd: CmdSetKillSwitch, On: true}},
		{"tls_fragment", Request{Cmd: CmdSetTLSFragment, On: true}},
		{"proxy_mode", Request{Cmd: CmdSetProxyMode, ProxyMode: "system-proxy", ProxyPort: 18181}},
		{"stack", Request{Cmd: CmdSetTun, Stack: "gvisor"}},
		{"remote_dns", Request{Cmd: CmdSetDNS, DNSRemote: "tls://8.8.4.4"}},
		{"direct_dns", Request{Cmd: CmdSetDNS, DNSDirect: "tls://9.9.9.9"}},
		{"ipv4_only", Request{Cmd: CmdSetDNS, IPv4Only: true}},
		{"ad_block", Request{Cmd: CmdSetDNS, AdBlock: true}},
		{"direct_rules", Request{Cmd: CmdSetRules, RulesDirect: []string{"direct.test"}}},
		{"proxy_rules", Request{Cmd: CmdSetRules, RulesProxy: []string{"proxy.test"}}},
		{"banking", Request{Cmd: CmdSetRules, PresetRuBanking: true}},
		{"government", Request{Cmd: CmdSetRules, PresetRuGov: true}},
		{"chain", Request{Cmd: CmdSetMultihopV2, Enabled: true}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			d, runner, p, release := canonicalRuntimeProbeGate(t)
			if item.request.Cmd == CmdSetMultihopV2 {
				item.request.Profile, item.request.EntryID, item.request.ExitID = p.ID, p.Servers[0].ID, p.Servers[1].ID
			}
			before := d.snapshotState()
			if response := d.Handle(context.Background(), item.request); !response.Ok {
				t.Fatal(response.Error)
			}
			pending := d.snapshotState()
			if pending.ConfigRevision <= before.ConfigRevision || !pending.SettingsPending {
				t.Fatal("in-flight change lacked a pending desired revision")
			}
			release()
			waitFor(t, func() bool {
				view := d.snapshotState()
				return view.State == StateConnected && runner.starts() >= 2 && !view.SettingsPending && view.AppliedConfigRevision == view.ConfigRevision
			}, "latest connection preference actually applied")
			if runner.starts() != 2 {
				t.Fatal("one in-flight preference caused redundant engine restarts")
			}
		})
	}
}

func TestCoalescedReconcileSettlesOnLatestInput(t *testing.T) {
	d, runner, p, release := canonicalRuntimeProbeGate(t)
	intent := d.canonical.View().Connection
	if intent.ProfileID != p.ID || intent.Selection.Kind != state.Pinned || intent.Selection.Ref.NodeID != p.Servers[0].ID {
		t.Fatal("the first probe ran before the selected intent was durably committed")
	}
	for index := 1; index <= 10; index++ {
		response := d.Handle(context.Background(), Request{Cmd: CmdSetDNS, DNSDirect: fmt.Sprintf("tls://9.9.9.%d", index)})
		if !response.Ok {
			t.Fatal(response.Error)
		}
	}
	if runner.starts() != 1 {
		t.Fatal("an in-flight batch restarted before the accepted probe")
	}
	release()
	waitFor(t, func() bool {
		view := d.snapshotState()
		return view.State == StateConnected && !view.SettingsPending && view.ConfigRevision == view.AppliedConfigRevision && runner.starts() == 2
	}, "coalesced latest input to settle")
	time.Sleep(30 * time.Millisecond)
	if runner.starts() != 2 || d.snapshotState().DNSDirect != "tls://9.9.9.10" {
		t.Fatal("stable input triggered another process or lost the last setting")
	}
	var config map[string]any
	if err := json.Unmarshal(runner.startCfgs()[1], &config); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(runner.startCfgs()[1], []byte("9.9.9.10")) {
		t.Fatal("the final engine config did not contain the last requested resolver")
	}
}

func TestReconcileRevertedInputAcknowledgesWithoutRestart(t *testing.T) {
	d, runner, _, release := canonicalRuntimeProbeGate(t)
	initial := d.snapshotState()
	for _, resolver := range []string{"tls://9.9.9.9", initial.DNSDirect} {
		if response := d.Handle(context.Background(), Request{Cmd: CmdSetDNS, DNSDirect: resolver}); !response.Ok {
			t.Fatal(response.Error)
		}
	}
	release()
	waitFor(t, func() bool {
		view := d.snapshotState()
		return view.State == StateConnected && !view.SettingsPending && view.ConfigRevision == view.AppliedConfigRevision
	}, "reverted input to acknowledge actual equivalent config")
	if runner.starts() != 1 || d.snapshotState().ConfigRevision <= initial.ConfigRevision {
		t.Fatal("reverted input restarted or reused an old revision")
	}
}

func TestStaleProfileConfigIsRefusedBeforeNativePreparation(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	p := addCanonicalProfile(t, store)
	extra, err := profile.NewProfile("Synthetic extra node", profile.SourceManual, "", []model.Node{
		{Protocol: model.Trojan, Name: "new", Server: "new.test", Port: 443, Password: "SYNTHETIC_ONLY"},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeHostProtection{}
	d.SetProtection(protection.New(backend))
	_, err = d.CommitCanonical(context.Background(), state.AnyRevision, func(snapshot *state.Snapshot) error {
		snapshot.Connection.ProfileID = p.ID
		snapshot.Connection.Selection = state.Selection{Kind: state.Pinned, Ref: state.NodeRef{ProfileID: p.ID, NodeID: p.Servers[0].ID, Resolution: state.Resolved}}
		snapshot.Preferences.KillSwitch = true
		changed := snapshot.Profiles[p.ID]
		changed.Servers = append(changed.Servers, extra.Servers[0])
		snapshot.Profiles[p.ID] = changed
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.startConnect(context.Background(), p, p.Servers[0].ID, false, true, "")
	if !errors.Is(err, state.ErrIntentConflict) {
		t.Fatalf("old captured profile was admitted: %v", err)
	}
	if d.runner.(*fakeRunner).starts() != 0 || len(backend.applied) != 0 {
		t.Fatal("stale profile reached engine or native policy preparation")
	}
}
