package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/zapret"
)

// TestConnectInstallsTheBundleWhenThereIsNone: the product is "paste a link,
// press one button". A first connect that finds no bypass installed must fetch
// one rather than quietly tunnelling YouTube and Discord at full round-trip
// latency — the exact cost the bypass exists to avoid.
func TestConnectInstallsTheBundleWhenThereIsNone(t *testing.T) {
	d, _ := newTestDaemon(t)
	dir := filepath.Join(d.store.Dir(), zapretDirName)

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{Version: "1.10.1", ArchiveURL: "https://example.invalid/b.zip"}, nil
	}
	installed := 0
	d.zapretApply = func(_ context.Context, target string, rel zapret.Release) error {
		installed++
		if err := os.MkdirAll(filepath.Join(target, "bin"), 0o755); err != nil {
			return err
		}
		for _, f := range []string{"general.bat", filepath.Join("bin", "winws.exe")} {
			if err := os.WriteFile(filepath.Join(target, f), []byte("x"), 0o644); err != nil {
				return err
			}
		}
		return zapret.WriteVersion(target, rel.Version)
	}

	d.installZapretIfMissing(context.Background())

	if installed != 1 {
		t.Fatalf("bundle installed %d times, want 1", installed)
	}
	if got := zapret.Version(dir); got != "1.10.1" {
		t.Errorf("installed version = %q, want 1.10.1", got)
	}
}

// TestConnectDoesNotRefetchAnInstalledBundle: a bundle already on disk is the
// user's, possibly one they dragged in themselves. Replacing it on every connect
// would undo their choice and pay for a download nobody asked for.
func TestConnectDoesNotRefetchAnInstalledBundle(t *testing.T) {
	d, _ := newTestDaemon(t)
	seedBundle(t, d, "1.9.9")

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		t.Error("checked for a release with a bundle already installed")
		return zapret.Release{}, errors.New("should not be called")
	}

	d.installZapretIfMissing(context.Background())
}

// TestConnectHonoursTheAutoUpdateChoice: switching automatic bundle updates off
// is a statement about this app reaching the network on its own — no release
// feed, no download, on a first connect as much as on the twelve-hour re-check.
//
// It is not a statement about the bypass. The copy compiled into the build costs
// no request, needs no update and is the release this client was built and
// tested against, so a first connect still installs it. Reading the switch as
// "no bypass at all" turns a user who wanted fewer downloads into a user whose
// YouTube quietly goes through the exit node — and leaves them nothing on screen
// to explain why.
func TestConnectHonoursTheAutoUpdateChoice(t *testing.T) {
	d, _ := newTestDaemon(t)
	dir := filepath.Join(d.store.Dir(), zapretDirName)
	d.zapretAutoUpdate = false

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		t.Error("fetched a bundle with automatic updates turned off")
		return zapret.Release{}, errors.New("should not be called")
	}
	embedded := 0
	d.zapretEmbed = func(target string) ([]zapret.Strategy, error) {
		embedded++
		return seedBundleAt(t, target, "1.10.2"), nil
	}

	d.installZapretIfMissing(context.Background())

	if embedded != 1 {
		t.Fatalf("the embedded bundle installed %d times, want 1", embedded)
	}
	if got := zapret.Version(dir); got != "1.10.2" {
		t.Errorf("installed version = %q, want the embedded 1.10.2", got)
	}
	// The status the UI reads has to move with the disk here too: a bypass that
	// installed itself and a bypass that never arrived look the same on a screen
	// still reporting no version.
	d.mu.Lock()
	reported := d.state.ZapretVersion
	d.mu.Unlock()
	if reported != "1.10.2" {
		t.Errorf("reported version = %q, want 1.10.2", reported)
	}
}

// TestConnectSurvivesAFailedInstall: no bypass is a slower session, not a failed
// one. The connect must go on and the log must say what happened, rather than the
// user meeting a button that appears to do nothing.
//
// This is the one path where there is genuinely no bypass left: the download
// failed AND the embedded copy would not unpack. Both have to be on the log —
// the second line is the only thing that separates "the network was down" from
// "the network was down and the floor under it gave way too".
func TestConnectSurvivesAFailedInstall(t *testing.T) {
	d, _ := newTestDaemon(t)
	var logs []string
	d.SetEmitter(func(name string, body any) {
		if name == "log" {
			if ev, ok := body.(LogEvent); ok {
				logs = append(logs, ev.Msg)
			}
		}
	})

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{}, errors.New("release feed unreachable")
	}

	d.installZapretIfMissing(context.Background())

	for _, want := range []string{"не удалось скачать сборку", "туннель работает без обхода"} {
		found := false
		for _, l := range logs {
			if strings.Contains(l, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("a failed install never said %q on the log channel: %v", want, logs)
		}
	}
}

// TestASuccessfulDownloadIsNotOverwrittenByTheEmbeddedBundle: the compiled-in
// copy is the floor, and a floor that also lands on top of what it was holding up
// would downgrade every fresh install to the version this binary was cut with.
func TestASuccessfulDownloadIsNotOverwrittenByTheEmbeddedBundle(t *testing.T) {
	d, _ := newTestDaemon(t)
	dir := filepath.Join(d.store.Dir(), zapretDirName)

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{Version: "1.10.1", ArchiveURL: "https://github.com/x/b.zip"}, nil
	}
	d.zapretApply = func(_ context.Context, target string, rel zapret.Release) error {
		if err := os.MkdirAll(filepath.Join(target, "bin"), 0o755); err != nil {
			return err
		}
		for _, f := range []string{"general.bat", filepath.Join("bin", "winws.exe")} {
			if err := os.WriteFile(filepath.Join(target, f), []byte("x"), 0o644); err != nil {
				return err
			}
		}
		return zapret.WriteVersion(target, rel.Version)
	}
	d.zapretEmbed = func(string) ([]zapret.Strategy, error) {
		t.Error("laid the embedded floor over a bundle that had just installed")
		return nil, errors.New("should not be called")
	}

	d.installZapretIfMissing(context.Background())

	if got := zapret.Version(dir); got != "1.10.1" {
		t.Fatalf("installed version = %q, want the downloaded 1.10.1", got)
	}
}

// TestTheEmbeddedInstallHoldsTheBypassLock: unpacking the compiled-in bundle
// stages into dir+".new" and swaps it into place — the same two paths
// zapret.Apply uses — so it has to be serialized against every other bypass
// operation exactly the way updateZapret is.
//
// Without the lock the window is real and reachable: installZapretIfMissing gets
// here just after updateZapret handed the lock back, and RunZapretAutoUpdate
// fires 45 seconds after the daemon starts (or the user presses Update). Both
// then run os.RemoveAll on the same staging directory and swap it into the same
// place, which ends as a failed install with an incoherent message, a staging
// directory deleted from under a live unpack, or a bundle directory holding files
// of two releases under one version stamp.
//
// It is a race on the file system rather than on memory, so -race never sees it
// and no amount of repetition reproduces it on demand. The lock itself is the
// thing that can be checked, so that is what this checks.
func TestTheEmbeddedInstallHoldsTheBypassLock(t *testing.T) {
	d, _ := newTestDaemon(t)
	dir := filepath.Join(d.store.Dir(), zapretDirName)

	inside := make(chan struct{})
	release := make(chan struct{})
	d.zapretEmbed = func(target string) ([]zapret.Strategy, error) {
		close(inside)
		<-release
		return []zapret.Strategy{{Name: "general", Path: filepath.Join(target, "general.bat")}}, nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.installEmbeddedZapretIfMissing(dir)
	}()

	<-inside
	held := !d.zapretOpMu.TryLock()
	if !held {
		d.zapretOpMu.Unlock()
	}
	close(release)
	<-done
	if !held {
		t.Fatal("the bypass lock was free while the embedded bundle was unpacking — an update could swap the same staging directory")
	}

	// And handed back: a lock still held here deadlocks the connect that follows.
	if !d.zapretOpMu.TryLock() {
		t.Fatal("the embedded install never released the bypass lock")
	}
	d.zapretOpMu.Unlock()
}
