//go:build linux || darwin

package installtx

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// ServiceHost owns one directly spawned child for its entire lifetime. Its
// kernel owner lock is separate from journal, transaction and data writers.
// Neither a saved PID nor a published socket grants permission to adopt/kill.
type ServiceHost struct {
	mu                                sync.Mutex
	running, closing, closed          bool
	launcher                          *CoreLauncher
	journal                           *JournalFile
	process                           *ProcessRecordFile
	ready, owners, guard, maintenance *securefs.Root
	ownedMaintenance, ownedLauncher   bool
}

func OpenServiceHost(helperSHA string) (*ServiceHost, error) {
	if os.Geteuid() != 0 {
		return nil, ErrReadiness
	}
	paths, err := SystemPaths(runtime.GOOS)
	if err != nil {
		return nil, err
	}
	maintenance, err := securefs.OpenRoot(paths.Maintenance, securefs.Policy{Mode: securefs.MachinePrivate}, true)
	if err != nil {
		return nil, err
	}
	launcher, err := OpenCoreLauncher(helperSHA)
	if err != nil {
		maintenance.Close()
		return nil, err
	}
	host, err := newServiceHost(maintenance, launcher, securefs.Policy{Mode: securefs.MachinePrivate})
	if err != nil {
		launcher.Close()
		maintenance.Close()
		return nil, err
	}
	host.ownedMaintenance, host.ownedLauncher = true, true
	// Every production fork must belong to the exact native OS job whose stop
	// later proves an ambiguous reservation safe. A manual root host outside
	// that job must not create an orphan which a different job cannot stop.
	ownerCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := verifyServiceHostOwner(ownerCtx, host.journal); err != nil {
		return nil, errors.Join(err, host.Close())
	}
	return host, nil
}

func newServiceHost(maintenance *securefs.Root, launcher *CoreLauncher, policy securefs.Policy) (*ServiceHost, error) {
	if maintenance == nil || launcher == nil || !oneOfPolicy(policy.Mode) {
		return nil, ErrProtocol
	}
	h := &ServiceHost{launcher: launcher, maintenance: maintenance}
	var err error
	if policy.Mode == securefs.MachinePrivate {
		h.journal, err = OpenJournalFile(maintenance)
	} else {
		h.journal, err = openAccountJournalFile(maintenance)
	}
	if err == nil {
		h.process, err = openProcessRecordFile(maintenance, policy)
	}
	if err == nil {
		h.ready, err = maintenance.OpenDir("ready", policy, true)
	}
	if err == nil {
		h.owners, err = maintenance.OpenDir("socket-owner", policy, true)
	}
	if err == nil {
		h.guard, err = maintenance.OpenDir("host", policy, true)
	}
	if err != nil {
		return nil, errors.Join(err, h.closeResources())
	}
	return h, nil
}

func (h *ServiceHost) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return securefs.ErrBusy
	}
	if h.closed {
		return nil
	}
	h.closing = true
	err := h.closeResources()
	if err == nil {
		h.closed = true
	}
	return err
}
func (h *ServiceHost) closeResources() error {
	var err error
	if h.journal != nil {
		err = errors.Join(err, h.journal.Close())
	}
	if h.process != nil {
		err = errors.Join(err, h.process.Close())
	}
	for _, root := range []*securefs.Root{h.ready, h.owners, h.guard} {
		if root != nil {
			err = errors.Join(err, root.Close())
		}
	}
	if h.ownedLauncher {
		err = errors.Join(err, h.launcher.Close())
	}
	if h.ownedMaintenance {
		err = errors.Join(err, h.maintenance.Close())
	}
	return err
}

func (h *ServiceHost) Run(ctx context.Context) (result error) {
	if h == nil || h.guard == nil {
		return ErrProtocol
	}
	h.mu.Lock()
	if h.closed || h.closing || h.running {
		h.mu.Unlock()
		return securefs.ErrBusy
	}
	h.running = true
	h.mu.Unlock()
	defer func() { h.mu.Lock(); h.running = false; h.mu.Unlock() }()
	owner, err := h.guard.LockWriter(ctx)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, owner.Close()) }()
	s := hostSession{host: h}
	// Cleanup retains exclusive ownership, and continues independently of the
	// caller's canceled context. No metadata lock surrounds any process wait.
	defer func() {
		// A requested stop is a successful exit only if owned cleanup also
		// succeeds. Preserve all errors from the loop and cleanup itself.
		if result == context.Canceled && ctx.Err() == context.Canceled {
			result = nil
		}
		if s.child != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			result = errors.Join(result, s.stop(cleanup, Journal{}))
		}
	}()
	if err := s.retirePrior(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.step(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type hostSession struct {
	host          *ServiceHost
	child         *CoreProcess
	current       *HostedProcess
	operationalID string
}

func (s *hostSession) retirePrior(ctx context.Context) error {
	p, err := s.host.process.Read(ctx)
	if err != nil {
		return err
	}
	l, err := s.host.process.ReadLaunch(ctx)
	if err != nil {
		return err
	}
	if p.Current == nil {
		if oneOf(l.State, "reserved", "released") {
			return ErrRecoveryRequired
		}
		return nil
	}
	if err := ProcessDefinitelyEnded(ctx, p.Current.Identity()); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	if err := CleanupPrivateReady(ctx, s.host.ready, s.host.owners); err != nil {
		return err
	}
	return s.host.process.retireEnded(ctx, p, l)
}

func (s *hostSession) step(ctx context.Context) error {
	h := s.host
	if s.child != nil {
		select {
		case <-s.child.Done():
			return errors.Join(ErrRecoveryRequired, s.stop(ctx, Journal{}))
		default:
		}
	}
	j, err := h.journal.ReadJournal(ctx)
	if err != nil {
		return err
	}
	plan, err := PlanServiceHost(j, s.current)
	if err != nil {
		return err
	}
	switch plan.Action {
	case "inactive", "keep_current":
		return nil
	case "stop":
		return s.stop(ctx, j)
	case "start_held":
		launch, err := h.process.ReadLaunch(ctx)
		if err != nil {
			return err
		}
		if launch.State == "idle" {
			// Only a completed operational commit permits an ordinary boot.
			// Transactions must explicitly prepare their own exact launch ID.
			if plan.OperationalCommitID == "" {
				return nil
			}
			id, err := newID("")
			if err != nil {
				return err
			}
			launch, err = h.process.PrepareLaunch(ctx, h.journal, LaunchRequest{TxID: plan.TxID, Runtime: *plan.Runtime, LaunchID: id, Purpose: "boot"})
			if err != nil {
				return err
			}
		}
		if launch.State != "prepared" || launch.Request == nil || launch.Request.TxID != plan.TxID || launch.Request.Runtime != *plan.Runtime {
			return ErrRecoveryRequired
		}
		reserved, err := h.process.ReserveLaunch(ctx, h.journal, plan.TxID, launch.Request.LaunchID)
		if err != nil {
			return err
		}
		if err := launchAllowed(ctx, h.journal, *reserved.Request); err != nil {
			return err
		}
		child, err := h.launcher.StartHeld(ctx, *plan.Runtime, plan.TxID, reserved.Request.LaunchID)
		if err != nil {
			return err
		}
		s.child = child // Own it BEFORE any fallible metadata/readiness work.
		identity := child.Identity()
		s.current = &HostedProcess{TxID: plan.TxID, Runtime: *plan.Runtime, PID: identity.PID, StartToken: identity.StartToken, LaunchID: identity.LaunchID}
		before, err := h.process.Read(ctx)
		if err != nil {
			return err
		}
		if before.Current != nil {
			return ErrConflict
		}
		if _, err := h.process.Replace(ctx, before.Generation, s.current); err != nil {
			return err
		}
		probe, err := NewReadyProbe(h.ready, plan.TxID)
		if err != nil {
			return err
		}
		ready, err := VerifyReady(ctx, probe, ReadyExpectation{Schema: 1, StoreSchema: plan.SchemaReceipt.Schema, StoreRevision: plan.SchemaReceipt.Revision,
			TxID: plan.TxID, DaemonVersion: plan.Runtime.Version, LaunchID: identity.LaunchID, CoreSHA256: child.CoreSHA256(), Transport: "unix", BusinessAdmission: "closed", Process: identity})
		if err != nil {
			return err
		}
		return s.saveReady(ctx, ready)
	case "keep_held":
		if plan.OperationalCommitID == "" {
			return nil
		}
		launch, err := h.process.ReadLaunch(ctx)
		if err != nil {
			return err
		}
		if launch.Request == nil || launch.Request.TxID != s.current.TxID || launch.Request.LaunchID != s.current.LaunchID || launch.Request.Runtime != s.current.Runtime {
			return ErrRecoveryRequired
		}
		if launch.Request.Purpose == "transaction" {
			if launch.State == "reserved" {
				return nil
			}
			if launch.State != "released" || launch.OperationalCommitID != plan.OperationalCommitID {
				return ErrRecoveryRequired
			}
		} else if launch.State != "reserved" {
			return ErrRecoveryRequired
		}
		if s.current.Ready == nil {
			return ErrReadiness
		}
		probe, err := NewReadyProbe(h.ready, s.current.TxID)
		if err != nil {
			return err
		}
		admitted, err := AdmitPrivate(ctx, probe, s.current.Identity(), s.current.TxID, plan.OperationalCommitID, *s.current.Ready)
		if err != nil {
			return err
		}
		if err := s.saveReady(ctx, admitted); err != nil {
			return err
		}
		s.operationalID = plan.OperationalCommitID
		return nil
	case "keep_open":
		if s.operationalID == "" || s.operationalID != plan.OperationalCommitID {
			return ErrRecoveryRequired
		}
		return nil
	default:
		return ErrRecoveryRequired
	}
}

func (s *hostSession) saveReady(ctx context.Context, receipt ReadyReceipt) error {
	bound, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		before, err := s.host.process.Read(bound)
		if err != nil {
			return err
		}
		if !sameHostMetadata(before.Current, s.current) {
			return ErrConflict
		}
		next := *s.current
		next.Ready = &receipt
		_, err = s.host.process.Replace(bound, before.Generation, &next)
		if errors.Is(err, securefs.ErrBusy) {
			// The release is visible after rename. The installer may still
			// hold the ledger lock while it syncs that same release.
			if err := hostPoll(bound); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		s.current = &next
		return nil
	}
}

func (s *hostSession) stop(ctx context.Context, journal Journal) error {
	if s.child == nil {
		return nil
	}
	var fenceErr error
	if s.current != nil && s.current.Ready != nil && journal.Transaction != nil && journal.Transaction.Phase == "fencing" && journal.Transaction.ID != s.current.TxID {
		probe, err := NewReadyProbe(s.host.ready, s.current.TxID)
		if err == nil {
			_, err = FencePrivate(ctx, probe, s.current.Identity(), journal.Transaction.ID, *s.current.Ready)
		}
		fenceErr = err
	}
	if err := s.child.Stop(ctx); err != nil {
		return errors.Join(fenceErr, err)
	}
	if err := CleanupPrivateReady(ctx, s.host.ready, s.host.owners); err != nil {
		return errors.Join(fenceErr, err)
	}
	process, err := s.host.process.Read(ctx)
	if err != nil {
		return errors.Join(fenceErr, err)
	}
	launch, err := s.host.process.ReadLaunch(ctx)
	if err != nil {
		return errors.Join(fenceErr, err)
	}
	if process.Current == nil || s.current == nil || process.Current.Identity() != s.current.Identity() || process.Current.Runtime != s.current.Runtime || process.Current.TxID != s.current.TxID {
		return errors.Join(fenceErr, ErrRecoveryRequired)
	}
	if err := s.host.process.retireEnded(ctx, process, launch); err != nil {
		return errors.Join(fenceErr, err)
	}
	s.child, s.current, s.operationalID = nil, nil, ""
	return fenceErr
}
