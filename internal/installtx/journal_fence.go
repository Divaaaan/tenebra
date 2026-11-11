package installtx

import (
	"context"
	"errors"
)

// WithDurableFence authorizes stopping this OLD runtime for one exact NEW
// transaction. The callback may only close local admission/start its drain;
// waiting for that drain must happen after this short CAS lock is released.
func (f *JournalFile) WithDurableFence(ctx context.Context, txID string, runtime RuntimeRef, begin func() error) error {
	if f == nil || f.root == nil || begin == nil || !protocolID(txID, "it1_") || !validRuntime(&runtime) {
		return ErrProtocol
	}
	lock, err := f.root.LockWriter(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	j, err := f.ReadJournal(ctx)
	if err != nil || j.Validate() != nil || j.Transaction == nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	tx := j.Transaction
	if tx.ID != txID || tx.Phase != "fencing" || tx.OperationalCommitID != "" ||
		j.Active == nil || tx.PriorActive == nil || *j.Active != runtime || *tx.PriorActive != runtime {
		return ErrRecoveryRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return begin()
}
