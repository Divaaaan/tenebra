//go:build linux

package installtx

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

var errFixtureJob = errors.New("synthetic fixed native job refusal")

// This native fixture owns only its copied Go TEST children; no real systemd,
// launchd or engine is executed. Its stop proof actually joins every retained
// child/host and checks the launcher's owned set; native OS adapters must prove
// the stronger real whole-job contract separately in ACCEPT1.
type fixtureHostJob struct {
	mu                   sync.Mutex
	host                 *ServiceHost
	orphan               *CoreProcess
	cancel               context.CancelFunc
	done                 chan struct{}
	refused, stopFailure bool
	starts, stops        int
}

func (j *fixtureHostJob) VerifyKnown(context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.refused {
		return errFixtureJob
	}
	return nil
}
func (j *fixtureHostJob) Start(ctx context.Context) error {
	if err := j.VerifyKnown(ctx); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.done != nil {
		select {
		case <-j.done:
		default:
			return ErrConflict
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owned, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	j.done = make(chan struct{})
	done := j.done
	j.starts++
	go func() { _ = j.host.Run(owned); close(done) }()
	return nil
}
func (j *fixtureHostJob) StopAndWait(ctx context.Context) error {
	if err := j.VerifyKnown(ctx); err != nil {
		return err
	}
	j.mu.Lock()
	if j.stopFailure {
		j.mu.Unlock()
		return errFixtureJob
	}
	j.stops++
	cancel, done, orphan := j.cancel, j.done, j.orphan
	j.mu.Unlock()
	if orphan != nil {
		if err := orphan.Stop(ctx); err != nil {
			return err
		}
	}
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
	}
	j.host.launcher.mu.Lock()
	active := len(j.host.launcher.active)
	starting := j.host.launcher.starting
	j.host.launcher.mu.Unlock()
	if active != 0 || starting != 0 {
		return ErrNotReady
	}
	return nil
}

func managerFixture(t *testing.T, committed bool) (*HostedServiceManager, *ServiceHost, *fixtureHostJob, RuntimeRef, Journal) {
	t.Helper()
	h, ref, j := nativeHostFixture(t, committed)
	job := &fixtureHostJob{host: h}
	m, err := NewHostedServiceManager(h.maintenance, job)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		job.mu.Lock()
		job.refused = false
		job.stopFailure = false
		job.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
		defer cancel()
		if err := job.StopAndWait(ctx); err != nil {
			t.Error(err)
		}
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m, h, job, ref, j
}

func TestMgrLifecycle(t *testing.T) {
	m, h, job, ref, j := managerFixture(t, false)
	ctx := context.Background()
	launch := strings.Repeat("c", 32)
	id, err := m.StartHeld(ctx, ref, j.Transaction.ID, launch)
	if err != nil {
		t.Fatal(err)
	}
	held := awaitHostProcess(t, h, "ready")
	if held.Current.Identity() != id || held.Current.Ready.BusinessAdmission != "closed" {
		t.Fatal("manager start was not exact held child", held)
	}
	j.Generation++
	j.Active, j.Pending = &ref, nil
	j.Transaction.Phase = "committed_closed"
	j.Transaction.OperationalCommitID = "oc1_" + strings.Repeat("2", 32)
	if err := h.journal.ReplaceJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	lock, err := h.process.root.LockWriter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	busy := m.Admit(short, j.Transaction.ID, j.Transaction.OperationalCommitID)
	cancel()
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(busy, context.DeadlineExceeded) {
		t.Fatalf("admission should wait within its bound for the ledger lock: %v", busy)
	}
	if got := mustHostLaunch(t, h); got.State != "reserved" || got.OperationalCommitID != "" {
		t.Fatal("locked ledger unexpectedly granted admission", got)
	}
	if err := m.Admit(ctx, j.Transaction.ID, j.Transaction.OperationalCommitID); err != nil {
		t.Fatal(err)
	}
	opened := awaitHostProcess(t, h, "admitted")
	if opened.Current.Identity() != id || opened.Current.Ready.StoreRevision != 49 {
		t.Fatal("manager admitted wrong child or revision", opened)
	}
	released := mustHostLaunch(t, h)
	if err := m.Admit(ctx, j.Transaction.ID, j.Transaction.OperationalCommitID); err != nil {
		t.Fatal("idempotent admission failed fresh peer check", err)
	}
	if got := mustHostLaunch(t, h); got.Generation != released.Generation {
		t.Fatal("admission replay minted another grant", got)
	}
	j.Generation++
	j.Transaction = &Transaction{ID: "it1_" + strings.Repeat("e", 32), Action: "remove", Phase: "fencing", PriorActive: &ref, Trust: ref.Provenance}
	if err := h.journal.ReplaceJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := m.Fence(ctx, j.Transaction.ID); err != nil {
		t.Fatal("actual stopped job did not fence", err)
	}
	if err := m.StopAndWait(ctx); err != nil {
		t.Fatal("verified stop was not idempotent", err)
	}
	if err := ProcessDefinitelyEnded(ctx, id); err != nil {
		t.Fatal("gate fence returned before actual child death", err)
	}
	if p, err := m.process.Read(ctx); err != nil || p.Current != nil || mustHostLaunch(t, h).State != "idle" {
		t.Fatal("stopped job metadata not retired", p, err)
	}
	job.mu.Lock()
	starts := job.starts
	job.mu.Unlock()
	if starts != 1 {
		t.Fatal("remove/replay started another service", starts)
	}
}

func TestMgrLostPID(t *testing.T) {
	m, h, job, ref, j := managerFixture(t, false)
	ctx := context.Background()
	launch := strings.Repeat("c", 32)
	if _, err := m.process.PrepareLaunch(ctx, m.journal, LaunchRequest{TxID: j.Transaction.ID, Runtime: ref, LaunchID: launch, Purpose: "transaction"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.process.ReserveLaunch(ctx, m.journal, j.Transaction.ID, launch); err != nil {
		t.Fatal(err)
	}
	child, err := h.launcher.StartHeld(ctx, ref, j.Transaction.ID, launch)
	if err != nil {
		t.Fatal(err)
	}
	job.orphan = child // The known native job owns it, even though current.json was lost.
	writer := h.launcher.output.(*fixtureReadyWriter)
	select {
	case <-writer.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("unrecorded actual child never published its owned endpoint")
	}
	owner, err := ReadReadySocketOwner(ctx, m.owners)
	if err != nil || owner.Owner == nil || owner.Owner.Process() != child.Identity() {
		t.Fatal("lost PID fixture did not own the real kernel lifetime", owner, err)
	}
	if p, err := m.process.Read(ctx); err != nil || p.Current != nil {
		t.Fatal("PID was accidentally recorded", p, err)
	}
	if err := m.StopAndWait(ctx); err != nil {
		t.Fatal("confirmed native job stop could not retire uncertain fork", err)
	}
	if err := ProcessDefinitelyEnded(ctx, child.Identity()); err != nil || mustHostLaunch(t, h).State != "idle" {
		t.Fatal("reservation cleared before actual orphan death", err)
	}
	id, err := m.StartHeld(ctx, ref, j.Transaction.ID, strings.Repeat("b", 32))
	if err != nil || id.LaunchID == child.Identity().LaunchID {
		t.Fatal("fresh explicit recovery reused stale launch", id, err)
	}
	awaitHostProcess(t, h, "ready")
	owner, err = ReadReadySocketOwner(ctx, m.owners)
	if err != nil || owner.Generation != 2 || owner.Owner.Process() != id {
		t.Fatal("new launch did not replace ended owner ledger", owner, err)
	}
}

func TestMgrFailedStop(t *testing.T) {
	m, h, job, ref, j := managerFixture(t, false)
	ctx := context.Background()
	launch := strings.Repeat("c", 32)
	if _, err := m.process.PrepareLaunch(ctx, m.journal, LaunchRequest{TxID: j.Transaction.ID, Runtime: ref, LaunchID: launch, Purpose: "transaction"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.process.ReserveLaunch(ctx, m.journal, j.Transaction.ID, launch); err != nil {
		t.Fatal(err)
	}
	before := mustHostLaunch(t, h)
	job.mu.Lock()
	job.stopFailure = true
	job.mu.Unlock()
	if err := m.StopAndWait(ctx); !errors.Is(err, errFixtureJob) {
		t.Fatal("native stop failure was hidden", err)
	}
	if after := mustHostLaunch(t, h); !sameHostMetadata(before, after) {
		t.Fatal("uncertain stop erased reserved launch", after)
	}
	if p, err := m.process.Read(ctx); err != nil || p.Current != nil {
		t.Fatal(p, err)
	}
}

func TestMgrCustomJob(t *testing.T) {
	m, h, job, ref, j := managerFixture(t, false)
	job.mu.Lock()
	job.refused = true
	job.mu.Unlock()
	if _, err := m.StartHeld(context.Background(), ref, j.Transaction.ID, strings.Repeat("c", 32)); !errors.Is(err, errFixtureJob) {
		t.Fatal("custom job accepted", err)
	}
	if launch := mustHostLaunch(t, h); launch.Generation != 0 || launch.State != "idle" {
		t.Fatal("custom job changed launch metadata", launch)
	}
	j.Generation++
	j.Active, j.Pending = &ref, nil
	j.Transaction = &Transaction{ID: "it1_" + strings.Repeat("e", 32), Action: "remove", Phase: "fencing", PriorActive: &ref, Trust: ref.Provenance}
	if err := h.journal.ReplaceJournal(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if err := m.Fence(context.Background(), j.Transaction.ID); !errors.Is(err, errFixtureJob) {
		t.Fatal("custom job was fenced", err)
	}
	job.mu.Lock()
	starts, stops := job.starts, job.stops
	job.mu.Unlock()
	if starts != 0 || stops != 0 {
		t.Fatal("unknown custom job was mutated", starts, stops)
	}
}

func TestMgrFalseReady(t *testing.T) {
	m, _, _, ref, j := managerFixture(t, true)
	token, err := ProcessStartToken(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	p := HostedProcess{TxID: j.Transaction.ID, Runtime: ref, PID: uint32(os.Getpid()), StartToken: token, LaunchID: strings.Repeat("c", 32)}
	p.Ready = &ReadyReceipt{Schema: 1, StoreSchema: 2, StoreRevision: 49, Status: "admitted", TxID: p.TxID, LaunchID: p.LaunchID, DaemonVersion: ref.Version,
		DaemonInstanceID: strings.Repeat("z", 32), CoreSHA256: strings.Repeat("a", 64), Transport: "unix", BusinessAdmission: "open"}
	before, err := m.process.Replace(context.Background(), 0, &p)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Admit(context.Background(), p.TxID, j.Transaction.OperationalCommitID); err == nil {
		t.Fatal("saved admitted metadata replaced actual kernel-private readiness")
	}
	after, err := m.process.Read(context.Background())
	if err != nil || !sameHostMetadata(before, after) {
		t.Fatal("failed peer check changed process metadata", after, err)
	}
}
