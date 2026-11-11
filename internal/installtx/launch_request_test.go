package installtx

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func launchFixture(t *testing.T) (*ProcessRecordFile, *installDisk, LaunchRequest) {
	t.Helper()
	disk := installerDisk()
	return testProcessRecord(t), disk, LaunchRequest{TxID: disk.journal.Transaction.ID, Runtime: *disk.journal.Active, LaunchID: strings.Repeat("c", 32), Purpose: "transaction"}
}

func TestLaunchTransactionReleaseWaitsForReadyAndExactDurableCommit(t *testing.T) {
	f, disk, request := launchFixture(t)
	ctx := context.Background()
	reader := fakeInstallFS{disk}
	if _, err := f.PrepareLaunch(ctx, reader, request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReserveLaunch(ctx, reader, request.TxID, request.LaunchID); err != nil {
		t.Fatal(err)
	}
	oc := disk.journal.Transaction.OperationalCommitID
	if _, err := f.ReleaseLaunch(ctx, reader, request.TxID, request.LaunchID, oc); !errors.Is(err, ErrReadiness) {
		t.Fatal("candidate admitted before observed readiness", err)
	}
	process := hostedFixture(disk.journal)
	process.Ready.StoreRevision = disk.journal.Transaction.SchemaReceipt.Revision
	process.Ready.Status = "ready"
	process.Ready.BusinessAdmission = "closed"
	process.Ready.Challenge = strings.Repeat("d", 32)
	if _, err := f.Replace(ctx, 0, &process); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReleaseLaunch(ctx, reader, request.TxID, request.LaunchID, "oc1_"+strings.Repeat("f", 32)); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("wrong OC released transaction candidate", err)
	}
	released, err := f.ReleaseLaunch(ctx, reader, request.TxID, request.LaunchID, oc)
	if err != nil || released.State != "released" || released.OperationalCommitID != oc || released.Generation != 3 {
		t.Fatal(released, err)
	}
	if replay, err := f.ReleaseLaunch(ctx, reader, request.TxID, request.LaunchID, oc); err != nil || replay.Generation != released.Generation {
		t.Fatal("release replay minted new authority", replay, err)
	}
	disk.journal.Transaction.Phase = "fencing"
	disk.journal.Transaction.OperationalCommitID = ""
	disk.journal.Transaction.PriorActive = disk.journal.Active
	if _, err := f.ReleaseLaunch(ctx, reader, request.TxID, request.LaunchID, oc); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("old released launch crossed a new fence", err)
	}
}
func TestLaunchReservationIsDurableBeforeForkAndNeverReplayedAfterCrash(t *testing.T) {
	f, disk, request := launchFixture(t)
	ctx := context.Background()
	reader := fakeInstallFS{disk}
	prepared, err := f.PrepareLaunch(ctx, reader, request)
	if err != nil || prepared.State != "prepared" || prepared.Generation != 1 {
		t.Fatal(prepared, err)
	}
	if replay, err := f.PrepareLaunch(ctx, reader, request); err != nil || replay.Generation != prepared.Generation {
		t.Fatal("idempotent prepare minted a new launch", replay, err)
	}
	prepared.Request.LaunchID = strings.Repeat("a", 32)
	reserved, err := f.ReserveLaunch(ctx, reader, request.TxID, request.LaunchID)
	if err != nil || reserved.State != "reserved" || reserved.Generation != 2 || *reserved.Request != request {
		t.Fatal(reserved, err)
	}
	// Reconstruct the metadata adapter with no in-memory reservation state.
	reopened := &ProcessRecordFile{root: f.root}
	if _, err := reopened.ReserveLaunch(ctx, reader, request.TxID, request.LaunchID); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("lost fork ACK allowed a second process", err)
	}
	request.LaunchID = strings.Repeat("d", 32)
	if _, err := reopened.PrepareLaunch(ctx, reader, request); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("fresh launch silently cleared uncertain old fork", err)
	}
	still, err := reopened.ReadLaunch(ctx)
	if err != nil || still.State != "reserved" || still.Generation != 2 {
		t.Fatal("reservation erased after crash", still, err)
	}
}
func TestLaunchReservationCannotCrossJournalFenceOrRuntimeBinding(t *testing.T) {
	f, disk, request := launchFixture(t)
	ctx := context.Background()
	reader := fakeInstallFS{disk}
	wrong := request
	wrong.Runtime.ArtifactSHA256 = strings.Repeat("f", 64)
	if _, err := f.PrepareLaunch(ctx, reader, wrong); err == nil {
		t.Fatal("unbound runtime authorized")
	}
	if _, err := f.PrepareLaunch(ctx, reader, request); err != nil {
		t.Fatal(err)
	}
	disk.journal.Generation++
	disk.journal.Transaction.OperationalCommitID = ""
	disk.journal.Transaction.Phase = "fencing"
	disk.journal.Transaction.PriorActive = disk.journal.Active
	if _, err := f.ReserveLaunch(ctx, reader, request.TxID, request.LaunchID); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("new fence did not close spawn admission", err)
	}
	after, err := f.ReadLaunch(ctx)
	if err != nil || after.State != "prepared" || after.Generation != 1 {
		t.Fatal("refused spawn changed request", after, err)
	}
}
func TestLaunchConcurrentReservationsHaveExactlyOneWinner(t *testing.T) {
	f, disk, request := launchFixture(t)
	ctx := context.Background()
	reader := fakeInstallFS{disk}
	if _, err := f.PrepareLaunch(ctx, reader, request); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.ReserveLaunch(ctx, reader, request.TxID, request.LaunchID)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, securefs.ErrBusy) && !errors.Is(err, ErrRecoveryRequired) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("two native forks won the same durable launch", wins)
	}
}
func TestLaunchPreparePreservesActiveOrMalformedOwnerMetadata(t *testing.T) {
	f, disk, request := launchFixture(t)
	ctx := context.Background()
	reader := fakeInstallFS{disk}
	process := hostedFixture(disk.journal)
	if _, err := f.Replace(ctx, 0, &process); err != nil {
		t.Fatal(err)
	}
	if _, err := f.PrepareLaunch(ctx, reader, request); !errors.Is(err, ErrConflict) {
		t.Fatal("active process was superseded by a metadata request", err)
	}
	if _, err := f.Replace(ctx, 1, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(LaunchRecord{Schema: 1, Generation: 1, State: "prepared", Request: &request})
	bad := strings.Replace(string(data), `"schema":1`, `"schema":1,"command":"/tmp/foreign"`, 1)
	if state, err := f.root.ReplaceFile("launch.json", []byte(bad)); err != nil || state != securefs.Committed {
		t.Fatal(state, err)
	}
	if _, err := f.ReadLaunch(ctx); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("unknown launch command accepted", err)
	}
	if _, err := f.PrepareLaunch(ctx, reader, request); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("malformed launch was auto-healed", err)
	}
	after, err := f.root.ReadFile("launch.json", MaxMetadataBytes)
	if err != nil || string(after) != bad {
		t.Fatal("forensic launch record changed", err)
	}
}
