package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

// This model tests the repository's response to durability outcomes. Native
// write/sync/rename behavior belongs to securefs tests, not this fake.
type crashRepository struct {
	durable         LegacyBundle
	backup          LegacyBundle
	fault           string
	backupConfirmed bool
	replaceCalls    int
}

var errCrash = errors.New("synthetic crash boundary")

func (f *crashRepository) ReadLegacy(context.Context) (LegacyBundle, error) {
	return cloneLegacyFiles(f.durable), nil
}
func (f *crashRepository) Backup(_ context.Context, bundle LegacyBundle) error {
	switch f.fault {
	case "backup-create", "backup-write", "backup-sync", "backup-manifest", "backup-publish", "backup-readback":
		return errCrash
	}
	f.backup = cloneLegacyFiles(bundle)
	f.backupConfirmed = true
	return nil
}
func (f *crashRepository) ReplaceCanonical(_ context.Context, data []byte) (securefs.CommitState, error) {
	f.replaceCalls++
	if !f.backupConfirmed {
		return securefs.Unchanged, errors.New("replace before confirmed backup")
	}
	switch f.fault {
	case "candidate-create", "candidate-write", "candidate-sync", "candidate-close", "candidate-readback", "candidate-replace":
		return securefs.Unchanged, errCrash
	}
	f.durable.Profiles = LegacyFile{true, bytes.Clone(data)}
	if f.fault == "final-sync" {
		return securefs.Indeterminate, errCrash
	}
	return securefs.Committed, nil
}
func TestCanonicalMigrationCrashBoundariesReconstructRepository(t *testing.T) {
	before := bundleFor(t, []profile.Profile{{ID: "preserved", Name: "before", Servers: []profile.Server{legacyNode("stored", "sni.test")}}}, `{"version":2,"kill_switch":true,"last_profile":"preserved","last_node":"stored"}`, `{"preserved":"stored"}`)
	for _, point := range []string{"backup-create", "backup-write", "backup-sync", "backup-manifest", "backup-publish", "backup-readback", "candidate-create", "candidate-write", "candidate-sync", "candidate-close", "candidate-readback", "candidate-replace", "final-sync", "confirmed"} {
		t.Run(point, func(t *testing.T) {
			files := &crashRepository{durable: cloneLegacyFiles(before), fault: point}
			writer := &testWriter{}
			store, err := openWithAllocator(context.Background(), "metadata", files, writer, nextTestID())
			if point != "confirmed" {
				if store != nil || !errors.Is(err, errCrash) || writer.calls != 1 {
					t.Fatalf("failed open published owner: %v", err)
				}
				if point == "final-sync" && !errors.Is(err, ErrDurabilityUnknown) {
					t.Fatal("uncertain durability reported as ordinary failure", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !errors.Is(store.Ready(), ErrMaintenancePending) {
					t.Fatal("migration opened business admission")
				}
				store.Close()
			}
			if point != "confirmed" && point != "final-sync" {
				if !equalJSON(files.durable, before) {
					t.Fatal("pre-replace failure changed durable source")
				}
				if !files.backupConfirmed && files.replaceCalls != 0 {
					t.Fatal("replace attempted before confirmed backup")
				}
				return
			}
			if !equalJSON(files.backup, before) {
				t.Fatal("confirmed backup differs from source bytes/presence")
			}
			var accepted Snapshot
			if err := json.Unmarshal(files.durable.Profiles.Bytes, &accepted); err != nil {
				t.Fatal(err)
			}
			if err := accepted.Validate(); err != nil {
				t.Fatal(err)
			}
			if !accepted.Preferences.KillSwitch || accepted.Connection.Selection.Kind != Pinned {
				t.Fatal("durable candidate erased required intent")
			}
			// Reconstruct a distinct repository/owner, with RNG unavailable and no
			// backup state in memory. Schema-2 is the only business input.
			reopened := &fakeRepository{bundle: cloneLegacyFiles(files.durable), commit: securefs.Committed}
			next, err := openWithAllocator(context.Background(), "metadata", reopened, &testWriter{}, func() (string, error) { return "", errors.New("RNG must not run") })
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			if !equalJSON(next.View(), accepted) || reopened.backups != 0 || reopened.writes != 0 {
				t.Fatal("reopen rolled back, rekeyed, or rewrote durable candidate")
			}
		})
	}
}
