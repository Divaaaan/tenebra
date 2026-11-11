package installtx

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestHelperRecoverBindsTransactionUnderRecoveryLock(t *testing.T) {
	d := installerDisk()
	installer := reconstructedInstaller(d)
	if _, err := installer.Apply(context.Background(), installRequest(d)); err != nil {
		t.Fatal(err)
	}
	d.calls = nil
	before := cloneJournal(d.journal)
	if _, err := installer.RecoverTransaction(context.Background(), "it1_"+strings.Repeat("f", 32)); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong transaction recovered", err)
	}
	if len(d.calls) != 0 || !reflect.DeepEqual(before, d.journal) {
		t.Fatal("mismatched recovery mutated another transaction", d.calls)
	}
	if _, err := installer.RecoverTransaction(context.Background(), "it1_invalid"); !errors.Is(err, ErrProtocol) {
		t.Fatal("invalid transaction accepted", err)
	}
	// A matching postcommit recovery retains the existing operational commit.
	oc := d.journal.Transaction.OperationalCommitID
	if _, err := installer.RecoverTransaction(context.Background(), d.journal.Transaction.ID); err != nil {
		t.Fatal(err)
	}
	if d.journal.Transaction.OperationalCommitID != oc || d.restoreCount != 0 || d.journal.Transaction.Phase != "committed_open" {
		t.Fatal("bound recovery rolled back committed authority", d.journal)
	}
}
