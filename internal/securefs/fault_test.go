package securefs

import (
	"context"
	"errors"
	"testing"
)

var errInjected = errors.New("synthetic I/O fault")

type faultNative struct {
	nativeOps
	beforeRename, afterRename bool
}

func (n *faultNative) replacePrivate(parent, temp handle, temporary, target string) (CommitState, error) {
	if n.beforeRename {
		return Unchanged, errInjected
	}
	state, err := n.nativeOps.replacePrivate(parent, temp, temporary, target)
	if err == nil && state == Committed && n.afterRename {
		return Indeterminate, errInjected
	}
	return state, err
}

func TestReplaceBeforeRenameUnchangedAfterRenameIndeterminate(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			ops, err := newNativeOps()
			if err != nil {
				t.Fatal(err)
			}
			fault := &faultNative{nativeOps: ops}
			root, err := openRootWithOps(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false, fault)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if _, err := root.ReplaceFile("state", []byte("OLD")); err != nil {
				t.Fatal(err)
			}
			fault.beforeRename = !after
			fault.afterRename = after
			state, err := root.ReplaceFile("state", []byte("NEW"))
			want := Unchanged
			if after {
				want = Indeterminate
			}
			if state != want || !errors.Is(err, errInjected) {
				t.Fatalf("state=%v err=%v, want %v/fault", state, err, want)
			}
			fault.beforeRename = false
			fault.afterRename = false
			data, err := root.ReadFile("state", 64)
			if err != nil {
				t.Fatal(err)
			}
			wantData := "OLD"
			if after {
				wantData = "NEW"
			}
			if string(data) != wantData {
				t.Fatalf("canonical outcome=%q want %q", data, wantData)
			}
		})
	}
}

func TestCopyCancelledNeverPublishesAndSourcePreserved(t *testing.T) {
	source, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.ReplaceFile("payload", []byte("KEEP")); err != nil {
		t.Fatal(err)
	}
	parent, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stage, err := parent.NewStage("cancelled-")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CopyToStage(ctx, source, stage, Limits{10, 128}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled copy=%v", err)
	}
	if _, _, err := stage.Publish(context.Background(), "must-not-publish", Limits{10, 128}, func(context.Context, *PathLease) error { return nil }); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("cancelled stage activated: %v", err)
	}
	data, err := source.ReadFile("payload", 128)
	if err != nil || string(data) != "KEEP" {
		t.Fatalf("cancel damaged source: %q,%v", data, err)
	}
}
