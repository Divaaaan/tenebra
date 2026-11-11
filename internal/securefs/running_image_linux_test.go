//go:build linux

package securefs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func ownRunningImageFixture(t *testing.T) (*Root, string, []byte) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("native running-root image fixture requires isolated uid0")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(filepath.Dir(executable), Policy{Mode: OperatorInput, Principal: "uid:0"}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	return root, filepath.Base(executable), body
}
func TestRunningImageHashUsesActualKernelExecutable(t *testing.T) {
	root, name, body := ownRunningImageFixture(t)
	digest, err := root.HashRunningExecutable(context.Background(), name, int64(len(body)))
	sum := sha256.Sum256(body)
	if err != nil || digest != hex.EncodeToString(sum[:]) {
		t.Fatal("actual kernel image did not match retained role", digest, err)
	}
}
func TestRunningImageRejectsByteIdenticalCopyAtAnotherInode(t *testing.T) {
	_, _, body := ownRunningImageFixture(t)
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "copied-helper"), body, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(directory, Policy{Mode: MachinePrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if digest, err := root.HashRunningExecutable(context.Background(), "copied-helper", int64(len(body))); digest != "" || !errors.Is(err, ErrChanged) {
		t.Fatal("equal copied bytes claimed to be running image", digest, err)
	}
}
func TestRunningImageBoundsCancellationAndNoFollow(t *testing.T) {
	root, name, body := ownRunningImageFixture(t)
	if digest, err := root.HashRunningExecutable(context.Background(), name, int64(len(body)-1)); digest != "" || !errors.Is(err, ErrLimit) {
		t.Fatal("image hash exceeded bound", digest, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if digest, err := root.HashRunningExecutable(ctx, name, int64(len(body))); digest != "" || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled hash returned identity", digest, err)
	}
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(directory, "helper")); err != nil {
		t.Fatal(err)
	}
	other, err := OpenRoot(directory, Policy{Mode: MachinePrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if digest, err := other.HashRunningExecutable(context.Background(), "helper", int64(len(body))); digest != "" || err == nil {
		t.Fatal("source symlink supplied running identity", digest, err)
	}
}
