package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func putBundle(t *testing.T, data *securefs.Root, bundle LegacyBundle) {
	t.Helper()
	for name, file := range legacyFiles(bundle) {
		if file.Present {
			if state, err := data.ReplaceFile(name, file.Bytes); err != nil || state != securefs.Committed {
				t.Fatal(state, err)
			}
		}
	}
}
func requireBundle(t *testing.T, data *securefs.Root, expected LegacyBundle) {
	t.Helper()
	for name, file := range legacyFiles(expected) {
		actual, err := data.ReadFile(name, canonicalFileLimit)
		if !file.Present {
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("%s absence changed: %v", name, err)
			}
			continue
		}
		if err != nil || !bytes.Equal(actual, file.Bytes) {
			t.Fatalf("%s source changed: %v", name, err)
		}
	}
}
func TestCanonicalHeldOwnershipAndWholeCheckpointRestore(t *testing.T) {
	ctx := context.Background()
	data, backup := nativeStateRoots(t)
	before := bundleFor(t, []profile.Profile{{ID: "keep", Name: "before", Servers: []profile.Server{legacyNode("stored", "sni.test")}}}, `{"version":2,"kill_switch":true}`, `{"keep":"stored"}`)
	putBundle(t, data, before)
	held, err := OpenHeld(ctx, data, backup)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Close() })
	if _, err := Open(ctx, data, backup); !errors.Is(err, securefs.ErrBusy) {
		t.Fatalf("normal owner competed with held writer: %v", err)
	}
	if _, err := OpenHeld(ctx, data, backup); !errors.Is(err, securefs.ErrBusy) {
		t.Fatalf("second held writer: %v", err)
	}
	ref, err := held.Checkpoint(ctx, "tx-test")
	if err != nil {
		t.Fatal(err)
	}
	requireBundle(t, data, before)
	if again, err := held.Checkpoint(ctx, "tx-test"); err != nil || again != ref {
		t.Fatal("checkpoint retry changed reference", err)
	}
	receipt, err := held.Migrate(ctx, "tx-test")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Schema != SchemaVersion || receipt.MigrationID == "" || receipt.Revision != 1 || !validChecksum(receipt.CanonicalSHA256) {
		t.Fatal(receipt)
	}
	if again, err := held.Migrate(ctx, "tx-test"); err != nil || again != receipt {
		t.Fatal("migration retry rekeyed snapshot", err)
	}
	if got, err := held.Inspect(ctx); err != nil || got != receipt {
		t.Fatal("held inspection differs", err)
	}
	if _, err := Open(ctx, data, backup); !errors.Is(err, securefs.ErrBusy) {
		t.Fatal("borrowed Store.Close released held kernel lock", err)
	}
	bad := ref
	bad.SHA256 = checksum([]byte("wrong"))
	if err := held.Restore(ctx, "tx-test", bad); !errors.Is(err, ErrCorruptState) {
		t.Fatal("wrong checkpoint accepted", err)
	}
	if state, err := data.ReplaceFile("settings.json", []byte("changed")); err != nil || state != securefs.Committed {
		t.Fatal(err)
	}
	if err := held.Restore(ctx, "tx-test", ref); err != nil {
		t.Fatal(err)
	}
	requireBundle(t, data, before)
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := held.Inspect(ctx); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	store, err := Open(ctx, data, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !errors.Is(store.Ready(), ErrMaintenancePending) {
		t.Fatal("normal reopen admitted business")
	}
}
func TestCanonicalHeldCheckpointPreservesObsoleteSideFilesExactly(t *testing.T) {
	ctx := context.Background()
	data, backup := nativeStateRoots(t)
	snapshot := canonicalFixture(t)
	root, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	before := LegacyBundle{LegacyFile{true, root}, LegacyFile{true, []byte("{CORRUPT_OBSOLETE_PREFS")}, LegacyFile{true, []byte("old hints\n")}}
	putBundle(t, data, before)
	held, err := OpenHeld(ctx, data, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ref, err := held.Checkpoint(ctx, "canonical-test")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Revision != snapshot.Revision {
		t.Fatal(ref)
	}
	record, err := held.readRecord("canonical-test")
	if err != nil {
		t.Fatal(err)
	}
	copy, err := held.readCheckpoint(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	if !equalJSON(copy, before) {
		t.Fatal("whole checkpoint lost stale side files")
	}
	if _, err := held.Migrate(ctx, "canonical-test"); err != nil {
		t.Fatal("canonical migration incorrectly decoded stale side files", err)
	}
	if err := held.Restore(ctx, "canonical-test", ref); err != nil {
		t.Fatal(err)
	}
	requireBundle(t, data, before)
}
func TestCanonicalHeldAbsentBeforeRestoreRetainsArchiveAndRepeats(t *testing.T) {
	ctx := context.Background()
	data, backup := nativeStateRoots(t)
	before := LegacyBundle{Settings: LegacyFile{true, []byte(`{"version":2,"kill_switch":true}`)}}
	putBundle(t, data, before)
	held, err := OpenHeld(ctx, data, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ref, err := held.Checkpoint(ctx, "first-install")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.Migrate(ctx, "first-install"); err != nil {
		t.Fatal(err)
	}
	changed := []byte(`{"version":2,"kill_switch":false}`)
	if _, err := data.ReplaceFile("settings.json", changed); err != nil {
		t.Fatal(err)
	}
	canonical, err := data.ReadFile("profiles.json", canonicalFileLimit)
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Restore(ctx, "first-install", ref); err != nil {
		t.Fatal(err)
	}
	requireBundle(t, data, before)
	record, err := held.readRecord("first-install")
	if err != nil {
		t.Fatal(err)
	}
	if !record.RestoreCompleted || record.RestoreDetaches["profiles.json"].SHA256 != checksum(canonical) {
		t.Fatal("absence restored without bound durable plan/receipt")
	}
	if err := held.Restore(ctx, "first-install", ref); err != nil {
		t.Fatal("repeated restore did not verify completed detach", err)
	}
	requireBundle(t, data, before)
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	held, err = OpenHeld(ctx, data, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := held.Restore(ctx, "first-install", ref); err != nil {
		t.Fatal("fresh held owner lost proof of completed detach", err)
	}
	requireBundle(t, data, before)
}
func TestCanonicalHeldRejectsInvalidTransactionAndChangedLineage(t *testing.T) {
	ctx := context.Background()
	data, backup := nativeStateRoots(t)
	putBundle(t, data, LegacyBundle{Profiles: LegacyFile{true, []byte("[]")}})
	held, err := OpenHeld(ctx, data, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	for _, id := range []string{"", "../x", "UPPER", "with space"} {
		if _, err := held.Checkpoint(ctx, id); err == nil {
			t.Fatal("invalid transaction accepted", id)
		}
	}
	if _, err := held.Checkpoint(ctx, "lineage-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := data.ReplaceFile("profiles.json", []byte("[ ]")); err != nil {
		t.Fatal(err)
	}
	if _, err := held.Migrate(ctx, "lineage-test"); !errors.Is(err, ErrDurabilityUnknown) {
		t.Fatal("changed source was remigrated", err)
	}
	got, err := data.ReadFile("profiles.json", 64)
	if err != nil || string(got) != "[ ]" {
		t.Fatal("lineage refusal changed source", err)
	}
}

func TestCanonicalHeldRejectsChangedLegacySideFilesAfterCheckpoint(t *testing.T) {
	ctx := context.Background()
	data, backup := nativeStateRoots(t)
	before := LegacyBundle{Profiles: LegacyFile{true, []byte("[]")}, Settings: LegacyFile{true, []byte(`{"version":2,"kill_switch":true}`)}}
	putBundle(t, data, before)
	held, err := OpenHeld(ctx, data, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := held.Checkpoint(ctx, "side-lineage"); err != nil {
		t.Fatal(err)
	}
	changed := []byte(`{"version":2,"kill_switch":false}`)
	if _, err := data.ReplaceFile("settings.json", changed); err != nil {
		t.Fatal(err)
	}
	if _, err := held.Migrate(ctx, "side-lineage"); !errors.Is(err, ErrDurabilityUnknown) {
		t.Fatal("mixed legacy checkpoint/current side files migrated", err)
	}
	requireBundle(t, data, LegacyBundle{Profiles: before.Profiles, Settings: LegacyFile{true, changed}})
}
