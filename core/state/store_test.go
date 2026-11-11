package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

type fakeRepository struct {
	mu                        sync.Mutex
	bundle                    LegacyBundle
	backupError, replaceError error
	commit                    securefs.CommitState
	backups, writes           int
	order                     []string
	entered, release          chan struct{}
}

func cloneLegacy(bundle LegacyBundle) LegacyBundle {
	copyFile := func(file LegacyFile) LegacyFile { return LegacyFile{file.Present, bytes.Clone(file.Bytes)} }
	return LegacyBundle{copyFile(bundle.Profiles), copyFile(bundle.Settings), copyFile(bundle.LastGood)}
}
func (f *fakeRepository) ReadLegacy(ctx context.Context) (LegacyBundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneLegacy(f.bundle), nil
}
func (f *fakeRepository) Backup(ctx context.Context, bundle LegacyBundle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.backups++
	f.order = append(f.order, "backup")
	return f.backupError
}
func (f *fakeRepository) ReplaceCanonical(ctx context.Context, data []byte) (securefs.CommitState, error) {
	if f.entered != nil {
		close(f.entered)
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	f.order = append(f.order, "replace")
	if f.commit != securefs.Unchanged {
		f.bundle.Profiles = LegacyFile{true, bytes.Clone(data)}
	}
	return f.commit, f.replaceError
}

type testWriter struct{ calls int }

func (w *testWriter) Close() error { w.calls++; return nil }

func canonicalFixture(t *testing.T) Snapshot {
	t.Helper()
	snapshot, err := MigrateLegacy(bundleFor(t, []profile.Profile{{ID: "p", Name: "original", Servers: []profile.Server{legacyNode("old", "a.test")}}}, `{"version":2,"kill_switch":true}`, ""), nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func openFakeCanonical(t *testing.T) (*Store, *fakeRepository) {
	t.Helper()
	data, err := json.Marshal(canonicalFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	files := &fakeRepository{bundle: LegacyBundle{Profiles: LegacyFile{true, data}}, commit: securefs.Committed}
	store, err := openWithFiles(context.Background(), "synthetic-metadata-only", files, &testWriter{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, files
}

func TestCanonicalTransactGlobalCASAndDeepCopies(t *testing.T) {
	store, files := openFakeCanonical(t)
	if !errors.Is(store.Ready(), ErrMaintenancePending) {
		t.Fatal("schema opening admitted business writes")
	}
	if err := store.Admit(); err != nil {
		t.Fatal(err)
	}
	before := store.View()
	calls := 0
	if result, err := store.Transact(context.Background(), before.Revision+1, func(*Snapshot) error { calls++; return nil }); !errors.Is(err, ErrRevisionConflict) || calls != 0 || result.Revision != 0 {
		t.Fatal("stale global CAS invoked callback")
	}
	view := store.View()
	p := view.Profiles["p"]
	p.Servers[0].TLS.ServerName = "mutated caller copy"
	view.Profiles["p"] = p
	if store.View().Profiles["p"].Servers[0].TLS.ServerName != "a.test" {
		t.Fatal("View returned mutable aliases")
	}
	var retained *Snapshot
	result, err := store.Transact(context.Background(), before.Revision, func(candidate *Snapshot) error {
		retained = candidate
		candidate.Preferences.AdBlock = true
		return nil
	})
	if err != nil || result.Revision != before.Revision+1 || !result.Preferences.AdBlock {
		t.Fatalf("confirmed commit not published: %v", err)
	}
	retained.Preferences.AdBlock = false
	retained.Profiles["p"].Servers[0].TLS.ServerName = "callback-held-mutation"
	result.Preferences.AdBlock = false
	result.Profiles["p"].Servers[0].TLS.ServerName = "result-held-mutation"
	if !store.View().Preferences.AdBlock {
		t.Fatal("callback/result alias mutated committed snapshot")
	}
	if store.View().Profiles["p"].Servers[0].TLS.ServerName != "a.test" {
		t.Fatal("retained callback/result nested alias mutated committed snapshot")
	}
	writes := files.writes
	revision := store.View().Revision
	if _, err := store.Transact(context.Background(), AnyRevision, func(*Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if files.writes != writes || store.View().Revision != revision {
		t.Fatal("no-op rewrote canonical state or revision")
	}
}

func TestCanonicalRejectsOpaqueConnectionRebind(t *testing.T) {
	store, files := openFakeCanonical(t)
	store.Admit()
	before := store.View()
	bytesBefore := bytes.Clone(files.bundle.Profiles.Bytes)
	if _, err := store.Transact(context.Background(), AnyRevision, func(candidate *Snapshot) error {
		p := candidate.Profiles["p"]
		p.Servers[0].TLS.ServerName = "different.test"
		candidate.Profiles["p"] = p
		return nil
	}); !errors.Is(err, profile.ErrIdentityRebind) {
		t.Fatal("store accepted same ID/new connection")
	}
	if store.View().Revision != before.Revision || !bytes.Equal(bytesBefore, files.bundle.Profiles.Bytes) {
		t.Fatal("rebind refusal changed canonical data")
	}
}

func TestCanonicalMigrationCrashBoundaries(t *testing.T) {
	for _, outcome := range []securefs.CommitState{securefs.Unchanged, securefs.Indeterminate} {
		t.Run(map[securefs.CommitState]string{securefs.Unchanged: "before_replace", securefs.Indeterminate: "uncertain_after_replace"}[outcome], func(t *testing.T) {
			store, files := openFakeCanonical(t)
			store.Admit()
			before := store.View()
			files.commit = outcome
			failure := errors.New("synthetic final IO fault")
			files.replaceError = failure
			result, err := store.Transact(context.Background(), AnyRevision, func(candidate *Snapshot) error { candidate.Preferences.AdBlock = true; return nil })
			if err == nil || result.Revision != 0 || store.View().Revision != before.Revision {
				t.Fatal("failed/uncertain replacement was projected")
			}
			if outcome == securefs.Indeterminate {
				if !errors.Is(store.Ready(), ErrDurabilityUnknown) {
					t.Fatal("ambiguous durable outcome did not poison readiness")
				}
				calls := 0
				if _, err := store.Transact(context.Background(), AnyRevision, func(*Snapshot) error { calls++; return nil }); !errors.Is(err, ErrDurabilityUnknown) || calls != 0 {
					t.Fatal("uncertain store admitted another writer")
				}
			} else if !errors.Is(err, failure) || store.Ready() != nil {
				t.Fatal("confirmed unchanged failure incorrectly poisoned or hid IO cause")
			}
		})
	}
}

func TestCanonicalBackupBeforeReplaceAndUnknownRootNeverFallsBack(t *testing.T) {
	legacy := bundleFor(t, []profile.Profile{{ID: "p", Name: "n", Servers: []profile.Server{legacyNode("old", "a.test")}}}, `{"version":2}`, "")
	files := &fakeRepository{bundle: legacy, commit: securefs.Committed}
	writer := &testWriter{}
	store, err := openWithFiles(context.Background(), "metadata", files, writer)
	if err != nil {
		t.Fatal(err)
	}
	if len(files.order) != 2 || files.order[0] != "backup" || files.order[1] != "replace" {
		t.Fatal("canonical replace preceded durable private backup")
	}
	store.Close()
	for _, root := range []string{`{"schemaVersion":99}`, `{broken`} {
		files := &fakeRepository{bundle: legacy, commit: securefs.Committed}
		files.bundle.Profiles = LegacyFile{true, []byte(root)}
		writer := &testWriter{}
		if store, err := openWithFiles(context.Background(), "metadata", files, writer); err == nil {
			store.Close()
			t.Fatal("bad canonical root fell back to stale legacy/defaults")
		}
		if files.writes != 0 || writer.calls != 1 {
			t.Fatal("refused startup wrote data or retained lock")
		}
	}
}
