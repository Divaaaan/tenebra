//go:build linux

package securefs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func socketFixture(t *testing.T) (*Root, string) {
	t.Helper()
	base := os.Getenv("TENEBRA_SECUREFS_TEST_ROOT")
	if base == "" {
		base = "/tmp"
	}
	if !filepath.IsAbs(base) {
		t.Fatal("socket fixture base must be absolute")
	}
	// /tmp is a shared sticky ancestor, never a private leaf. MkdirTemp creates
	// a new0700 child; OpenRoot verifies that child AND all its ancestors.
	directory, err := os.MkdirTemp(base, "s-")
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(directory, Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
			return
		}
		if !strings.HasPrefix(directory, filepath.Clean(base)+string(filepath.Separator)+"s-") {
			t.Fatal("fixture cleanup escaped its root")
		}
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	return root, directory
}
func bindSocketFixture(t *testing.T, root *Root, name string) *SocketLease {
	t.Helper()
	socket, err := root.BindPrivateSocket(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := socket.Close(); err != nil {
			t.Error(err)
		}
	})
	return socket
}

func TestPrivateSocketPublishRetainsRootAndRealListener(t *testing.T) {
	root, directory := socketFixture(t)
	socket := bindSocketFixture(t, root, "temp.sock")
	identity := socket.Identity()
	if !identity.valid() {
		t.Fatal("socket lacked device/inode/persistent pin identity")
	}
	if err := root.Close(); !errors.Is(err, ErrBusy) {
		t.Fatal("live socket did not retain root", err)
	}
	if lease, err := root.LeaseTree(context.Background(), Limits{MaxFiles: 16, MaxBytes: 4096}, nil); !errors.Is(err, ErrBusy) || lease != nil {
		t.Fatal("live IPC entered file staging", err)
	}
	if state, err := socket.Publish(context.Background(), "final.sock"); err != nil || state != Committed {
		t.Fatal(state, err)
	}
	if socket.Identity() != identity {
		t.Fatal("pinned identity changed through rename")
	}
	if _, err := os.Lstat(filepath.Join(directory, "temp.sock")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("temp socket remained after exclusive publish", err)
	}
	echo := make(chan error, 1)
	go func() {
		conn, err := socket.Accept()
		if err == nil {
			_, err = conn.Write([]byte("x"))
			conn.Close()
		}
		echo <- err
	}()
	conn, err := net.DialTimeout("unix", filepath.Join(directory, "final.sock"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(time.Second))
	var value [1]byte
	_, err = io.ReadFull(conn, value[:])
	conn.Close()
	if err != nil || value[0] != 'x' {
		t.Fatal("published node lost its actual listener", err)
	}
	if err := <-echo; err != nil {
		t.Fatal(err)
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "final.sock")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("owned node outlived close", err)
	}
}
func TestPrivateSocketNeverReplacesUnknownTarget(t *testing.T) {
	root, _ := socketFixture(t)
	socket := bindSocketFixture(t, root, "temp.sock")
	foreign := []byte("SYNTHETIC_FOREIGN_KEEP")
	if state, err := root.ReplaceFile("foreign.sock", foreign); err != nil || state != Committed {
		t.Fatal(state, err)
	}
	if state, err := socket.Publish(context.Background(), "foreign.sock"); state != Unchanged || !errors.Is(err, fs.ErrExist) {
		t.Fatal("unknown final node replaced", state, err)
	}
	after, err := root.ReadFile("foreign.sock", 1024)
	if err != nil || string(after) != string(foreign) {
		t.Fatal("foreign node changed", err)
	}
	if err := root.RemoveVerifiedSocket(context.Background(), "foreign.sock", socket.Identity(), func(context.Context) error { return nil }); !errors.Is(err, ErrUntrusted) {
		t.Fatal("regular file accepted as socket ownership", err)
	}
}
func TestPrivateSocketRecoveryRequiresEndedOwnerAndExactPin(t *testing.T) {
	root, directory := socketFixture(t)
	socket := bindSocketFixture(t, root, "temp.sock")
	if state, err := socket.Publish(context.Background(), "final.sock"); err != nil || state != Committed {
		t.Fatal(state, err)
	}
	// Model the dead listener's filesystem residue. The verifier is a unit
	// stub; real kernel process lifetime is tested in installtx's owned child.
	if err := socket.listener.Close(); err != nil {
		t.Fatal(err)
	}
	blocked := errors.New("synthetic owner still alive")
	if err := root.RemoveVerifiedSocket(context.Background(), "final.sock", socket.Identity(), func(context.Context) error { return blocked }); !errors.Is(err, blocked) {
		t.Fatal("live/uncertain owner bypassed", err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "final.sock")); err != nil {
		t.Fatal("refused recovery removed node", err)
	}
	wrong := socket.Identity()
	wrong.Pin = "pin_" + strings.Repeat("f", 32)
	if wrong.Pin == socket.Identity().Pin {
		wrong.Pin = "pin_" + strings.Repeat("e", 32)
	}
	if err := root.RemoveVerifiedSocket(context.Background(), "final.sock", wrong, func(context.Context) error { return nil }); !errors.Is(err, ErrChanged) {
		t.Fatal("inode-only receipt ignored persistent pin", err)
	}
	calls := 0
	verify := func(ctx context.Context) error {
		calls++
		// Must run before ALL namespace/object locks, not inside a callback
		// that deadlocks the very owner it needs to inspect.
		lock, err := root.lockWriterNamed(ctx, ".socket-writer.lock")
		if err != nil {
			return err
		}
		return lock.Close()
	}
	if err := root.RemoveVerifiedSocket(context.Background(), "final.sock", socket.Identity(), verify); err != nil || calls != 1 {
		t.Fatal("confirmed exact recovery failed", err, calls)
	}
	if _, err := os.Lstat(filepath.Join(directory, "final.sock")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("known dead node remained", err)
	}
}
func TestPrivateSocketRefusesCancelledCreationAndSourcePolicy(t *testing.T) {
	root, directory := socketFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if socket, err := root.BindPrivateSocket(ctx, "cancelled.sock"); !errors.Is(err, context.Canceled) || socket != nil {
		t.Fatal("cancelled creation ran", err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "cancelled.sock")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("cancelled socket created", err)
	}
	operator, err := OpenRoot(directory, Policy{Mode: OperatorInput, Principal: root.state.actor}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer operator.Close()
	if socket, err := operator.BindPrivateSocket(context.Background(), "operator.sock"); !errors.Is(err, ErrUnsupported) || socket != nil {
		t.Fatal("source root created privileged IPC", err)
	}
	for _, name := range []string{"../foreign.sock", "dir/socket", "/absolute.sock", ".socket-writer.lock"} {
		if socket, err := root.BindPrivateSocket(context.Background(), name); !errors.Is(err, ErrUntrusted) || socket != nil {
			t.Fatal("arbitrary namespace accepted", name, err)
		}
	}
}
