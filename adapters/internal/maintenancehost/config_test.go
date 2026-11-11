package maintenancehost

import (
	"bytes"
	"context"
	"errors"
	"github.com/Divaaaan/tenebra/internal/securefs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func configTestRoot(t *testing.T) *securefs.Root {
	t.Helper()
	if base := os.Getenv("TENEBRA_SECUREFS_TEST_ROOT"); base != "" {
		t.Setenv("GOTMPDIR", filepath.Clean(base))
	} else if runtime.GOOS == "windows" {
		t.Fatal("set explicit protected TENEBRA_SECUREFS_TEST_ROOT")
	}
	root, err := securefs.OpenRoot(filepath.Join(t.TempDir(), "private"), securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
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

func TestMaintenanceConfigPrivatePlacement(t *testing.T) {
	root := configTestRoot(t)
	payload := []byte(`{"synthetic":"private-config-only"}`)
	lease, err := StageConfig(context.Background(), root, payload)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	path, err := lease.Path()
	if err != nil {
		t.Fatal(err)
	}
	parent, err := root.Path()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(parent, path)
	if err != nil || relative == ".." || filepath.IsAbs(relative) {
		t.Fatal("config escaped private root")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("wrong staged bytes", err)
	}
	if err := root.Close(); !errors.Is(err, securefs.ErrBusy) {
		t.Fatal("lease did not retain root lifetime", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Path(); !errors.Is(err, securefs.ErrClosed) {
		t.Fatal("closed config returned a path", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("owned config was not retired", err)
	}
}

func TestMaintenanceConfigRefusesInvalidOrUnavailableStorage(t *testing.T) {
	root := configTestRoot(t)
	for _, data := range [][]byte{nil, []byte(`not-json`)} {
		if _, err := StageConfig(context.Background(), root, data); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := StageConfig(ctx, root, []byte(`{}`)); err == nil {
		t.Fatal("cancelled stage accepted")
	}
	if _, err := StageConfig(context.Background(), nil, []byte(`{}`)); err == nil {
		t.Fatal("ambient temp fallback")
	}
}
