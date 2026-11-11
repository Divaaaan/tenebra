package installtx

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func TestDurableFenceRequiresExactNewTransactionAndPriorRuntime(t *testing.T) {
	ctx := context.Background()
	file, err := openAccountJournalFile(privateInstallerRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	j := installerDisk().journal
	if err := file.ReplaceJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	newTx := "it1_" + strings.Repeat("d", 32)
	var calls int
	begin := func() error { calls++; return nil }
	if err := file.WithDurableFence(ctx, newTx, *j.Active, begin); !errors.Is(err, ErrRecoveryRequired) || calls != 0 {
		t.Fatal("old committed transaction authorized a new fence", err)
	}
	j.Generation++
	j.Transaction = &Transaction{ID: newTx, Action: "remove", Phase: "fencing", Trust: "release", PriorActive: j.Active}
	if err := file.ReplaceJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	wrong := *j.Active
	wrong.Version = "999.0.0"
	if err := file.WithDurableFence(ctx, newTx, wrong, begin); !errors.Is(err, ErrRecoveryRequired) || calls != 0 {
		t.Fatal("different runtime was fenced", err)
	}
	if err := file.WithDurableFence(ctx, "it1_"+strings.Repeat("e", 32), *j.Active, begin); !errors.Is(err, ErrRecoveryRequired) || calls != 0 {
		t.Fatal("different transaction was fenced", err)
	}
	if err := file.WithDurableFence(ctx, newTx, *j.Active, begin); err != nil || calls != 1 {
		t.Fatal("exact durable fence was refused", err)
	}
}

func TestDurableFenceSerializesGateClosureWithJournalCAS(t *testing.T) {
	ctx := context.Background()
	file, err := openAccountJournalFile(privateInstallerRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	j := installerDisk().journal
	j.Transaction = &Transaction{ID: "it1_" + strings.Repeat("d", 32), Action: "remove", Phase: "fencing", Trust: "release", PriorActive: j.Active}
	if err := file.ReplaceJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	next := cloneJournal(j)
	next.Generation++
	next.Transaction.Phase = "fenced"
	if err := file.WithDurableFence(ctx, j.Transaction.ID, *j.Active, func() error {
		if err := file.ReplaceJournal(ctx, next); !errors.Is(err, securefs.ErrBusy) {
			t.Error("CAS crossed gate closure", err)
		}
		if current, err := file.ReadJournal(ctx); err != nil || current.Generation != j.Generation {
			t.Error("read-only status blocked or changed", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := file.ReplaceJournal(ctx, next); err != nil {
		t.Fatal("CAS remained locked after local gate closure", err)
	}
}
