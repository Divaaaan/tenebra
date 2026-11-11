//go:build go1.25

package state

import (
	"context"
	"errors"
	"github.com/Divaaaan/tenebra/internal/securefs"
	"testing"
	"testing/synctest"
)

func TestCanonicalFenceDoesNotAcknowledgeIndeterminateAdmittedWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, files := openFakeCanonical(t)
		if err := store.Admit(); err != nil {
			t.Fatal(err)
		}
		files.entered, files.release = make(chan struct{}), make(chan struct{})
		files.commit, files.replaceError = securefs.Indeterminate, errors.New("final sync failed")
		write := make(chan error, 1)
		go func() {
			_, err := store.Transact(context.Background(), AnyRevision, func(s *Snapshot) error { s.Preferences.AdBlock = true; return nil })
			write <- err
		}()
		<-files.entered
		fenced := make(chan error, 1)
		go func() { fenced <- store.Fence(context.Background()) }()
		synctest.Wait()
		close(files.release)
		if err := <-write; !errors.Is(err, ErrDurabilityUnknown) {
			t.Fatal(err)
		}
		if err := <-fenced; !errors.Is(err, ErrDurabilityUnknown) {
			t.Fatalf("fence acknowledged unknown durable state: %v", err)
		}
		if err := store.Admit(); !errors.Is(err, ErrDurabilityUnknown) {
			t.Fatal(err)
		}
	})
}
