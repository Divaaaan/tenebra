package securefs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func privateFixtureDir(t *testing.T) string {
	t.Helper()
	if base := os.Getenv("TENEBRA_SECUREFS_TEST_ROOT"); base != "" {
		if !filepath.IsAbs(base) {
			t.Fatal("native fixture anchor must be absolute")
		}
		anchor, err := OpenRoot(base, Policy{Mode: SameAccountPrivate}, true)
		if err != nil {
			t.Fatalf("native fixture anchor is untrusted: %v", err)
		}
		if err := anchor.Close(); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMP", base)
		t.Setenv("TEMP", base)
		t.Setenv("TMPDIR", base)
	}
	// testing.TempDir's numbered child is 0755 on Unix. Create our own final
	// object with the production private creation path; never chmod a fixture
	// into trust or relax the policy to accept a public-readable directory.
	path := filepath.Join(t.TempDir(), "private")
	root, err := OpenRoot(path, Policy{Mode: SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativePrivateRootAtomicReadAndLifetime(t *testing.T) {
	root, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if errors.Is(err, ErrUnsupported) {
		t.Skip("native backend is not implemented for this OS")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	if state, err := root.ReplaceFile("payload.json", []byte("SYNTHETIC_PRIVATE_BYTES")); err != nil || state != Committed {
		t.Fatalf("replace=%v,%v", state, err)
	}
	if data, err := root.ReadFile("payload.json", 64); err != nil || string(data) != "SYNTHETIC_PRIVATE_BYTES" {
		t.Fatalf("read=%q,%v", data, err)
	}
	if data, err := root.ReadFile("payload.json", 3); !errors.Is(err, ErrLimit) || data != nil {
		t.Fatalf("bounded read=%v,%v", data, err)
	}
	if _, err := root.ReadFile("missing", 16); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing is not distinguishable: %v", err)
	}
	lease, err := root.LeaseTree(context.Background(), Limits{MaxFiles: 10, MaxBytes: 128}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.ReplaceFile("payload.json", []byte("BAD")); !errors.Is(err, ErrBusy) {
		t.Fatalf("live lease mutation: %v", err)
	}
	if err := root.Close(); !errors.Is(err, ErrBusy) {
		t.Fatalf("live lease close: %v", err)
	}
	stream, err := lease.OpenFile("payload.json", 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); !errors.Is(err, ErrBusy) {
		t.Fatalf("stream did not retain lease: %v", err)
	}
	if data, err := io.ReadAll(stream); err != nil || string(data) != "SYNTHETIC_PRIVATE_BYTES" {
		t.Fatalf("stream=%q,%v", data, err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	alias := *root
	if err := alias.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.ReadFile("payload.json", 64); !errors.Is(err, ErrClosed) {
		t.Fatalf("value alias resurrected root: %v", err)
	}
}

func TestPortableRelativeNamesRejectEscapes(t *testing.T) {
	for _, name := range []string{"", ".", "..", "/absolute", "../escape", "a//b", "a/../b", `a\b`, "C:escape", "a\x00b"} {
		if _, err := relativeParts(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	if got, err := relativeParts("one/two.json"); err != nil || len(got) != 2 {
		t.Fatalf("valid relative name: %v,%v", got, err)
	}
	for _, name := range []string{"CON", "nul.txt", "COM¹", "LPT9.log", "name.", "name ", "bad*name"} {
		if validWindowsComponent(name) {
			t.Errorf("accepted Windows alias %q", name)
		}
	}
}

func TestOwnerAndExplicitGrantPolicy(t *testing.T) {
	good := objectInfo{id: "fixture", owner: "S-1-5-18", kind: kindRegular, links: 1, securityKnown: true}
	policy := Policy{Mode: MachinePrivate}
	if err := verifyObject(good, policy, "S-1-5-18", false); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.owner = "FOREIGN_OWNER"
	if err := verifyObject(bad, policy, "S-1-5-18", false); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("foreign owner accepted: %v", err)
	}
	bad = good
	bad.grants = []grant{{principal: "FOREIGN", rights: rightMutate}}
	if err := verifyObject(bad, policy, "S-1-5-18", false); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("foreign explicit write accepted: %v", err)
	}
	bad = good
	bad.links = 2
	if err := verifyObject(bad, policy, "S-1-5-18", false); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("hardlink accepted: %v", err)
	}
}

func TestStageCopiesVerifiedBytesAndIsPermanentlyReadOnly(t *testing.T) {
	parent, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stage, err := parent.NewStage("stage-")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	if _, err := stage.Root().ReplaceFile("binary", []byte("INERT_TEST_PAYLOAD")); err != nil {
		t.Fatal(err)
	}
	alias := *stage.Root()
	published, state, err := stage.Publish(context.Background(), "generation", Limits{MaxFiles: 10, MaxBytes: 128}, func(ctx context.Context, lease *PathLease) error {
		data, err := lease.ReadFile("binary", 128)
		if err != nil {
			return err
		}
		if string(data) != "INERT_TEST_PAYLOAD" {
			return ErrUntrusted
		}
		return nil
	})
	if err != nil || state != Committed {
		t.Fatalf("publish=%v,%v", state, err)
	}
	defer published.Close()
	if _, err := alias.ReplaceFile("binary", []byte("BAD")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("published alias could mutate: %v", err)
	}
	reopened, err := parent.OpenDir("generation", Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ReplaceFile("binary", []byte("BAD_REOPEN")); !errors.Is(err, ErrUnsupported) {
		reopened.Close()
		t.Fatalf("reopening bypassed published immutability: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := published.ReadFile("binary", 128); err != nil || string(data) != "INERT_TEST_PAYLOAD" {
		t.Fatalf("stage close removed published bytes: %q,%v", data, err)
	}
}
