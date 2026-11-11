package installtx

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

type actualStoreGate struct {
	live       *state.Store
	admitCalls int
}

func (g *actualStoreGate) Fence(ctx context.Context, _ string) error {
	if g.live == nil {
		return nil
	}
	return g.live.Fence(ctx)
}
func (g *actualStoreGate) Admit(ctx context.Context, tx, oc string) error {
	g.admitCalls++
	return g.live.Admit()
}
func actualInstallerRoots(t *testing.T) (*securefs.Root, *securefs.Root) {
	t.Helper()
	root := privateInstallerRoot(t)
	data, err := root.OpenDir("data", securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := root.OpenDir("backup", securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backup.Close(); err != nil {
			t.Error(err)
		}
		if err := data.Close(); err != nil {
			t.Error(err)
		}
	})
	return data, backup
}
func writeActualLegacy(t *testing.T, data *securefs.Root) map[string][]byte {
	t.Helper()
	files := map[string][]byte{
		"profiles.json": []byte(`[{"id":"profile-kept","name":"before","source":"manual","nodes":[{"id":"legacy-node","protocol":"trojan","name":"synthetic","server":"example.test","port":443,"password":"SYNTHETIC_ONLY"}]}]`),
		"settings.json": []byte(`{"version":2,"kill_switch":true}`),
		"lastgood.json": []byte("{}\n"),
	}
	for name, body := range files {
		if outcome, err := data.ReplaceFile(name, body); err != nil || outcome != securefs.Committed {
			t.Fatal(outcome, err)
		}
	}
	return files
}
func heldPortJournal(d *installDisk) string {
	id := "it1_" + strings.Repeat("8", 32)
	runtime := testRuntime()
	d.journal.Pending = &runtime
	d.journal.Transaction = &Transaction{ID: id, Action: "install", Phase: "fencing", Trust: "release", PriorActive: d.journal.Active, Candidate: &runtime}
	return id
}
func TestHeldPortRetainsCheckpointWriterAndReleasesBeforeCandidateOpen(t *testing.T) {
	ctx := context.Background()
	data, backup := actualInstallerRoots(t)
	writeActualLegacy(t, data)
	old, err := state.Open(ctx, data, backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Admit(); err != nil {
		t.Fatal(err)
	}
	before := old.View()
	oldNodeID := before.Profiles["profile-kept"].Servers[0].ID
	d := installerDisk()
	tx := heldPortJournal(d)
	gate := &actualStoreGate{live: old}
	port, err := NewHeldStorePort(data, backup, fakeInstallFS{d}, gate)
	if err != nil {
		t.Fatal(err)
	}
	defer port.Close()
	if err := port.Fence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Transact(ctx, state.AnyRevision, func(*state.Snapshot) error { return nil }); !errors.Is(err, state.ErrMaintenancePending) {
		t.Fatal("old writer remained admitted", err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	gate.live = nil
	ref, err := port.Checkpoint(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if competing, err := state.Open(ctx, data, backup); !errors.Is(err, securefs.ErrBusy) {
		if competing != nil {
			competing.Close()
		}
		t.Fatal("checkpoint released sole held writer", err)
	}
	d.journal.Transaction.Checkpoint = &ref
	d.journal.Transaction.Phase = "fenced"
	receipt, err := port.MigrateHeld(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := state.Open(ctx, data, backup)
	if err != nil {
		t.Fatal("held owner blocked new core Store", err)
	}
	defer candidate.Close()
	gate.live = candidate
	if err := candidate.Ready(); !errors.Is(err, state.ErrMaintenancePending) {
		t.Fatal("candidate admitted before operational commit", err)
	}
	if err := port.Admit(ctx, tx, "oc1_"+strings.Repeat("9", 32)); !errors.Is(err, ErrRecoveryRequired) || gate.admitCalls != 0 {
		t.Fatal("precommit admit reached writer", err)
	}
	d.journal.Transaction.SchemaReceipt = &receipt
	d.journal.Transaction.OperationalCommitID = "oc1_" + strings.Repeat("9", 32)
	d.journal.Transaction.Phase = "committed_closed"
	d.journal.Active = d.journal.Transaction.Candidate
	d.journal.Pending = nil
	if err := port.Admit(ctx, tx, d.journal.Transaction.OperationalCommitID); err != nil {
		t.Fatal(err)
	}
	if _, err := candidate.Transact(ctx, state.AnyRevision, func(s *state.Snapshot) error { s.Preferences.KillSwitch = false; return nil }); err != nil {
		t.Fatal(err)
	}
	newProfile := candidate.View().Profiles["profile-kept"]
	newProfile.ID = "new-profile"
	newProfile.Name = "later"
	newProfile.Revision = 1
	newProfile.Servers = append([]profile.Server{}, newProfile.Servers...)
	newProfile.Servers[0].ID = "n2_" + strings.Repeat("a", 32)
	if err := candidate.Profiles().Add(newProfile); err != nil {
		t.Fatal(err)
	}
	latest := candidate.View()
	if err := port.Fence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	gate.live = nil
	if err := port.Close(); err != nil {
		t.Fatal(err)
	}
	reconstructed, err := NewHeldStorePort(data, backup, fakeInstallFS{d}, gate)
	if err != nil {
		t.Fatal(err)
	}
	defer reconstructed.Close()
	observed, err := reconstructed.Inspect(ctx)
	if err != nil || !validPostCommitLineage(observed, receipt) || observed.Revision != latest.Revision {
		t.Fatal("latest lineage lost", observed, err)
	}
	if err := reconstructed.RestoreHeld(ctx, tx, ref); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("postcommit restore allowed", err)
	}
	reopened, err := state.Open(ctx, data, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	view := reopened.View()
	if view.Preferences.KillSwitch || view.Profiles["new-profile"].Servers[0].ID != newProfile.Servers[0].ID ||
		view.Profiles["profile-kept"].Servers[0].ID != oldNodeID || view.Revision != latest.Revision {
		t.Fatal("real postcommit state/IDs changed")
	}
}
func TestHeldPortReconstructedWholeLegacyRestore(t *testing.T) {
	ctx := context.Background()
	data, backup := actualInstallerRoots(t)
	before := writeActualLegacy(t, data)
	d := installerDisk()
	tx := heldPortJournal(d)
	gate := &actualStoreGate{}
	port, err := NewHeldStorePort(data, backup, fakeInstallFS{d}, gate)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := port.Checkpoint(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	d.journal.Transaction.Checkpoint = &ref
	d.journal.Transaction.Phase = "fenced"
	receipt, err := port.MigrateHeld(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := port.Close(); err != nil {
		t.Fatal(err)
	}
	d.journal.Transaction.SchemaReceipt = &receipt
	d.journal.Transaction.Phase = "rolling_back"
	reconstructed, err := NewHeldStorePort(data, backup, fakeInstallFS{d}, gate)
	if err != nil {
		t.Fatal(err)
	}
	defer reconstructed.Close()
	if err := reconstructed.RestoreHeld(ctx, tx, ref); err != nil {
		t.Fatal(err)
	}
	if err := reconstructed.RestoreHeld(ctx, tx, ref); err != nil {
		t.Fatal("whole restore retry failed", err)
	}
	for name, expected := range before {
		got, err := data.ReadFile(name, 1<<20)
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatalf("whole legacy snapshot changed: %s err=%v", name, err)
		}
	}
	if gate.admitCalls != 0 {
		t.Fatal("precommit rollback opened writers")
	}
}
