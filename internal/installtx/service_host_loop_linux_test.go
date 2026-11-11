//go:build linux

package installtx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func nativeHostFixture(t *testing.T, committed bool) (*ServiceHost, RuntimeRef, Journal) {
	t.Helper()
	var maintenance *securefs.Root
	// The staging fixture owns other handles for this SAME maintenance object.
	// Its children must close before this final independent root binding.
	t.Cleanup(func() {
		if maintenance != nil {
			if err := maintenance.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	launcher, ref, _ := coreProcessFixture(t)
	releases, err := launcher.releases.Path()
	if err != nil {
		t.Fatal(err)
	}
	maintenance, err = securefs.OpenRoot(filepath.Join(filepath.Dir(releases), "maintenance"), securefs.Policy{Mode: securefs.MachinePrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	h, err := newServiceHost(maintenance, launcher, securefs.Policy{Mode: securefs.MachinePrivate})
	if err != nil {
		maintenance.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Error(err)
		}
	})
	j := installerDisk().journal
	j.Active = &ref
	j.Transaction.Candidate = &ref
	j.Transaction.Trust = ref.Provenance
	if !committed {
		j.Active, j.Pending = nil, &ref
		j.Transaction.Phase = "schema_ready"
		j.Transaction.OperationalCommitID = ""
	}
	if err := h.journal.ReplaceJournal(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	return h, ref, j
}

func awaitHostProcess(t *testing.T, h *ServiceHost, status string) ProcessRecord {
	t.Helper()
	// The production readiness exchange may use 15 seconds after identified
	// launch; an emulated race fixture must not cancel it at eight seconds.
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		p, err := h.process.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status == "absent" && p.Current == nil || p.Current != nil && p.Current.Ready != nil && p.Current.Ready.Status == status {
			return p
		}
		select {
		case <-deadline.C:
			t.Fatal("host did not reach requested actual process state", status, p)
		case <-tick.C:
		}
	}
}

func runHostFixture(t *testing.T, h *ServiceHost) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error("native host returned", err)
			}
		case <-time.After(22 * time.Second):
			t.Error("host cleanup did not confirm child termination")
		}
	})
	return cancel, done
}

// Bounded private fixture directories fit sockaddr_un like the production
// namespace; all actual process, kernel peer and readiness checks remain enabled.
func TestHostTxFence(t *testing.T) {
	h, ref, j := nativeHostFixture(t, false)
	ctx := context.Background()
	launch := strings.Repeat("c", 32)
	if _, err := h.process.PrepareLaunch(ctx, h.journal, LaunchRequest{TxID: j.Transaction.ID, Runtime: ref, LaunchID: launch, Purpose: "transaction"}); err != nil {
		t.Fatal(err)
	}
	// The installer can own its transaction lock while the independent host
	// starts, reads the journal, verifies ready and waits for its release.
	transaction, err := h.journal.LockTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Close()
	runHostFixture(t, h)
	closed := awaitHostProcess(t, h, "ready")
	if closed.Current.Ready.BusinessAdmission != "closed" || closed.Current.Ready.StoreRevision != 40 {
		t.Fatal("candidate did not start held", closed)
	}
	oldIdentity := closed.Current.Identity()
	if err := h.process.retireEnded(ctx, closed, mustHostLaunch(t, h)); !errors.Is(err, ErrNotReady) {
		t.Fatal("live process metadata retired", err)
	}
	j.Generation++
	j.Active, j.Pending = &ref, nil
	j.Transaction.Phase = "committed_closed"
	j.Transaction.OperationalCommitID = "oc1_" + strings.Repeat("2", 32)
	if err := h.journal.ReplaceJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	// Several real host polls must NOT auto-admit a transaction merely
	// because a durable OC appeared. Probe the actual identified child again.
	timer := time.NewTimer(350 * time.Millisecond)
	<-timer.C
	probe, err := NewReadyProbe(h.ready, j.Transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := closed.Current.Ready
	actual, err := VerifyReady(ctx, probe, ReadyExpectation{Schema: 1, StoreSchema: r.StoreSchema, StoreRevision: r.StoreRevision,
		TxID: r.TxID, DaemonVersion: r.DaemonVersion, DaemonInstanceID: r.DaemonInstanceID, LaunchID: launch, CoreSHA256: r.CoreSHA256,
		Transport: "unix", BusinessAdmission: "closed", Process: oldIdentity})
	if err != nil || actual.BusinessAdmission != "closed" {
		t.Fatal("transaction opened before release", actual, err)
	}
	if _, err := h.process.ReleaseLaunch(ctx, h.journal, j.Transaction.ID, launch, "oc1_"+strings.Repeat("f", 32)); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("wrong operational grant released", err)
	}
	if _, err := h.process.ReleaseLaunch(ctx, h.journal, j.Transaction.ID, launch, j.Transaction.OperationalCommitID); err != nil {
		t.Fatal(err)
	}
	admitted := awaitHostProcess(t, h, "admitted")
	if admitted.Current.Identity() != oldIdentity || admitted.Current.Ready.StoreRevision != 49 {
		t.Fatal("admission replaced child or lost minimum revision", admitted)
	}
	j.Generation++
	j.Transaction = &Transaction{ID: "it1_" + strings.Repeat("e", 32), Action: "remove", Phase: "fencing", PriorActive: &ref, Trust: ref.Provenance}
	if err := h.journal.ReplaceJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	awaitHostProcess(t, h, "absent")
	if err := ProcessDefinitelyEnded(ctx, oldIdentity); err != nil {
		t.Fatal("stop ACK preceded actual death", err)
	}
	if launch := mustHostLaunch(t, h); launch.State != "idle" {
		t.Fatal("ended launch was not retired", launch)
	}
	if err := CleanupPrivateReady(ctx, h.ready, h.owners); err != nil {
		t.Fatal("owned endpoint cleanup incomplete", err)
	}
}

func mustHostLaunch(t *testing.T, h *ServiceHost) LaunchRecord {
	t.Helper()
	l, err := h.process.ReadLaunch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestHostBoot(t *testing.T) {
	h, _, j := nativeHostFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	// Register before assertions so a failed native check still stops its child.
	finished := false
	t.Cleanup(func() {
		cancel()
		if !finished {
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				t.Error("native host returned", err)
			}
		}
	})
	first := awaitHostProcess(t, h, "admitted")
	if first.Current.Ready.StoreRevision < j.Transaction.SchemaReceipt.Revision || mustHostLaunch(t, h).Request.Purpose != "boot" {
		t.Fatal("ordinary boot did not replay committed authority", first)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal("requested stop with successful cleanup should exit cleanly", err)
	}
	finished = true
	if err := ProcessDefinitelyEnded(context.Background(), first.Current.Identity()); err != nil {
		t.Fatal(err)
	}
	if p, err := h.process.Read(context.Background()); err != nil || p.Current != nil {
		t.Fatal(p, err)
	}
	runHostFixture(t, h)
	second := awaitHostProcess(t, h, "admitted")
	if second.Current.LaunchID == first.Current.LaunchID || second.Current.Ready.DaemonInstanceID == first.Current.Ready.DaemonInstanceID {
		t.Fatal("clean boot reused process authority")
	}
	observed, err := h.journal.ReadJournal(context.Background())
	if err != nil || !sameHostMetadata(observed, j) {
		t.Fatal("host changed durable operational commit or migration receipt", observed, err)
	}
	owner, err := ReadReadySocketOwner(context.Background(), h.owners)
	if err != nil || owner.Generation != 2 || owner.Owner.Process() != second.Current.Identity() {
		t.Fatal("boot did not persist actual new owner", owner, err)
	}
}

func TestNativeServiceHostRefusesLiveMetadataWithoutAdoptingOrSignalling(t *testing.T) {
	h, ref, j := nativeHostFixture(t, true)
	token, err := ProcessStartToken(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	p := &HostedProcess{TxID: j.Transaction.ID, Runtime: ref, PID: uint32(os.Getpid()), StartToken: token, LaunchID: strings.Repeat("c", 32)}
	before, err := h.process.Replace(context.Background(), 0, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Run(context.Background()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("unowned live process adopted", err)
	}
	after, err := h.process.Read(context.Background())
	if err != nil || !sameHostMetadata(before, after) {
		t.Fatal("unknown live metadata changed", after, err)
	}
	if len(h.launcher.active) != 0 || mustHostLaunch(t, h).State != "idle" {
		t.Fatal("a second child was started")
	}
}

func TestNativeServiceHostPreservesAmbiguousReservedLaunchWithoutPID(t *testing.T) {
	h, ref, j := nativeHostFixture(t, true)
	ctx := context.Background()
	request := LaunchRequest{TxID: j.Transaction.ID, Runtime: ref, LaunchID: strings.Repeat("c", 32), Purpose: "transaction"}
	if _, err := h.process.PrepareLaunch(ctx, h.journal, request); err != nil {
		t.Fatal(err)
	}
	before, err := h.process.ReserveLaunch(ctx, h.journal, request.TxID, request.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Run(ctx); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("ambiguous fork was replayed", err)
	}
	if after := mustHostLaunch(t, h); !sameHostMetadata(before, after) || len(h.launcher.active) != 0 {
		t.Fatal("unresolved reservation was erased or executed", after)
	}
}

func TestNativeServiceHostOwnerLockExcludesSecondHostWithoutBlockingJournal(t *testing.T) {
	h, _, _ := nativeHostFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	var other *ServiceHost
	t.Cleanup(func() {
		cancel()
		<-done
		if other != nil {
			if err := other.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	// Wait for the actual independent host owner lock, without a child request.
	deadline := time.Now().Add(3 * time.Second)
	for {
		attempt, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
		lock, err := h.guard.LockWriter(attempt)
		stop()
		if errors.Is(err, securefs.ErrBusy) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		lock.Close()
		if time.Now().After(deadline) {
			t.Fatal("first host never owned native lock")
		}
	}
	other, err := newServiceHost(h.maintenance, h.launcher, securefs.Policy{Mode: securefs.MachinePrivate})
	if err != nil {
		t.Fatal(err)
	}
	attempt, stop := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer stop()
	if err := other.Run(attempt); !errors.Is(err, securefs.ErrBusy) {
		t.Fatal("two native hosts ran concurrently", err)
	}
	if _, err := h.journal.ReadJournal(context.Background()); err != nil {
		t.Fatal("host lock blocked status journal", err)
	}
	if len(h.launcher.active) != 0 {
		t.Fatal("host without prepared transaction invented a launch")
	}
}

func TestNativeHostRetirementRejectsStaleCASAfterConfirmedChildDeath(t *testing.T) {
	h, ref, j := nativeHostFixture(t, true)
	ctx := context.Background()
	launch := strings.Repeat("c", 32)
	if _, err := h.process.PrepareLaunch(ctx, h.journal, LaunchRequest{TxID: j.Transaction.ID, Runtime: ref, LaunchID: launch, Purpose: "transaction"}); err != nil {
		t.Fatal(err)
	}
	reserved, err := h.process.ReserveLaunch(ctx, h.journal, j.Transaction.ID, launch)
	if err != nil {
		t.Fatal(err)
	}
	child, err := h.launcher.StartHeld(ctx, ref, j.Transaction.ID, launch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Stop(context.Background()) })
	id := child.Identity()
	current := &HostedProcess{TxID: j.Transaction.ID, Runtime: ref, PID: id.PID, StartToken: id.StartToken, LaunchID: id.LaunchID}
	before, err := h.process.Replace(ctx, 0, current)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := CleanupPrivateReady(ctx, h.ready, h.owners); err != nil {
		t.Fatal(err)
	}
	fresh, err := h.process.Replace(ctx, before.Generation, current)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.process.retireEnded(ctx, before, reserved); !errors.Is(err, ErrConflict) {
		t.Fatal("stale retirement overwrote newer metadata", err)
	}
	if after := mustHostLaunch(t, h); !sameHostMetadata(after, reserved) {
		t.Fatal("stale CAS erased launch", after)
	}
	if err := h.process.retireEnded(ctx, fresh, reserved); err != nil {
		t.Fatal(err)
	}
	if p, err := h.process.Read(ctx); err != nil || p.Current != nil || mustHostLaunch(t, h).State != "idle" {
		t.Fatal("ended exact child was not retired", p, err)
	}
}
