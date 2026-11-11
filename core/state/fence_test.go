package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCanonicalBusinessFenceJoinsAdmittedWriteAndRejectsNew(t *testing.T) {
	store, files := openFakeCanonical(t)
	store.Admit()
	files.entered = make(chan struct{})
	files.release = make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := store.Transact(context.Background(), AnyRevision, func(candidate *Snapshot) error { candidate.Preferences.AdBlock = true; return nil })
		result <- err
	}()
	<-files.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Fence(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("fence acknowledged an undrained write: %v", err)
	}
	calls := 0
	if _, err := store.Transact(context.Background(), AnyRevision, func(*Snapshot) error { calls++; return nil }); !errors.Is(err, ErrMaintenancePending) || calls != 0 {
		t.Fatal("fence admitted another transaction")
	}
	if err := store.Admit(); !errors.Is(err, ErrMaintenancePending) {
		t.Fatal("admission reopened while active write was undrained")
	}
	close(files.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := store.Fence(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !store.View().Preferences.AdBlock || !errors.Is(store.Ready(), ErrMaintenancePending) {
		t.Fatal("already-admitted commit lost or gate reopened")
	}
}

func TestCanonicalCancelAfterAdmittedReplaceReturnsCommittedOutcome(t *testing.T) {
	store, files := openFakeCanonical(t)
	store.Admit()
	before := store.View().Revision
	files.entered = make(chan struct{})
	files.release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		snapshot Snapshot
		err      error
	}
	result := make(chan outcome, 1)
	go func() {
		snapshot, err := store.Transact(ctx, AnyRevision, func(candidate *Snapshot) error { candidate.Preferences.AdBlock = true; return nil })
		result <- outcome{snapshot, err}
	}()
	<-files.entered
	cancel()
	close(files.release)
	select {
	case got := <-result:
		if got.err != nil || got.snapshot.Revision != before+1 || !store.View().Preferences.AdBlock {
			t.Fatal("confirmed durable commit was reported as cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("commit outcome did not drain")
	}
}
