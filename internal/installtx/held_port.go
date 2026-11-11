package installtx

import (
	"context"
	"errors"
	"sync"

	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

type JournalReader interface {
	ReadJournal(context.Context) (Journal, error)
}
type BusinessGate interface {
	Fence(context.Context, string) error
	Admit(context.Context, string, string) error
}

// HeldStorePort retains the sole canonical writer from the latest checkpoint
// through migration. It releases that writer before the held candidate opens
// its own normal Store. The data/backup roots and journal are owned by the
// composition root; no paths or environment overrides are accepted here.
type HeldStorePort struct {
	mu           sync.Mutex
	data, backup *securefs.Root
	journal      JournalReader
	gate         BusinessGate
	held         *state.HeldRepository
	heldTx       string
}

func NewHeldStorePort(data, backup *securefs.Root, journal JournalReader, gate BusinessGate) (*HeldStorePort, error) {
	if data == nil || backup == nil || journal == nil || gate == nil {
		return nil, ErrProtocol
	}
	if _, err := data.Path(); err != nil {
		return nil, err
	}
	if _, err := backup.Path(); err != nil {
		return nil, err
	}
	return &HeldStorePort{data: data, backup: backup, journal: journal, gate: gate}, nil
}
func (p *HeldStorePort) read(ctx context.Context, txID string) (Journal, error) {
	j, err := p.journal.ReadJournal(ctx)
	if err != nil {
		return j, err
	}
	if j.Validate() != nil || j.Transaction == nil || j.Transaction.ID != txID {
		return j, ErrRecoveryRequired
	}
	return j, nil
}
func (p *HeldStorePort) precommit(ctx context.Context, txID string, phases ...string) (Journal, error) {
	j, err := p.read(ctx, txID)
	if err != nil || j.Transaction.OperationalCommitID != "" || !oneOf(j.Transaction.Phase, phases...) {
		return j, errors.Join(ErrRecoveryRequired, err)
	}
	return j, nil
}
func (p *HeldStorePort) acquire(ctx context.Context, txID string) error {
	if p.held != nil {
		if p.heldTx != txID {
			return ErrRecoveryRequired
		}
		return nil
	}
	h, err := state.OpenHeld(ctx, p.data, p.backup)
	if err != nil {
		return err
	}
	p.held = h
	p.heldTx = txID
	return nil
}
func (p *HeldStorePort) release() error {
	if p.held == nil {
		return nil
	}
	err := p.held.Close()
	if err == nil {
		p.held = nil
		p.heldTx = ""
	}
	return err
}
func (p *HeldStorePort) Close() error { p.mu.Lock(); defer p.mu.Unlock(); return p.release() }
func (p *HeldStorePort) Fence(ctx context.Context, txID string) error {
	if _, err := p.read(ctx, txID); err != nil {
		return err
	}
	return p.gate.Fence(ctx, txID)
}
func (p *HeldStorePort) Checkpoint(ctx context.Context, txID string) (CheckpointRef, error) {
	if _, err := p.precommit(ctx, txID, "fencing", "fenced"); err != nil {
		return CheckpointRef{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.acquire(ctx, txID); err != nil {
		return CheckpointRef{}, err
	}
	return p.held.Checkpoint(ctx, txID)
}
func (p *HeldStorePort) MigrateHeld(ctx context.Context, txID string) (SchemaReceipt, error) {
	j, err := p.precommit(ctx, txID, "fenced", "schema_ready", "ready")
	if err != nil || j.Transaction.Checkpoint == nil {
		return SchemaReceipt{}, errors.Join(ErrRecoveryRequired, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.acquire(ctx, txID); err != nil {
		return SchemaReceipt{}, err
	}
	receipt, migrateErr := p.held.Migrate(ctx, txID)
	closeErr := p.release()
	return receipt, errors.Join(migrateErr, closeErr)
}
func (p *HeldStorePort) RestoreHeld(ctx context.Context, txID string, ref CheckpointRef) error {
	j, err := p.precommit(ctx, txID, "rolling_back")
	if err != nil || j.Transaction.Checkpoint == nil || *j.Transaction.Checkpoint != ref {
		return errors.Join(ErrRecoveryRequired, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.acquire(ctx, txID); err != nil {
		return err
	}
	restoreErr := p.held.Restore(ctx, txID, ref)
	return errors.Join(restoreErr, p.release())
}
func (p *HeldStorePort) Inspect(ctx context.Context) (SchemaReceipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held != nil {
		return p.held.Inspect(ctx)
	}
	held, err := state.OpenHeld(ctx, p.data, p.backup)
	if err != nil {
		return SchemaReceipt{}, err
	}
	receipt, inspectErr := held.Inspect(ctx)
	return receipt, errors.Join(inspectErr, held.Close())
}
func (p *HeldStorePort) Admit(ctx context.Context, txID, ocID string) error {
	j, err := p.read(ctx, txID)
	if err != nil || !protocolID(ocID, "oc1_") || j.Transaction.OperationalCommitID != ocID ||
		!oneOf(j.Transaction.Phase, "committed_closed", "committed_open") || j.Transaction.Action == "remove" ||
		!sameRuntime(j.Active, j.Transaction.Candidate) {
		return errors.Join(ErrRecoveryRequired, err)
	}
	p.mu.Lock()
	busy := p.held != nil
	p.mu.Unlock()
	if busy {
		return ErrRecoveryRequired
	}
	return p.gate.Admit(ctx, txID, ocID)
}
