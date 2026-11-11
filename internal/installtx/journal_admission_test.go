package installtx

import (
	"context"
	"errors"
	"testing"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func TestDurableAdmissionCannotRaceNewJournalFence(t *testing.T) {
	ctx := context.Background()
	root := privateInstallerRoot(t)
	file, err := openAccountJournalFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	j := installerDisk().journal
	if err := file.ReplaceJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	tx := j.Transaction
	calls := 0
	if err := file.WithDurableAdmission(ctx, tx.ID, "oc1_00000000000000000000000000000000", *j.Active, func(Journal) error { calls++; return nil }); !errors.Is(err, ErrRecoveryRequired) || calls != 0 {
		t.Fatal("uncommitted identity reached writer gate", err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- file.WithDurableAdmission(ctx, tx.ID, tx.OperationalCommitID, *j.Active, func(Journal) error { close(entered); <-release; return nil })
	}()
	<-entered
	next := cloneJournal(j)
	next.Generation++
	next.Transaction.Phase = "committed_closed"
	if err := file.ReplaceJournal(ctx, next); !errors.Is(err, securefs.ErrBusy) {
		close(release)
		<-done
		t.Fatal("fencing CAS crossed admission verdict", err)
	}
	if current, err := file.ReadJournal(ctx); err != nil || current.Generation != j.Generation {
		close(release)
		<-done
		t.Fatal("read-only service status waited on admission lock", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := file.ReplaceJournal(ctx, next); err != nil {
		t.Fatal(err)
	}
}
