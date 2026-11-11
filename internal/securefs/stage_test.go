package securefs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyToStageVerifiesCopiedBytesAndInputHasNoPath(t *testing.T) {
	sourcePath := privateFixtureDir(t)
	source, err := OpenRoot(sourcePath, Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.ReplaceFile("payload", []byte("COPIED_SYNTHETIC_BYTES")); err != nil {
		t.Fatal(err)
	}
	actor, err := source.state.ops.principal()
	if err != nil {
		t.Fatal(err)
	}
	input, err := OpenRoot(sourcePath, Policy{Mode: OperatorInput, Principal: actor}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.Path(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("operator input exposed a launch path: %v", err)
	}
	if _, err := input.LeaseTree(context.Background(), Limits{10, 128}, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("operator input acquired a lease: %v", err)
	}
	destination, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	stage, err := destination.NewStage("copy-")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	if err := CopyToStage(context.Background(), input, stage, Limits{10, 128}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReplaceFile("payload", []byte("SOURCE_CHANGED_AFTER_COPY")); err != nil {
		t.Fatal(err)
	}
	published, state, err := stage.Publish(context.Background(), "copied-generation", Limits{10, 128}, func(ctx context.Context, lease *PathLease) error {
		bytes, err := lease.ReadFile("payload", 128)
		if err != nil {
			return err
		}
		if string(bytes) != "COPIED_SYNTHETIC_BYTES" {
			return ErrUntrusted
		}
		return nil
	})
	if err != nil || state != Committed {
		t.Fatalf("copied payload publish: %v,%v", state, err)
	}
	defer published.Close()
}

func TestCopyBudgetFailurePoisonsStageAndPreservesSource(t *testing.T) {
	source, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.ReplaceFile("one", []byte("KEEP_ONE")); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReplaceFile("two", []byte("KEEP_TWO")); err != nil {
		t.Fatal(err)
	}
	parent, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stage, err := parent.NewStage("partial-")
	if err != nil {
		t.Fatal(err)
	}
	if err := CopyToStage(context.Background(), source, stage, Limits{1, 128}); !errors.Is(err, ErrLimit) {
		t.Fatalf("copy limit: %v", err)
	}
	if _, _, err := stage.Publish(context.Background(), "must-not-activate", Limits{10, 128}, func(context.Context, *PathLease) error { return nil }); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("poisoned copy activated: %v", err)
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := source.ReadFile("one", 128); err != nil || string(data) != "KEEP_ONE" {
		t.Fatalf("source damaged: %q,%v", data, err)
	}
}

func TestStageCleanupRefusesUninventoriedObjects(t *testing.T) {
	parent, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stage, err := parent.NewStage("owned-")
	if err != nil {
		t.Fatal(err)
	}
	path, err := stage.Root().Path()
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(path, "outside-inventory")
	if err := os.WriteFile(foreign, []byte("MUST_NOT_DELETE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stage.Close(); !errors.Is(err, ErrChanged) {
		t.Fatalf("unowned object cleanup: %v", err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "MUST_NOT_DELETE" {
		t.Fatalf("foreign data removed: %q,%v", data, err)
	}
	if err := os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationFenceCoversIndependentRootAndNestedReopen(t *testing.T) {
	parent, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stage, err := parent.NewStage("fenced-")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	dir, err := stage.Root().OpenDir("nested", Policy{Mode: SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dir.ReplaceFile("payload", []byte("KEEP")); err != nil {
		t.Fatal(err)
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	path, err := stage.Root().Path()
	if err != nil {
		t.Fatal(err)
	}
	independent, err := OpenRoot(path, Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer independent.Close()
	published, state, err := stage.Publish(context.Background(), "immutable-generation", Limits{10, 128}, func(context.Context, *PathLease) error {
		if _, err := independent.ReplaceFile("late", []byte("BAD")); !errors.Is(err, ErrBusy) {
			return ErrChanged
		}
		return nil
	})
	if err != nil || state != Committed {
		t.Fatalf("publication: %v,%v", state, err)
	}
	defer published.Close()
	if _, err := independent.ReplaceFile("late", []byte("BAD")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("independent root thawed publication: %v", err)
	}
	if nested, err := parent.OpenDir("immutable-generation/new-child", Policy{Mode: SameAccountPrivate}, true); !errors.Is(err, ErrUnsupported) {
		if nested != nil {
			nested.Close()
		}
		t.Fatalf("created child inside published tree: %v", err)
	}
	publishedPath, err := published.Path()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRoot(filepath.Join(publishedPath, "nested"), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.ReplaceFile("payload", []byte("BAD")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("nested cold reopen thawed publication: %v", err)
	}
}
