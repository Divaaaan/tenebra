package securefs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type checkedStream struct {
	remaining int64
	largest   int
}

func (r *checkedStream) Read(p []byte) (int, error) {
	r.largest = max(r.largest, len(p))
	if len(p) > 32<<10 {
		return 0, errors.New("unbounded streaming allocation")
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.remaining)
	for i := range int(n) {
		p[i] = 'x'
	}
	r.remaining -= n
	return int(n), nil
}

type failedInput struct{}

func (failedInput) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestReplaceStreamBoundsPrivateModeAndPreservesOldOnFailure(t *testing.T) {
	directory := privateFixtureDir(t)
	root, err := OpenRoot(directory, Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, mode := range []uint32{0600, 0700} {
		name := "metadata"
		if mode == 0700 {
			name = "executable"
		}
		input := &checkedStream{remaining: 4 << 20}
		if state, err := root.ReplaceStream(context.Background(), name, input, 4<<20, mode); err != nil || state != Committed {
			t.Fatal(state, err)
		}
		if input.largest > 32<<10 {
			t.Fatal("growing stream buffer")
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(filepath.Join(directory, name))
			if err != nil || info.Mode().Perm() != os.FileMode(mode) {
				t.Fatal("private copied mode", info, err)
			}
		}
	}
	if state, err := root.ReplaceFile("kept", []byte("old")); state != Committed || err != nil {
		t.Fatal(state, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		reader io.Reader
		limit  int64
		mode   uint32
		ctx    context.Context
	}{
		{bytes.NewReader([]byte("oversized")), 3, 0600, context.Background()},
		{failedInput{}, 100, 0600, context.Background()},
		{bytes.NewReader([]byte("new")), 100, 0777, context.Background()},
		{bytes.NewReader([]byte("new")), 100, 0600, cancelled},
	} {
		if state, err := root.ReplaceStream(tc.ctx, "kept", tc.reader, tc.limit, tc.mode); state != Unchanged || err == nil {
			t.Fatal("failure published stream", state, err)
		}
		got, err := root.ReadFile("kept", 3)
		if err != nil || string(got) != "old" {
			t.Fatal("old target changed", err)
		}
	}
	lease, err := root.LeaseTree(context.Background(), Limits{MaxFiles: 8, MaxBytes: 9 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := root.ReplaceStream(context.Background(), "kept", bytes.NewReader(nil), 0, 0600); state != Unchanged || !errors.Is(err, ErrBusy) {
		t.Fatal("stream mutated leased bytes", state, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}
