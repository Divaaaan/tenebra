//go:build linux

package installtx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func ownedReadyFixture(t *testing.T) (*CoreLauncher, RuntimeRef, *fixtureReadyWriter, *securefs.Root, *securefs.Root, string) {
	t.Helper()
	l, ref, w := coreProcessFixture(t)
	releases, err := l.releases.Path()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(filepath.Dir(releases), "maintenance")
	policy := securefs.Policy{Mode: securefs.MachinePrivate}
	ready, err := securefs.OpenRoot(filepath.Join(base, "ready"), policy, true)
	if err != nil {
		t.Fatal(err)
	}
	owners, err := securefs.OpenRoot(filepath.Join(base, "socket-owner"), policy, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owners.Close(); err != nil {
			t.Error(err)
		}
		if err := ready.Close(); err != nil {
			t.Error(err)
		}
	})
	return l, ref, w, ready, owners, filepath.Join(base, "ready", "it1_"+strings.Repeat("a", 32)+".sock")
}
func TestReadyOwnerRealCrashRecoveryRefusesLiveAndPreservesForeignSocket(t *testing.T) {
	l, ref, w, ready, owners, path := ownedReadyFixture(t)
	child := startFixtureCore(t, l, ref, w, strings.Repeat("d", 32))
	record, err := ReadReadySocketOwner(context.Background(), owners)
	if err != nil || record.Generation != 1 || record.Owner == nil || record.Owner.Process() != child.Identity() || record.Owner.Socket.Validate() != nil {
		t.Fatal("durable owner was not the actual spawned kernel lifetime", record, err)
	}
	if err := CleanupPrivateReady(context.Background(), ready, owners); !errors.Is(err, ErrNotReady) {
		t.Fatal("live owner node was removed", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("live node disappeared", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := child.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatal("forced crash did not leave the actual socket residue", err)
	}
	if err := CleanupPrivateReady(ctx, ready, owners); err != nil {
		t.Fatal("confirmed dead owner could not recover exact node", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("old owned node remained", err)
	}
	foreign, err := ready.BindPrivateSocket(ctx, "foreign-temp.sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := foreign.Close(); err != nil {
			t.Error(err)
		}
	})
	if state, err := foreign.Publish(ctx, "it1_"+strings.Repeat("a", 32)+".sock"); err != nil || state != securefs.Committed {
		t.Fatal(state, err)
	}
	if err := CleanupPrivateReady(ctx, ready, owners); !errors.Is(err, securefs.ErrChanged) {
		t.Fatal("old receipt deleted a new foreign socket", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("foreign node was not preserved", err)
	}
}
func TestReadyOwnerFreshLaunchAfterRealCrashUsesNewLedgerGeneration(t *testing.T) {
	l, ref, w, ready, owners, path := ownedReadyFixture(t)
	first := startFixtureCore(t, l, ref, w, strings.Repeat("d", 32))
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := first.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	// A fresh launch recovers the old exact object before replacing the one
	// owner slot. No copied old PID or metadata alone is used to signal it.
	nextWriter := &fixtureReadyWriter{ready: make(chan struct{})}
	l.output = nextWriter
	second := startFixtureCore(t, l, ref, nextWriter, strings.Repeat("c", 32))
	current, err := ReadReadySocketOwner(ctx, owners)
	if err != nil || current.Generation != 2 || current.Owner == nil || current.Owner.Process() != second.Identity() || current.Owner.Process() == first.Identity() {
		t.Fatal("new launch replayed stale owner identity", current, err)
	}
	if err := second.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("graceful close did not remove owned listener", err)
	}
	if err := CleanupPrivateReady(ctx, ready, owners); err != nil {
		t.Fatal("idempotent cleanup of ended owner failed", err)
	}
}
func TestReadyOwnerMalformedLedgerNeverCreatesOrRepairsEndpoint(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("isolated root fixture")
	}
	ready, owners := privateInstallerRoot(t), privateInstallerRoot(t)
	body := []byte(`{"schema":1,"generation":1,"owner":null,"exec":"/tmp/foreign"}`)
	if state, err := owners.ReplaceFile("socket-owner.json", body); err != nil || state != securefs.Committed {
		t.Fatal(state, err)
	}
	if socket, err := ListenPrivateReady(context.Background(), ready, owners, "it1_"+strings.Repeat("a", 32), strings.Repeat("b", 32)); !errors.Is(err, ErrRecoveryRequired) || socket != nil {
		t.Fatal("ambiguous owner ledger was auto-adopted", err)
	}
	after, err := owners.ReadFile("socket-owner.json", MaxMetadataBytes)
	if err != nil || string(after) != string(body) {
		t.Fatal("forensic owner ledger was repaired", err)
	}
	if err := CleanupPrivateReady(context.Background(), ready, owners); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("invalid owner was accepted for cleanup", err)
	}
}
