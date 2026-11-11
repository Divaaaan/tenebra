//go:build linux || darwin

package installtx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"testing"
)

func TestNativeHelperPinnedKeyHasValidTauriMinisignFraming(t *testing.T) {
	if _, err := minisignPublicKey(nativeReleasePublicKey); err != nil {
		t.Fatal("pinned native key is not valid existing Tauri framing", err)
	}
}
func TestNativeHelperOutsideInstalledRoleCannotOpenMachineRuntime(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("isolated native root fixture")
	}
	if _, _, err := nativeHelperImage(context.Background()); !errors.Is(err, ErrUntrustedHelperRole) {
		t.Fatal("uninstalled TEST image entered privileged helper role", err)
	}
}
func TestNativeHelperJournalNeverInventsRecoveryOrCrossManagerOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("isolated native root fixture")
	}
	root := privateInstallerRoot(t)
	journal, err := OpenJournalFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	ctx := context.Background()
	if _, err := initializeNativeJournal(ctx, journal, "", false); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("recovery invented an empty journal", err)
	}
	if _, err := journal.ReadJournal(ctx); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("missing recovery journal was written", err)
	}
	first, err := initializeNativeJournal(ctx, journal, "dpkg", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initializeNativeJournal(ctx, journal, "pacman", true); !errors.Is(err, ErrConflict) {
		t.Fatal("package owner replaced", err)
	}
	last, err := journal.ReadJournal(ctx)
	if err != nil || !reflect.DeepEqual(first, last) {
		t.Fatal("cross-manager refusal changed metadata", last, err)
	}
}
