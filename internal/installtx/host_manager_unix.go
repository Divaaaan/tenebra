//go:build linux || darwin

package installtx

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// ServiceHostJob is trusted native composition, never request data. Every method
// must verify the fixed known OS job/configuration and preserve unknown custom
// jobs. StopAndWait may return nil ONLY after its whole job/process group ended
// and automatic restart is suppressed until Start. A service PID/status alone
// does not satisfy this contract. Native acceptance tests remain a separate gate.
type ServiceHostJob interface {
	VerifyKnown(context.Context) error
	Start(context.Context) error
	StopAndWait(context.Context) error
}

type HostedServiceManager struct {
	job                  ServiceHostJob
	journal              *JournalFile
	process              *ProcessRecordFile
	ready, owners, guard *securefs.Root
}

func NewHostedServiceManager(maintenance *securefs.Root, job ServiceHostJob) (*HostedServiceManager, error) {
	if os.Geteuid() != 0 || maintenance == nil || job == nil {
		return nil, ErrProtocol
	}
	m := &HostedServiceManager{job: job}
	var err error
	m.journal, err = OpenJournalFile(maintenance)
	if err == nil {
		m.process, err = OpenProcessRecordFile(maintenance)
	}
	policy := securefs.Policy{Mode: securefs.MachinePrivate}
	if err == nil {
		m.ready, err = maintenance.OpenDir("ready", policy, true)
	}
	if err == nil {
		m.owners, err = maintenance.OpenDir("socket-owner", policy, true)
	}
	if err == nil {
		m.guard, err = maintenance.OpenDir("host", policy, true)
	}
	if err != nil {
		return nil, errors.Join(err, m.Close())
	}
	return m, nil
}

func (m *HostedServiceManager) Close() error {
	if m == nil {
		return nil
	}
	var err error
	if m.journal != nil {
		err = errors.Join(err, m.journal.Close())
	}
	if m.process != nil {
		err = errors.Join(err, m.process.Close())
	}
	for _, root := range []*securefs.Root{m.ready, m.owners, m.guard} {
		if root != nil {
			err = errors.Join(err, root.Close())
		}
	}
	return err
}

// Fence closes business admission by completing the actual fixed-job stop.
// This also covers recovery of the SAME postcommit transaction, for which a
// new-transaction private FENCE would correctly be refused. Installer's following
// StopAndWait remains idempotent; neither path acquires canonical data early.
func (m *HostedServiceManager) Fence(ctx context.Context, txID string) error {
	if m == nil || m.journal == nil || !protocolID(txID, "it1_") {
		return ErrProtocol
	}
	j, err := m.journal.ReadJournal(ctx)
	if err != nil {
		return err
	}
	if !hostStopPhase(j) || j.Transaction.ID != txID {
		return ErrRecoveryRequired
	}
	if err := m.job.VerifyKnown(ctx); err != nil {
		return err
	}
	p, err := m.process.Read(ctx)
	if err != nil {
		return err
	}
	var fenceErr error
	if p.Current != nil && p.Current.Ready != nil && p.Current.TxID != txID && j.Transaction.Phase == "fencing" {
		probe, err := NewReadyProbe(m.ready, p.Current.TxID)
		if err == nil {
			_, err = FencePrivate(ctx, probe, p.Current.Identity(), txID, *p.Current.Ready)
		}
		fenceErr = err
	}
	stopErr := m.StopAndWait(ctx)
	if stopErr == nil {
		return nil
	} // Confirmed whole-job death supersedes a lost private FENCE ACK.
	return errors.Join(fenceErr, stopErr)
}

func (m *HostedServiceManager) StopAndWait(ctx context.Context) error {
	if m == nil || m.job == nil {
		return ErrProtocol
	}
	j, err := m.journal.ReadJournal(ctx)
	if err != nil {
		return err
	}
	if !hostStopPhase(j) {
		return ErrRecoveryRequired
	}
	// Native config verification, job stop and joins are OUTSIDE all metadata
	// locks. A failed/uncertain native stop must leave every reservation intact.
	if err := m.job.StopAndWait(ctx); err != nil {
		return err
	}
	if err := CleanupPrivateReady(ctx, m.ready, m.owners); err != nil {
		return err
	}
	owner, err := m.guard.LockWriter(ctx)
	if err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	defer owner.Close()
	p, err := m.process.Read(ctx)
	if err != nil {
		return err
	}
	l, err := m.process.ReadLaunch(ctx)
	if err != nil {
		return err
	}
	if p.Current != nil {
		return m.process.retireEnded(ctx, p, l)
	}
	return m.process.resetLaunchAfterStopped(ctx, m.journal, l)
}

func (m *HostedServiceManager) StartHeld(ctx context.Context, ref RuntimeRef, txID, launchID string) (ProcessIdentity, error) {
	if m == nil || m.job == nil || !validRuntime(&ref) || !protocolID(txID, "it1_") || !hexValue(launchID, 32) {
		return ProcessIdentity{}, ErrProtocol
	}
	if err := m.job.VerifyKnown(ctx); err != nil {
		return ProcessIdentity{}, err
	}
	owner, err := m.guard.LockWriter(ctx)
	if err != nil {
		return ProcessIdentity{}, err
	}
	_, prepareErr := m.process.PrepareLaunch(ctx, m.journal, LaunchRequest{TxID: txID, Runtime: ref, LaunchID: launchID, Purpose: "transaction"})
	closeErr := owner.Close()
	if err := errors.Join(prepareErr, closeErr); err != nil {
		return ProcessIdentity{}, err
	}
	// Release the host owner BEFORE asking the native manager to start it.
	// OS start success is NOT readiness; only the private challenge can prove it.
	if err := m.job.Start(ctx); err != nil {
		return ProcessIdentity{}, err
	}
	bound, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		current, err := m.current(bound, txID)
		if err == nil {
			if current.Runtime != ref || current.LaunchID != launchID {
				return ProcessIdentity{}, ErrRecoveryRequired
			}
			token, err := ProcessStartToken(current.PID)
			if err == nil && token == current.StartToken {
				return current.Identity(), nil
			}
			if err == nil {
				return ProcessIdentity{}, ErrReadiness
			}
		} else if !errors.Is(err, ErrNotReady) {
			return ProcessIdentity{}, err
		}
		if err := hostPoll(bound); err != nil {
			return ProcessIdentity{}, err
		}
	}
}

func (m *HostedServiceManager) current(ctx context.Context, txID string) (*HostedProcess, error) {
	p, err := m.process.Read(ctx)
	if err != nil {
		return nil, err
	}
	if p.Current == nil {
		return nil, ErrNotReady
	}
	if p.Current.TxID != txID {
		return nil, ErrRecoveryRequired
	}
	return p.Current, nil
}

func hostPoll(ctx context.Context) error {
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m *HostedServiceManager) Admit(ctx context.Context, txID, operationalID string) error {
	if m == nil || !protocolID(txID, "it1_") || !protocolID(operationalID, "oc1_") {
		return ErrProtocol
	}
	bound, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var releasedLaunchID string
	for {
		j, err := m.journal.ReadJournal(bound)
		if err != nil {
			return err
		}
		plan, err := PlanServiceHost(j, nil)
		if err != nil || plan.Action != "start_held" || plan.Runtime == nil || plan.SchemaReceipt == nil || plan.TxID != txID || plan.OperationalCommitID != operationalID {
			return errors.Join(ErrRecoveryRequired, err)
		}
		current, err := m.current(bound, txID)
		if err != nil && !errors.Is(err, ErrNotReady) {
			return err
		}
		if current != nil && current.Ready != nil {
			if current.Runtime != *plan.Runtime || current.Ready.StoreSchema != plan.SchemaReceipt.Schema || current.Ready.StoreRevision < plan.SchemaReceipt.Revision {
				return ErrReadiness
			}
			if current.Ready.BusinessAdmission == "closed" {
				if current.LaunchID != releasedLaunchID {
					_, err := m.process.ReleaseLaunch(bound, m.journal, txID, current.LaunchID, operationalID)
					if err == nil {
						// Wait for the host's admission receipt without taking the
						// same process ledger lock it needs to publish the receipt.
						releasedLaunchID = current.LaunchID
					} else if !errors.Is(err, securefs.ErrBusy) {
						return err
					}
				}
			} else {
				if err := m.observeAdmission(bound, *current); err != nil {
					return err
				}
				return nil
			}
		}
		if err := hostPoll(bound); err != nil {
			return err
		}
	}
}

// Fresh kernel-authenticated read-only challenge observes admission after the
// host's durable release. A saved "admitted" process record alone is insufficient.
func (m *HostedServiceManager) observeAdmission(ctx context.Context, current HostedProcess) error {
	if current.Ready == nil || current.Ready.Status != "admitted" {
		return ErrReadiness
	}
	probe, err := NewReadyProbe(m.ready, current.TxID)
	if err != nil {
		return err
	}
	challenge, err := newID("")
	if err != nil {
		return err
	}
	one, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r, err := probe.Exchange(one, current.Identity(), ReadyRequest{Schema: 1, Cmd: "ready", TxID: current.TxID, Challenge: challenge})
	if err != nil {
		return err
	}
	e := current.Ready
	if r.Schema != e.Schema || r.Status != "ready" || r.TxID != e.TxID || r.Challenge != challenge || r.LaunchID != e.LaunchID ||
		r.DaemonVersion != e.DaemonVersion || r.DaemonInstanceID != e.DaemonInstanceID || r.CoreSHA256 != e.CoreSHA256 || r.Transport != e.Transport ||
		r.StoreSchema != e.StoreSchema || r.StoreRevision < e.StoreRevision || r.BusinessAdmission != "open" {
		return ErrReadiness
	}
	return nil
}
