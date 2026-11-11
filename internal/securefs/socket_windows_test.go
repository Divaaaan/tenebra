//go:build windows

package securefs

import (
	"context"
	"errors"
	"io/fs"
	"testing"
)

func TestUnixSocketAPIUnsupportedOnWindowsWithoutMutation(t *testing.T) {
	root, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if socket, err := root.BindPrivateSocket(context.Background(), "candidate.sock"); !errors.Is(err, ErrUnsupported) || socket != nil {
		t.Fatal("Unix IPC entered Windows pipe namespace", err)
	}
	if _, err := root.ReadFile(".socket-writer.lock", 64); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("unsupported IPC mutated the root", err)
	}
}
