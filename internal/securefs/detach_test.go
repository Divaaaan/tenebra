package securefs

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestDetachFileRequiresPrivateWriterExpectedBytesAndNoLease(t *testing.T) {
	root, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	const content = "SYNTHETIC_PRIVATE_CHECKPOINT"
	if _, err := root.ReplaceFile("profiles.json", []byte(content)); err != nil {
		t.Fatal(err)
	}
	if _, state, err := root.DetachFile("profiles.json", detachSHA([]byte(content)), 1024); state != Unchanged || !errors.Is(err, ErrBusy) {
		t.Fatal("detach without kernel writer", state, err)
	}
	lease, err := root.LeaseTree(context.Background(), Limits{MaxFiles: 8, MaxBytes: 1024}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, state, err := root.DetachFile("profiles.json", detachSHA([]byte(content)), 1024); state != Unchanged || !errors.Is(err, ErrBusy) {
		t.Fatal("detach with active lease", state, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := root.LockWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	for _, test := range []struct {
		name, sha string
		limit     int64
	}{{"profiles.json", detachSHA([]byte("wrong")), 1024}, {"profiles.json", detachSHA([]byte(content)), 1}, {".writer.lock", detachSHA(nil), 1}, {"../profiles.json", detachSHA([]byte(content)), 1024}} {
		if _, state, err := root.DetachFile(test.name, test.sha, test.limit); state != Unchanged || err == nil {
			t.Fatal("unsafe detach accepted", test.name, state, err)
		}
	}
	data, err := root.ReadFile("profiles.json", 1024)
	if err != nil || string(data) != content {
		t.Fatal("refusal changed trusted source", err)
	}
}
func TestDetachFileRetainsExactPrivateBytesAndRepeatedReceipt(t *testing.T) {
	path := privateFixtureDir(t)
	root, err := OpenRoot(path, Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("SYNTHETIC_PRIVATE_BYTES\x00\xff")
	if _, err := root.ReplaceFile("profiles.json", data); err != nil {
		t.Fatal(err)
	}
	writer, err := root.LockWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	receipt, state, err := root.DetachFile("profiles.json", detachSHA(data), int64(len(data)))
	if err != nil || state != Committed || !validDetachID(receipt.ID) {
		t.Fatal("detach not confirmed", state, err)
	}
	if _, err := root.ReadFile("profiles.json", 1024); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("source name remains", err)
	}
	archive, err := root.ReadFile(".detached-"+receipt.ID+".payload", 1024)
	if err != nil || !bytes.Equal(archive, data) {
		t.Fatal("archive lost exact bytes", err)
	}
	if _, err := root.ReadFile("quarantine-index.json", 1024); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("trusted detach created a quarantine recovery fence", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	// A fresh process owner must verify archive identity/hash and the committed
	// receipt, rather than treating the absent source as success by itself.
	root, err = OpenRoot(path, Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	writer, err = root.LockWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if repeated, state, err := root.DetachFile("profiles.json", detachSHA(data), int64(len(data))); err != nil || state != Committed || repeated != receipt {
		t.Fatal("replay changed receipt or lost proof", repeated, state, err)
	}
	if _, state, err := root.DetachFile("missing.json", detachSHA(nil), 1); state != Unchanged || !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("mere absence accepted as a completed detach", state, err)
	}
}

type detachFaultNative struct {
	nativeOps
	before, after bool
}

func (n *detachFaultNative) replacePrivate(parent, source handle, temporary, target string) (CommitState, error) {
	if temporary != "profiles.json" {
		return n.nativeOps.replacePrivate(parent, source, temporary, target)
	}
	if n.before {
		return Unchanged, errInjected
	}
	state, err := n.nativeOps.replacePrivate(parent, source, temporary, target)
	if state == Committed && err == nil && n.after {
		return Indeterminate, errInjected
	}
	return state, err
}
func TestDetachFileMoveBoundaryFaultAndReconstructedOwner(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_move", true: "after_move"}[after], func(t *testing.T) {
			path := privateFixtureDir(t)
			ops, err := newNativeOps()
			if err != nil {
				t.Fatal(err)
			}
			fault := &detachFaultNative{nativeOps: ops}
			root, err := openRootWithOps(path, Policy{Mode: SameAccountPrivate}, false, fault)
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("SYNTHETIC_RETAINED_ON_MOVE_FAULT")
			if _, err := root.ReplaceFile("profiles.json", data); err != nil {
				t.Fatal(err)
			}
			writer, err := root.LockWriter(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			fault.before, fault.after = !after, after
			receipt, state, err := root.DetachFile("profiles.json", detachSHA(data), 1024)
			want := Unchanged
			if after {
				want = Indeterminate
			}
			if state != want || !errors.Is(err, errInjected) || !validDetachID(receipt.ID) {
				t.Fatal(state, err)
			}
			if !after {
				kept, err := root.ReadFile("profiles.json", 1024)
				if err != nil || !bytes.Equal(kept, data) {
					t.Fatal("pre-move fault lost source", err)
				}
			} else {
				kept, err := root.ReadFile(".detached-"+receipt.ID+".payload", 1024)
				if err != nil || !bytes.Equal(kept, data) {
					t.Fatal("post-move fault lost archive", err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := root.Close(); err != nil {
				t.Fatal(err)
			}
			root, err = OpenRoot(path, Policy{Mode: SameAccountPrivate}, false)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			writer, err = root.LockWriter(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			repeated, state, err := root.DetachFile("profiles.json", detachSHA(data), 1024)
			if after {
				if state != Indeterminate || err == nil || repeated != receipt {
					t.Fatal("unknown move falsely acknowledged by observed absence", repeated, state, err)
				}
			} else if state != Committed || err != nil || repeated != receipt {
				t.Fatal("known-unchanged retry failed", repeated, state, err)
			}
		})
	}
}
func TestDetachFileRejectsHardlinkAndUntrustedReceipt(t *testing.T) {
	path := privateFixtureDir(t)
	root, err := OpenRoot(path, Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data := []byte("SYNTHETIC_DATA")
	if _, err := root.ReplaceFile("profiles.json", data); err != nil {
		t.Fatal(err)
	}
	writer, err := root.LockWriter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	link := filepath.Join(path, "extra-link")
	if err := os.Link(filepath.Join(path, "profiles.json"), link); err != nil {
		t.Fatal(err)
	}
	if _, state, err := root.DetachFile("profiles.json", detachSHA(data), 1024); state != Unchanged || !errors.Is(err, ErrUntrusted) {
		t.Fatal("hardlink detached", state, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if _, err := root.ReplaceFile(detachLocatorName("profiles.json"), []byte(`{"version":1,"version":1,"id":"d1_00000000000000000000000000000000"}`)); err != nil {
		t.Fatal(err)
	}
	if _, state, err := root.DetachFile("profiles.json", detachSHA(data), 1024); state != Unchanged || !errors.Is(err, ErrUntrusted) {
		t.Fatal("duplicate-key recovery receipt accepted", state, err)
	}
}
