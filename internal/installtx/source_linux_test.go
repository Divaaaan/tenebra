//go:build linux

package installtx

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceRejectsForeignOwnerAndWritableLeaf(t *testing.T) {
	for _, kind := range []string{"foreign-owner", "writable-leaf"} {
		t.Run(kind, func(t *testing.T) {
			stager, source, _, manifest := stagingFixture(t, "flat", "release", false)
			leaf := filepath.Join(source.AbsolutePath, "tenebra-core")
			if kind == "foreign-owner" {
				if os.Geteuid() != 0 {
					t.Skip("native chown fixture requires isolated root test process")
				}
				if err := os.Chown(leaf, 65534, -1); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Chmod(leaf, 0666); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := stager.Stage(context.Background(), source, manifest); err == nil {
				t.Fatal("untrusted source reached private executable candidate")
			}
			requireNoPublishedSource(t, stager, manifest)
		})
	}
}
