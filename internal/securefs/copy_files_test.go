package securefs

import (
	"context"
	"errors"
	"io/fs"
	"testing"
)

func TestCopyFilesSelectsOnlyNamedRegularInputs(t *testing.T) {
	ctx := context.Background()
	source, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	parent, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if outcome, err := source.ReplaceFile("container", []byte("chosen")); outcome != Committed || err != nil {
		t.Fatal(outcome, err)
	}
	if outcome, err := source.ReplaceFile("unrelated-private", []byte("must not be copied")); outcome != Committed || err != nil {
		t.Fatal(outcome, err)
	}
	for _, names := range [][]string{nil, {"../container"}, {"container", "container"}, {"container/nested"}, {sealName}} {
		stage, err := parent.NewStage("invalid-")
		if err != nil {
			t.Fatal(err)
		}
		if err := CopyFilesToStage(ctx, source, stage, names, Limits{MaxFiles: 2, MaxBytes: 100}); err == nil {
			t.Fatal("invalid filtered request accepted", names)
		}
		if err := stage.Close(); err != nil {
			t.Fatal(err)
		}
	}
	stage, err := parent.NewStage("selected-")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	if err := CopyFilesToStage(ctx, source, stage, []string{"container"}, Limits{MaxFiles: 1, MaxBytes: 6}); err != nil {
		t.Fatal(err)
	}
	if got, err := stage.Root().ReadFile("container", 6); err != nil || string(got) != "chosen" {
		t.Fatal(err)
	}
	if _, err := stage.Root().ReadFile("unrelated-private", 100); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("adjacent input copied", err)
	}
}
