//go:build linux

package securefs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func publicMetadataFixture(t *testing.T) (*Root, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("native machine publication requires isolated root fixture")
	}
	directory, err := os.MkdirTemp("", "tmmeta-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(directory, Policy{Mode: MachineContainer}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
		base := filepath.Clean(os.TempDir()) + string(filepath.Separator) + "tmmeta-"
		if !filepath.IsAbs(directory) || !strings.HasPrefix(directory, base) {
			t.Error("refusing unexpected fixture cleanup target")
			return
		}
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	return root, directory
}

func metadataDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

type metadataReadReplacement struct {
	nativeOps
	change    func() error
	afterOpen bool
	opened    handle
}

func (n *metadataReadReplacement) applyChange() error {
	change := n.change
	n.change = nil
	return change()
}

func (n *metadataReadReplacement) openAt(parent handle, name string, request openRequest) (handle, error) {
	h, err := n.nativeOps.openAt(parent, name, request)
	if err != nil || name != "journal.json" || n.change == nil {
		return h, err
	}
	n.opened = h
	if n.afterOpen {
		if err := n.applyChange(); err != nil {
			return 0, errors.Join(err, n.nativeOps.close(h))
		}
	}
	return h, nil
}

func (n *metadataReadReplacement) read(h handle, data []byte) (int, error) {
	count, err := n.nativeOps.read(h, data)
	if err == nil && count > 0 && h == n.opened && n.change != nil {
		err = n.applyChange()
	}
	return count, err
}

func TestPrivateMetadataConcurrentReplacement(t *testing.T) {
	tests := []struct {
		name                    string
		afterOpen, ordinaryRead bool
		change                  string
		want                    string
		wantError               error
	}{
		{name: "original_read_rejects_detached_inode", afterOpen: true, ordinaryRead: true, wantError: ErrUntrusted},
		{name: "atomic_reopens_after_open", afterOpen: true, want: "NEW"},
		{name: "atomic_reopens_during_read", want: "NEW"},
		{name: "hardlinked_replacement_refused", afterOpen: true, change: "hardlink", wantError: ErrUntrusted},
		{name: "detached_public_file_refused", afterOpen: true, change: "public", wantError: ErrUntrusted},
		{name: "missing_replacement_is_a_change", afterOpen: true, change: "remove", wantError: ErrChanged},
		{name: "in_place_write_is_a_change", change: "overwrite", wantError: ErrChanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ops, err := newNativeOps()
			if err != nil {
				t.Fatal(err)
			}
			injected := &metadataReadReplacement{nativeOps: ops, afterOpen: tt.afterOpen}
			directory := privateFixtureDir(t)
			root, err := openRootWithOps(directory, Policy{Mode: SameAccountPrivate}, false, injected)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			for name, value := range map[string]string{"journal.json": "OLD", "replacement": "NEW"} {
				if outcome, err := root.ReplaceFile(name, []byte(value)); err != nil || outcome != Committed {
					t.Fatal(outcome, err)
				}
			}
			old := filepath.Join(directory, "journal.json")
			replacement := filepath.Join(directory, "replacement")
			if tt.change == "hardlink" {
				if err := os.Link(replacement, filepath.Join(directory, "alias")); err != nil {
					t.Fatal(err)
				}
			}
			if tt.change == "public" {
				if err := os.Chmod(old, 0644); err != nil {
					t.Fatal(err)
				}
			}
			injected.change = func() error {
				switch tt.change {
				case "remove":
					return os.Remove(old)
				case "overwrite":
					return os.WriteFile(old, []byte("different length"), 0600)
				default:
					return os.Rename(replacement, old)
				}
			}
			var body []byte
			if tt.ordinaryRead {
				body, err = root.ReadFile("journal.json", 64)
			} else {
				body, err = root.ReadAtomicFile(context.Background(), "journal.json", 64)
			}
			if injected.change != nil || string(body) != tt.want || !errors.Is(err, tt.wantError) {
				t.Fatalf("read returned %q, %v, want %q, %v", body, err, tt.want, tt.wantError)
			}
		})
	}
}

func TestMachineMetadataNativePublicReadAndPrivateTemplate(t *testing.T) {
	root, directory := publicMetadataFixture(t)
	for _, mode := range []uint32{0644, 0600} {
		name := "service-status.json"
		if mode == 0600 {
			name = "tenebra.service"
		}
		body := []byte("{\"phase\":\"held\"}\n")
		state, err := root.PublishMachineMetadata(context.Background(), name, body, "", mode)
		if err != nil || state != Committed {
			t.Fatal(state, err)
		}
		path := filepath.Join(directory, name)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != os.FileMode(mode) || info.Mode()&0111 != 0 {
			t.Fatal("new metadata mode mismatch", info, err)
		}
		command := exec.Command("/bin/cat", path)
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{}}}
		got, err := command.CombinedOutput()
		if mode == 0644 && (err != nil || !bytes.Equal(got, body)) {
			t.Fatal("actual unprivileged reader could not read projection", err)
		}
		if mode == 0600 && err == nil {
			t.Fatal("unprivileged reader read private template")
		}
	}
	if _, err := root.ReplaceFile("ordinary.json", []byte("x")); !errors.Is(err, ErrUnsupported) {
		t.Fatal("container became a general private writer", err)
	}
	if _, err := root.ReplaceStream(context.Background(), "executable", bytes.NewReader([]byte("x")), 1, 0644); !errors.Is(err, ErrUnsupported) {
		t.Fatal("private stream gained public mode", err)
	}
}

func TestMachineMetadataCreateOnlyAndConcurrentDigestCAS(t *testing.T) {
	root, directory := publicMetadataFixture(t)
	ctx := context.Background()
	first := []byte("{\"generation\":1}\n")
	if state, err := root.PublishMachineMetadata(ctx, "status.json", first, "", 0644); err != nil || state != Committed {
		t.Fatal(state, err)
	}
	if state, err := root.PublishMachineMetadata(ctx, "status.json", first, "", 0644); !errors.Is(err, ErrChanged) || state != Unchanged {
		t.Fatal("create-only overwrote existing metadata", state, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, next := range []string{"{\"generation\":2}\n", "{\"generation\":3}\n"} {
		wg.Add(1)
		go func(next string) {
			defer wg.Done()
			_, err := root.PublishMachineMetadata(ctx, "status.json", []byte(next), metadataDigest(first), 0644)
			results <- err
		}(next)
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrChanged) && !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal("metadata CAS had wrong winner count", winners)
	}
	got, err := os.ReadFile(filepath.Join(directory, "status.json"))
	if err != nil || string(got) != "{\"generation\":2}\n" && string(got) != "{\"generation\":3}\n" {
		t.Fatal("partial publication", err)
	}
	if state, err := root.PublishMachineMetadata(ctx, "status.json", first, metadataDigest(first), 0644); state != Unchanged || !errors.Is(err, ErrChanged) {
		t.Fatal("stale digest replaced new metadata", state, err)
	}
}

func TestMachineMetadataPreservesUntrustedTargets(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "directory", "foreign-owner", "writable", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			root, directory := publicMetadataFixture(t)
			path := filepath.Join(directory, "status.json")
			original := []byte("preserve-forensic-object")
			other := filepath.Join(directory, "other")
			if err := os.WriteFile(other, original, 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(other, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			default:
				if kind == "oversized" {
					original = bytes.Repeat([]byte("x"), machineMetadataLimit+1)
				}
				if err := os.WriteFile(path, original, 0644); err != nil {
					t.Fatal(err)
				}
				if kind == "foreign-owner" {
					if err := os.Chown(path, 65534, 65534); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "writable" {
					if err := os.Chmod(path, 0666); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			state, err := root.PublishMachineMetadata(context.Background(), "status.json", []byte("{\"new\":true}"), metadataDigest(original), 0644)
			if err == nil || state != Unchanged {
				t.Fatal("untrusted target was replaced", state, err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("forensic identity changed", err)
			}
			got, err := os.ReadFile(other)
			if err != nil || string(got) != "preserve-forensic-object" {
				t.Fatal("unrelated/source object changed", err)
			}
			if kind == "foreign-owner" || kind == "writable" || kind == "oversized" {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, original) {
					t.Fatal("forensic bytes changed", err)
				}
			}
		})
	}
}

func TestMachineMetadataRejectsWritableParentAndPrivateRoot(t *testing.T) {
	root, directory := publicMetadataFixture(t)
	if err := os.Chmod(directory, 0777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if state, err := root.PublishMachineMetadata(context.Background(), "status.json", []byte("{}"), "", 0644); state != Unchanged || !errors.Is(err, ErrUntrusted) {
		t.Fatal("sticky writable parent trusted for publication", state, err)
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	private, err := root.OpenDir("private", Policy{Mode: MachinePrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	if state, err := private.PublishMachineMetadata(context.Background(), "status.json", []byte("{}"), "", 0644); state != Unchanged || !errors.Is(err, ErrUnsupported) {
		t.Fatal("private root published public metadata", state, err)
	}
}

func TestMachineMetadataBoundsAndCancellationMutateNothing(t *testing.T) {
	root, directory := publicMetadataFixture(t)
	for _, input := range []struct {
		name   string
		body   []byte
		digest string
		mode   uint32
	}{
		{"../status.json", []byte("{}"), "", 0644},
		{".hidden", []byte("{}"), "", 0644},
		{"bad\nname", []byte("{}"), "", 0644},
		{"status.json", []byte("{}"), "", 0700},
		{"status.json", []byte("{}"), "", 0755},
		{"status.json", bytes.Repeat([]byte("x"), machineMetadataLimit+1), "", 0644},
		{"status.json", []byte{0xff}, "", 0644},
		{"status.json", []byte{0}, "", 0644},
		{"status.json", []byte("{}"), strings.Repeat("A", 64), 0644},
	} {
		if state, err := root.PublishMachineMetadata(context.Background(), input.name, input.body, input.digest, input.mode); err == nil || state != Unchanged {
			t.Fatal("malformed metadata accepted", state, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if state, err := root.PublishMachineMetadata(ctx, "status.json", []byte("{}"), "", 0644); state != Unchanged || !errors.Is(err, context.Canceled) {
		t.Fatal(state, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("rejected input changed namespace", entries, err)
	}
}

func TestMachineMetadataKernelLockDoesNotWaitOrPublish(t *testing.T) {
	root, directory := publicMetadataFixture(t)
	file, err := os.OpenFile(filepath.Join(directory, ".tenebra-machine-metadata.lock"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN)
	if state, err := root.PublishMachineMetadata(context.Background(), "status.json", []byte("{}"), "", 0644); state != Unchanged || !errors.Is(err, ErrBusy) {
		t.Fatal("contended metadata lock published or waited", state, err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "status.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("contended publication created target", err)
	}
}

type machinePublicationFault struct {
	nativeOps
	before, after bool
}

func (n *machinePublicationFault) rename(source, parent handle, name string, replace bool) error {
	if n.before {
		return errInjected
	}
	err := n.nativeOps.rename(source, parent, name, replace)
	if err == nil && n.after {
		return errInjected
	}
	return err
}

func (n *machinePublicationFault) replacePrivate(parent, temp handle, temporary, target string) (CommitState, error) {
	if n.before {
		return Unchanged, errInjected
	}
	state, err := n.nativeOps.replacePrivate(parent, temp, temporary, target)
	if err == nil && state == Committed && n.after {
		return Indeterminate, errInjected
	}
	return state, err
}

func TestMachineMetadataLostPublicationAckIsIndeterminate(t *testing.T) {
	for _, update := range []bool{false, true} {
		for _, after := range []bool{false, true} {
			label := "create-before"
			if update {
				label = "update-before"
			}
			if after {
				label = strings.TrimSuffix(label, "before") + "after"
			}
			t.Run(label, func(t *testing.T) {
				root, directory := publicMetadataFixture(t)
				old, newBody := []byte("old-forensic-metadata"), []byte("new-durable-metadata")
				digest := ""
				if update {
					if state, err := root.PublishMachineMetadata(context.Background(), "status.json", old, "", 0644); err != nil || state != Committed {
						t.Fatal(state, err)
					}
					digest = metadataDigest(old)
				}
				root.state.ops = &machinePublicationFault{nativeOps: root.state.ops, before: !after, after: after}
				state, err := root.PublishMachineMetadata(context.Background(), "status.json", newBody, digest, 0644)
				want := Unchanged
				if after {
					want = Indeterminate
				}
				if state != want || !errors.Is(err, errInjected) {
					t.Fatal("publication uncertainty was hidden", state, err)
				}
				body, readErr := os.ReadFile(filepath.Join(directory, "status.json"))
				if after && (readErr != nil || !bytes.Equal(body, newBody)) {
					t.Fatal("actual published bytes missing after lost ACK", readErr)
				}
				if !after && update && (readErr != nil || !bytes.Equal(body, old)) {
					t.Fatal("before-rename fault changed canonical bytes", readErr)
				}
				if !after && !update && !errors.Is(readErr, os.ErrNotExist) {
					t.Fatal("failed create published a target", readErr)
				}
			})
		}
	}
}
