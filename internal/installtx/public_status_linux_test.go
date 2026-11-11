//go:build linux

package installtx

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPublicStatusProjectionPreservesUnknownFile(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("isolated native machine publisher requires root")
	}
	root := privateInstallerRoot(t)
	directory, err := root.Path()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "tenebra-service-status.json")
	status := journalStatus(installerDisk().journal)
	if err := publishPublicStatusAt(context.Background(), path, status); err != nil {
		t.Fatal(err)
	}
	got, err := readPublicStatusAt(path)
	if err != nil || !reflect.DeepEqual(got, status) {
		t.Fatal(got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("status was not published read-only for public consumers", info, err)
	}
	status.ErrorCode = "synthetic_error"
	if err := publishPublicStatusAt(context.Background(), path, status); err != nil {
		t.Fatal(err)
	}
	unknown := []byte(`{"phase":"unknown","profile_url":"synthetic-secret"}`)
	if err := os.WriteFile(path, unknown, 0644); err != nil {
		t.Fatal(err)
	}
	if err := publishPublicStatusAt(context.Background(), path, status); err == nil {
		t.Fatal("unknown file auto-repaired")
	}
	body, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(body, unknown) {
		t.Fatal("unknown status forensic bytes changed", err)
	}
}
