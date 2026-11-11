package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func nativeStateRoots(t *testing.T) (*securefs.Root, *securefs.Root) {
	t.Helper()
	if base := os.Getenv("TENEBRA_SECUREFS_TEST_ROOT"); base != "" {
		t.Setenv("GOTMPDIR", filepath.Clean(base))
	} else if runtime.GOOS == "windows" {
		t.Fatal("set explicit protected TENEBRA_SECUREFS_TEST_ROOT for NTFS fixtures")
	}
	policy := securefs.Policy{Mode: securefs.SameAccountPrivate}
	parent, err := securefs.OpenRoot(filepath.Join(t.TempDir(), "private"), policy, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := parent.OpenDir("data", policy, true)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := parent.OpenDir("backups", policy, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := data.Close(); err != nil {
			t.Error(err)
		}
		if err := backup.Close(); err != nil {
			t.Error(err)
		}
		if err := parent.Close(); err != nil {
			t.Error(err)
		}
	})
	return data, backup
}

func TestCanonicalNativeBackupBeforeReplaceAndReopenNoRekey(t *testing.T) {
	data, backup := nativeStateRoots(t)
	bundle := bundleFor(t, []profile.Profile{{ID: "kept", Name: "Friendly", Servers: []profile.Server{legacyNode("stored-arbitrary", "sni.test")}}}, `{"version":2,"kill_switch":true,"last_profile":"kept","last_node":"stored-arbitrary"}`, `{"kept":"stored-arbitrary"}`)
	for name, file := range legacyFiles(bundle) {
		if file.Present {
			if _, err := data.ReplaceFile(name, file.Bytes); err != nil {
				t.Fatal(err)
			}
		}
	}
	store, err := Open(context.Background(), data, backup)
	if err != nil {
		t.Fatal(err)
	}
	first := store.View()
	if err := data.Close(); !errors.Is(err, securefs.ErrBusy) {
		t.Fatal("store did not retain the sole kernel writer lock")
	}
	_, manifest, err := manifestFor(bundle)
	if err != nil {
		t.Fatal(err)
	}
	name := backupName(manifest)
	for fileName, file := range legacyFiles(bundle) {
		copied, err := backup.ReadFile(name+"/"+fileName, int64(len(file.Bytes)+1))
		if err != nil || !bytes.Equal(copied, file.Bytes) {
			t.Fatalf("private %s backup changed source bytes: %v", fileName, err)
		}
	}
	path, err := data.Path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profile.Open(path); err == nil {
		t.Fatal("old reader accepted the new canonical root")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := data.ReplaceFile("settings.json", []byte("{STALE_CORRUPT_LEGACY")); err != nil {
		t.Fatal(err)
	}
	writer, err := data.LockWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	store, err = openWithAllocator(context.Background(), path, &rootRepositoryFiles{data: data, backup: backup}, writer, func() (string, error) { called++; return "", io.ErrUnexpectedEOF })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if called != 0 || !equalJSON(first, store.View()) {
		t.Fatal("schema2 reopen rekeyed or read stale preferences")
	}
}

func TestCanonicalNativeSecondOwnerAndBackupFailurePreserveSource(t *testing.T) {
	data, backup := nativeStateRoots(t)
	store, err := Open(context.Background(), data, backup)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Open(context.Background(), data, backup); !errors.Is(err, securefs.ErrBusy) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("second repository acquired writer: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	legacy := bundleFor(t, []profile.Profile{{ID: "p", Name: "n", Servers: []profile.Server{legacyNode("old", "a.test")}}}, `{"version":2}`, "")
	for name, file := range legacyFiles(legacy) {
		if file.Present {
			data.ReplaceFile(name, file.Bytes)
		}
	}
	files := &fakeRepository{bundle: cloneLegacy(legacy), backupError: errors.New("synthetic backup sync fault"), commit: securefs.Committed}
	writer := &testWriter{}
	if store, err := openWithFiles(context.Background(), "metadata", files, writer); err == nil {
		store.Close()
		t.Fatal("failed backup still replaced canonical state")
	}
	if files.writes != 0 || !bytes.Equal(files.bundle.Profiles.Bytes, legacy.Profiles.Bytes) || writer.calls != 1 {
		t.Fatal("backup refusal mutated source or retained writer")
	}
}

func TestCanonicalProfileViewUpdateUsesProfileCAS(t *testing.T) {
	store, _ := openFakeCanonical(t)
	if err := store.Admit(); err != nil {
		t.Fatal(err)
	}
	view := store.Profiles()
	original, exists := view.Get("p")
	if !exists {
		t.Fatal("missing profile")
	}
	if _, err := store.Transact(context.Background(), AnyRevision, func(candidate *Snapshot) error { candidate.Preferences.AdBlock = true; return nil }); err != nil {
		t.Fatal(err)
	}
	updated := original
	updated.Name = "renamed"
	if err := view.Update(updated); err != nil {
		t.Fatalf("unrelated global preference spuriously conflicted with profile: %v", err)
	}
	if err := view.Update(original); !errors.Is(err, ErrProfileConflict) {
		t.Fatal("stale whole-profile update overwrote latest fields")
	}
	current, _ := view.Get("p")
	if current.Revision != original.Revision+1 || current.Name != "renamed" || !store.View().Preferences.AdBlock {
		t.Fatal("profile revision or unrelated preference lost")
	}
	data, _ := json.Marshal(store.View())
	var roundtrip Snapshot
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if err := view.Remove("p"); err != nil {
		t.Fatal(err)
	}
	if err := view.Update(current); !errors.Is(err, profile.ErrNotFound) {
		t.Fatal("removed profile resurrected")
	}
}
