package installtx

import (
	"context"
	"errors"
	"time"
)

func (i *Installer) Recover(ctx context.Context) (Result, error) {
	return i.recover(ctx, "")
}

// RecoverTransaction binds the root CLI's transaction argument under the SAME
// transaction lock used by recovery. An earlier unlocked status check is not a
// substitute: it could recover a different transaction after a concurrent apply.
func (i *Installer) RecoverTransaction(ctx context.Context, txID string) (Result, error) {
	if !protocolID(txID, "it1_") {
		return Result{}, ErrProtocol
	}
	return i.recover(ctx, txID)
}

func (i *Installer) recover(ctx context.Context, expectedTx string) (Result, error) {
	lock, err := i.lock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer lock.Close()
	j, err := i.fs.ReadJournal(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := j.Validate(); err != nil {
		return Result{}, err
	}
	tx := j.Transaction
	if expectedTx != "" && (tx == nil || tx.ID != expectedTx) {
		return resultFor(j, false, ErrConflict)
	}
	if tx == nil || oneOf(tx.Phase, "pending", "staged", "rolled_back") {
		return resultFor(j, false, nil)
	}
	if tx.Phase == "removed" {
		err := i.fenceAndStop(ctx, tx.ID)
		return resultFor(j, false, err)
	}
	if tx.OperationalCommitID != "" {
		if tx.Action == "remove" {
			if err := i.fenceAndStop(ctx, tx.ID); err != nil {
				return i.block(ctx, j, "remove_stop_failed", err)
			}
			j.Active = nil
			j.Pending = nil
			err := i.write(ctx, &j, "removed")
			return resultFor(j, true, err)
		}
		return i.recoverCommitted(ctx, j)
	}
	if tx.Phase == "blocked" {
		return resultFor(j, false, ErrRecoveryRequired)
	}
	return i.rollback(ctx, j, nil)
}
func (i *Installer) recoverCommitted(ctx context.Context, j Journal) (Result, error) {
	// Close the durable restart gate before stopping even a formerly admitted
	// candidate. The operational ID and active pointer never move backwards.
	if err := i.write(ctx, &j, "committed_closed"); err != nil {
		return resultFor(j, true, err)
	}
	tx := j.Transaction
	if err := i.fenceAndStop(ctx, tx.ID); err != nil {
		return i.block(ctx, j, "postcommit_stop_failed", err)
	}
	observed, err := i.store.Inspect(ctx)
	if err != nil || tx.SchemaReceipt == nil || !validPostCommitLineage(observed, *tx.SchemaReceipt) {
		return i.block(ctx, j, "postcommit_lineage_unknown", errors.Join(ErrRecoveryRequired, err))
	}
	if err := i.startAndVerify(ctx, j, observed); err != nil {
		return i.block(ctx, j, "postcommit_ready_failed", err)
	}
	return i.admit(ctx, j)
}
func validPostCommitLineage(current, committed SchemaReceipt) bool {
	return current.Schema == committed.Schema && current.MigrationID == committed.MigrationID && current.Revision >= committed.Revision &&
		hexValue(current.CanonicalSHA256, 64) && (current.Revision != committed.Revision || current.CanonicalSHA256 == committed.CanonicalSHA256)
}
func (i *Installer) rollback(parent context.Context, j Journal, cause error) (Result, error) {
	if j.Transaction == nil || j.Transaction.OperationalCommitID != "" {
		return resultFor(j, false, ErrRecoveryRequired)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
	defer cancel()
	tx := j.Transaction
	if err := i.fenceAndStop(ctx, tx.ID); err != nil {
		return i.block(ctx, j, "rollback_stop_failed", errors.Join(cause, err))
	}
	if tx.Checkpoint != nil && tx.Phase != "rolling_back" {
		// Resolve an interrupted receipt from the held repository's durable
		// migration intent. JSON shape alone cannot prove a schema outcome.
		expected := tx.SchemaReceipt
		if expected == nil {
			receipt, err := i.store.MigrateHeld(ctx, tx.ID)
			if err != nil {
				return i.block(ctx, j, "schema_outcome_unknown", errors.Join(cause, err))
			}
			expected = &receipt
			tx.SchemaReceipt = expected
		}
		observed, err := i.store.Inspect(ctx)
		if err != nil || observed != *expected {
			return i.block(ctx, j, "schema_outcome_unknown", errors.Join(cause, err))
		}
	}
	if err := i.write(ctx, &j, "rolling_back"); err != nil {
		return resultFor(j, true, err)
	}
	if tx.Checkpoint != nil {
		if err := i.store.RestoreHeld(ctx, tx.ID, *tx.Checkpoint); err != nil {
			return i.block(ctx, j, "restore_uncertain", errors.Join(cause, err))
		}
	}
	j.Active = tx.PriorActive
	j.Pending = nil
	if cause != nil {
		tx.ErrorCode = "activation_failed"
	}
	err := i.write(ctx, &j, "rolled_back")
	// A whole restore does not invent an operational admission token. The old
	// payload remains closed until an explicit subsequent activation.
	return resultFor(j, true, errors.Join(cause, err))
}
