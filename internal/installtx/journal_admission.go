package installtx

import (
	"context"
	"errors"
)

// WithDurableAdmission serializes the final journal verdict with the local
// writer gate. A concurrent helper cannot publish fencing between the verdict
// and Store.Admit. The callback may inspect local memory/admit, never perform
// blocking external I/O. It does not acquire the long-lived transaction lock.
func (f *JournalFile) WithDurableAdmission(ctx context.Context, txID, ocID string, runtime RuntimeRef, admit func(Journal) error) error {
	if f == nil || f.root == nil || admit == nil || !protocolID(txID, "it1_") || !protocolID(ocID, "oc1_") || !validRuntime(&runtime) {
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
	if tx.ID != txID || tx.OperationalCommitID != ocID || !oneOf(tx.Phase, "committed_closed", "committed_open") || tx.Action == "remove" ||
		tx.SchemaReceipt == nil || j.Active == nil || tx.Candidate == nil || *j.Active != runtime || *tx.Candidate != runtime {
		return ErrRecoveryRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return admit(j)
}
