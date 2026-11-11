package securefs

import (
	"context"
	"errors"
	"testing"
)

func TestWriterLockExclusiveCancelledAndRetainsRoot(t *testing.T) {
	root, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	first, err := root.LockWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second, err := root.LockWriter(context.Background()); !errors.Is(err, ErrBusy) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second writer=%v", err)
	}
	if err := root.Close(); !errors.Is(err, ErrBusy) {
		t.Fatalf("writer did not retain root: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if lock, err := root.LockWriter(ctx); !errors.Is(err, context.Canceled) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("cancelled writer=%v", err)
	}
}
