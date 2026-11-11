package installtx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func privateInstallerRoot(t *testing.T) *securefs.Root {
	t.Helper()
	var directory string
	if base := os.Getenv("TENEBRA_SECUREFS_TEST_ROOT"); base != "" {
		if !filepath.IsAbs(base) {
			t.Fatal("nonabsolute private fixture anchor")
		}
		anchor, err := securefs.OpenRoot(base, securefs.Policy{Mode: securefs.SameAccountPrivate}, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := anchor.Close(); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMP", base)
		t.Setenv("TEMP", base)
		t.Setenv("TMPDIR", base)
		// Native readiness adds a fixed transaction socket below this root.
		// Keep its length independent of testing.T's descriptive test name.
		directory, err = os.MkdirTemp(base, "it-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			relative, err := filepath.Rel(base, directory)
			if err != nil || relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") {
				t.Error("private fixture cleanup escaped its verified anchor", directory, err)
				return
			}
			if err := os.RemoveAll(directory); err != nil {
				t.Error(err)
			}
		})
	} else {
		directory = t.TempDir()
	}
	root, err := securefs.OpenRoot(filepath.Join(directory, "private"), securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	return root
}

func TestPrivateInstallerFixtureKeepsLongTestNamesOutOfSocketPaths(t *testing.T) {
	base := filepath.Join(t.TempDir(), "anchor")
	anchor, err := securefs.OpenRoot(base, securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	base, err = anchor.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := anchor.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TENEBRA_SECUREFS_TEST_ROOT", base)
	t.Run(strings.Repeat("long-fixture-name-", 8), func(t *testing.T) {
		root := privateInstallerRoot(t)
		path, err := root.Path()
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(base, path)
		if err != nil || len(relative) > 40 || strings.HasPrefix(relative, "..") {
			t.Fatalf("test name leaked into private socket root: %q (%v)", relative, err)
		}
	})
}
func TestJournalCASAndServiceReadDoNotWaitTransactionLock(t *testing.T) {
	ctx := context.Background()
	root := privateInstallerRoot(t)
	one, err := openAccountJournalFile(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { one.Close() })
	two, err := openAccountJournalFile(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { two.Close() })
	lock, err := one.LockTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if another, err := two.LockTransaction(ctx); !errors.Is(err, securefs.ErrBusy) {
		if another != nil {
			another.Close()
		}
		t.Fatalf("competing transaction admitted: %v", err)
	}
	initial := Journal{Schema: 1, Generation: 1, Manager: "standalone"}
	if err := one.ReplaceJournal(ctx, initial); err != nil {
		t.Fatal(err)
	}
	if observed, err := two.ReadJournal(ctx); err != nil || observed.Generation != 1 {
		t.Fatal("service read waited transaction lock", observed, err)
	}
	next := initial
	next.Generation = 2
	if err := one.ReplaceJournal(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err := two.ReplaceJournal(ctx, next); !errors.Is(err, ErrConflict) {
		t.Fatal("last-write-wins allowed", err)
	}
	if observed, err := two.ReadJournal(ctx); err != nil || observed.Generation != 2 {
		t.Fatal(observed, err)
	}
}
