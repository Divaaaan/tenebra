package control

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/protection"
	"github.com/Divaaaan/tenebra/core/state"
)

func TestOldProtectionSnapshotRejected(t *testing.T) {
	d, _ := newTestDaemon(t)
	t.Cleanup(d.entCancel)
	newer := protection.State{Status: "blocked", Enforced: true, Persistent: true}
	view, changed := d.applyProtectionSnapshot(newer, 2)
	if !changed || view.Protection != newer {
		t.Fatalf("new policy not published: %#v changed %v", view, changed)
	}
	view, changed = d.applyProtectionSnapshot(protection.State{Status: "off"}, 1)
	if changed || view.Protection != newer {
		t.Fatalf("delayed old policy replaced new state: %#v changed %v", view, changed)
	}
	view, changed = d.applyProtectionSnapshot(protection.State{Status: "off"}, 2)
	if changed || view.Protection != newer {
		t.Fatal("an equal-epoch conflicting callback changed the view")
	}
}

func TestProtectionEpochSurvivesConnectionStatePublication(t *testing.T) {
	d, _ := newTestDaemon(t)
	t.Cleanup(d.entCancel)
	newer := protection.State{Status: "blocked", Enforced: true, Persistent: true}
	d.applyProtectionSnapshot(newer, 2)
	// The fixture's guard still exposes its older initial epoch1. A connection
	// snapshot captured before the new callback must not replace epoch2.
	d.setState(State{State: StateConnecting, Profile: "synthetic-profile"})
	view := d.snapshotState()
	if view.State != StateConnecting || view.Protection != newer {
		t.Fatalf("connection publication applied stale protection: %#v", view)
	}
}

func TestProtectionNotificationStillEmitsAfterSnapshotRead(t *testing.T) {
	d, _ := newTestDaemon(t)
	t.Cleanup(d.entCancel)
	calls := 0
	d.SetEmitter(func(name string, body any) {
		if name == EventState {
			calls++
		}
	})
	_ = d.snapshotState()
	d.emitProtection()
	if calls != 1 {
		t.Fatal("an internal reader consumed the pending policy notification")
	}
}

func TestStateSnapshotsOwnMutablePreferenceFields(t *testing.T) {
	d, _ := newTestDaemon(t)
	t.Cleanup(d.entCancel)
	yes := true
	d.mu.Lock()
	d.state.SplitApps = []string{"synthetic.exe"}
	d.state.RulesDirect = []string{"safe.test"}
	d.state.RulesProxy = []string{"proxy.test"}
	d.state.Multihop = &model.Multihop{Enabled: true, EntryID: "entry", ExitID: "exit"}
	d.state.CrashReports = &yes
	d.mu.Unlock()
	view := d.snapshotState()
	view.SplitApps[0], view.RulesDirect[0], view.RulesProxy[0] = "changed.exe", "changed.test", "changed.test"
	view.Multihop.EntryID = "changed"
	*view.CrashReports = false
	actual := d.snapshotState()
	if actual.SplitApps[0] != "synthetic.exe" || actual.RulesDirect[0] != "safe.test" || actual.RulesProxy[0] != "proxy.test" ||
		actual.Multihop.EntryID != "entry" || !*actual.CrashReports {
		t.Fatal("a returned snapshot mutated the daemon's owned preferences")
	}
}

func TestStateRevisionsAreCapturedWithOwnedViews(t *testing.T) {
	d, _ := newTestDaemon(t)
	t.Cleanup(d.entCancel)
	first := d.snapshotState()
	if first.DaemonInstanceID == "" || first.StateRevision == 0 || first.ConfigRevision == 0 || first.AppliedConfigRevision == 0 {
		t.Fatal("missing positive initial state/config stamps")
	}
	if next := d.snapshotState(); next.StateRevision != first.StateRevision {
		t.Fatal("a read advanced state revision")
	}
	before, _ := json.Marshal(first)
	d.setState(State{State: StateConnecting, Profile: "synthetic"})
	next := d.snapshotState()
	if next.StateRevision <= first.StateRevision || next.DaemonInstanceID != first.DaemonInstanceID {
		t.Fatal("connection view did not receive its own coherent stamp")
	}
	after, _ := json.Marshal(first)
	if !bytes.Equal(before, after) {
		t.Fatal("serializing an older copy minted a current stamp")
	}
	other, _ := newTestDaemon(t)
	t.Cleanup(other.entCancel)
	if other.snapshotState().DaemonInstanceID == first.DaemonInstanceID {
		t.Fatal("two daemon instances shared a wire identity")
	}
}

func TestConfigRevisionIgnoresDisplaySettingsAndTracksEffectivePolicy(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	first := d.snapshotState()
	_, err := d.CommitCanonical(context.Background(), state.AnyRevision, func(snapshot *state.Snapshot) error {
		yes := true
		snapshot.Preferences.CrashReports = &yes
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	display := d.snapshotState()
	if display.ConfigRevision != first.ConfigRevision || display.StateRevision <= first.StateRevision {
		t.Fatal("display choice changed config revision or lacked a state stamp")
	}
	_, err = d.CommitCanonical(context.Background(), state.AnyRevision, func(snapshot *state.Snapshot) error {
		snapshot.Preferences.IPv4Only = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := d.snapshotState()
	if changed.ConfigRevision <= display.ConfigRevision || changed.SettingsPending || changed.AppliedConfigRevision != changed.ConfigRevision {
		t.Fatal("idle desired policy did not settle coherently")
	}
	if store.View().Preferences.IPv4Only != changed.IPv4Only {
		t.Fatal("public view diverged from canonical policy")
	}
}

func TestPublicRevisionsNeverWrapOrExceedSafeJSONIntegers(t *testing.T) {
	last, err := nextPublicRevision(maxPublicRevision - 1)
	if err != nil || last != maxPublicRevision {
		t.Fatal("last safe revision was rejected")
	}
	for _, current := range []uint64{maxPublicRevision, ^uint64(0)} {
		if _, err := nextPublicRevision(current); err == nil {
			t.Fatal("exhausted revision wrapped or became unsafe JSON")
		}
	}
}

func TestStateEventsCarryTheirCapturedFullRevision(t *testing.T) {
	d, _ := newTestDaemon(t)
	t.Cleanup(d.entCancel)
	var emitted State
	d.SetEmitter(func(name string, body any) {
		if name == EventState {
			data, err := json.Marshal(body)
			if err != nil || json.Unmarshal(data, &emitted) != nil {
				t.Error("invalid state event")
			}
		}
	})
	d.setState(State{State: StateConnecting, Profile: "synthetic"})
	if emitted.DaemonInstanceID == "" || emitted.StateRevision == 0 || emitted.Profile != "synthetic" {
		t.Fatal("missing captured full state fields")
	}
	captured := emitted.StateRevision
	d.setState(State{State: StateIdle})
	if captured >= emitted.StateRevision || emitted.Profile != "" {
		t.Fatal("full empty snapshot did not retire previous connection fields")
	}
}
