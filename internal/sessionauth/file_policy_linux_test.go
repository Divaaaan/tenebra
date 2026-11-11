//go:build linux

package sessionauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestOperatorPolicyNativeFileTrust(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires isolated root-owned Linux fixtures")
	}
	for _, problem := range []string{"valid", "missing", "public", "writable", "executable", "owner", "symlink", "hardlink", "malformed"} {
		t.Run(problem, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "operators.json")
			if problem != "missing" {
				body := []byte(`{"schema":1,"uids":[1001]}`)
				if problem == "malformed" {
					body = []byte(`{"schema":1,"uids":[1001,1001]}`)
				}
				if err := os.WriteFile(file, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch problem {
			case "public":
				if err := os.Chmod(file, 0644); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(file, 0620); err != nil {
					t.Fatal(err)
				}
			case "executable":
				if err := os.Chmod(file, 0700); err != nil {
					t.Fatal(err)
				}
			case "owner":
				if err := os.Chown(file, 1001, 1001); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(file, filepath.Join(dir, "target")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("target", file); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(file, filepath.Join(dir, "alias")); err != nil {
					t.Fatal(err)
				}
			}
			uids, err := loadPolicyFile(context.Background(), file)
			if problem == "valid" {
				if err != nil || !slices.Equal(uids, []uint32{1001}) {
					t.Fatal(uids, err)
				}
			} else if problem == "missing" {
				if err != nil || len(uids) != 0 {
					t.Fatal(uids, err)
				}
			} else if !errors.Is(err, ErrOperatorPolicy) {
				t.Fatal("untrusted policy admitted operators", uids, err)
			}
		})
	}
}
