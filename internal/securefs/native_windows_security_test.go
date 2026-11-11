//go:build windows

package securefs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

type observedNative struct {
	nativeOps
	reads      int
	beforeRead func()
}

func (n *observedNative) read(h handle, buffer []byte) (int, error) {
	n.reads++
	if n.beforeRead != nil {
		hook := n.beforeRead
		n.beforeRead = nil
		hook()
	}
	return n.nativeOps.read(h, buffer)
}
func nativeObservedRoot(t *testing.T) (*Root, *observedNative, string) {
	t.Helper()
	path := privateFixtureDir(t)
	ops, err := newNativeOps()
	if err != nil {
		t.Fatal(err)
	}
	observed := &observedNative{nativeOps: ops}
	root, err := openRootWithOps(path, Policy{Mode: SameAccountPrivate}, false, observed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	return root, observed, path
}

func TestNativeExplicitForeignWriteDeniedBeforePayloadRead(t *testing.T) {
	root, observed, path := nativeObservedRoot(t)
	if _, err := root.ReplaceFile("inert.txt", []byte("NEVER_CONSUME_UNTRUSTED_BYTES")); err != nil {
		t.Fatal(err)
	}
	actor, err := observed.principal()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + actor + ")(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(filepath.Join(path, "inert.txt"), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	observed.reads = 0
	if data, err := root.ReadFile("inert.txt", 128); !errors.Is(err, ErrUntrusted) || errors.Is(err, fs.ErrNotExist) || data != nil {
		t.Fatalf("unsafe explicit ACE was accepted: %v", err)
	}
	if observed.reads != 0 {
		t.Fatalf("unsafe payload reads=%d, want0", observed.reads)
	}
}

func TestNativeHardlinkDeniedBeforePayloadRead(t *testing.T) {
	root, observed, path := nativeObservedRoot(t)
	if _, err := root.ReplaceFile("one", []byte("SYNTHETIC_HARDLINK_BYTES")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(path, "one"), filepath.Join(path, "two")); err != nil {
		t.Fatal(err)
	}
	observed.reads = 0
	if _, err := root.ReadFile("one", 128); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("hardlink accepted: %v", err)
	}
	if observed.reads != 0 {
		t.Fatal("hardlink payload was read")
	}
}

func TestNativeReparseDeniedBeforePayloadRead(t *testing.T) {
	root, observed, path := nativeObservedRoot(t)
	if err := os.WriteFile(filepath.Join(path, "target"), []byte("SYNTHETIC_LINK_TARGET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(path, "target"), filepath.Join(path, "link")); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skip("native symlink gate NOT RUN: a disposable Windows stand with symlink permission is required")
		}
		t.Fatal(err)
	}
	observed.reads = 0
	if _, err := root.ReadFile("link", 128); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("reparse accepted: %v", err)
	}
	if observed.reads != 0 {
		t.Fatal("reparse target was read")
	}
}

func TestNativeReadUsesOpenedObjectAcrossNameSwap(t *testing.T) {
	root, observed, path := nativeObservedRoot(t)
	if _, err := root.ReplaceFile("payload", []byte("OLD_SAFE_BYTES")); err != nil {
		t.Fatal(err)
	}
	observed.beforeRead = func() {
		if err := os.Rename(filepath.Join(path, "payload"), filepath.Join(path, "original")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "payload"), []byte("REPLACEMENT_MUST_NOT_BE_READ"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := root.ReadFile("payload", 128)
	if err == nil && string(data) != "OLD_SAFE_BYTES" {
		t.Fatalf("name swap redirected object-bound read: %q", data)
	}
	if err != nil && !errors.Is(err, ErrChanged) {
		t.Fatalf("unexpected name-swap outcome: %v", err)
	}
}
