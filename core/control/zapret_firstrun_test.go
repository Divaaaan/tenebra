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

// TestConnectLaysTheBundleWithoutTouchingTheNetwork: the product is "paste a
// link, press one button". A first connect that finds no bypass installed must
// end with one rather than quietly tunnelling YouTube and Discord at full
// round-trip latency — the exact cost the bypass exists to avoid.
//
// It must do it from the bytes compiled into this binary and from nowhere else.
// Reaching upstream from here is what made the first press look broken: the
// download had a sixty-second budget, the settle for winws came after it, and the
// desktop bridge abandons a request at sixty — so the button reported failure
// while the daemon connected anyway, and the second press worked because by then
// the bundle was on disk. Keeping the network off this path is the fix, and
// RunZapretAutoUpdate is where the newer release comes from instead.
func TestConnectLaysTheBundleWithoutTouchingTheNetwork(t *testing.T) {
	d, _ := newTestDaemon(t)
	// No winws and no cmd.exe: what these check is the bundle on disk, not the
	// packet filter.
	stubStarts(d)
	dir := filepath.Join(d.store.Dir(), zapretDirName)

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		t.Error("a connect asked GitHub for a release")
		return zapret.Release{}, errors.New("should not be called")
	}
	d.zapretApply = func(context.Context, string, zapret.Release) error {
		t.Error("a connect downloaded a bundle")
		return errors.New("should not be called")
	}
	embedded := 0
	d.zapretEmbed = func(target string) ([]zapret.Strategy, error) {
		embedded++
		return seedBundleAt(t, target, "1.10.2"), nil
	}

	d.raiseZapretForConnect(context.Background())

	if embedded != 1 {
		t.Fatalf("the embedded bundle installed %d times, want 1", embedded)
	}
	if got := zapret.Version(dir); got != "1.10.2" {
		t.Errorf("installed version = %q, want the embedded 1.10.2", got)
	}
}

// TestConnectDoesNotReplaceAnInstalledBundle: a bundle already on disk is the
// user's, possibly one they dragged in themselves or one the updater fetched and
// newer than this binary's. Laying the compiled-in copy over it on every connect
// would be a silent rollback, and asking upstream about it would pay for a
// request nobody is waiting on.
func TestConnectDoesNotReplaceAnInstalledBundle(t *testing.T) {
	d, _ := newTestDaemon(t)
	// No winws and no cmd.exe: what these check is the bundle on disk, not the
	// packet filter.
	stubStarts(d)
	dir := seedBundle(t, d, "1.99.0")
	marker := filepath.Join(dir, "seeded.txt")
	if err := os.WriteFile(marker, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		t.Error("checked for a release with a bundle already installed")
		return zapret.Release{}, errors.New("should not be called")
	}
	d.zapretEmbed = func(string) ([]zapret.Strategy, error) {
		t.Error("laid the embedded floor over a bundle that was already there")
		return nil, errors.New("should not be called")
	}

	d.raiseZapretForConnect(context.Background())

	if got := zapret.Version(dir); got != "1.99.0" {
		t.Errorf("installed version = %q, want the untouched 1.99.0", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the installed bundle was replaced: %v", err)
	}
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
	// No winws and no cmd.exe: what these check is the bundle on disk, not the
	// packet filter.
	stubStarts(d)
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

	d.installEmbeddedZapretIfMissing(context.Background(), dir)

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
// This is the path where there is genuinely no bypass left: the floor under
// everything else would not unpack. It is the one case that has to be on the log,
// because it is the only one where the app has run out of ways to get a bundle.
func TestConnectSurvivesAFailedInstall(t *testing.T) {
	d, _ := newTestDaemon(t)
	// No winws and no cmd.exe: what these check is the bundle on disk, not the
	// packet filter.
	stubStarts(d)
	var logs []string
	d.SetEmitter(func(name string, body any) {
		if name == "log" {
			if ev, ok := body.(LogEvent); ok {
				logs = append(logs, ev.Msg)
			}
		}
	})

	d.zapretEmbed = func(string) ([]zapret.Strategy, error) {
		return nil, errors.New("архив повреждён")
	}

	d.raiseZapretForConnect(context.Background())

	found := false
	for _, l := range logs {
		if strings.Contains(l, "туннель работает без обхода") {
			found = true
		}
	}
	if !found {
		t.Errorf("a failed install never said the tunnel is on its own: %v", logs)
	}
}

// TestTheEmbeddedInstallHoldsTheBypassLock: unpacking the compiled-in bundle
// stages into dir+".new" and swaps it into place — the same two paths
// zapret.Apply uses — so it has to be serialized against every other bypass
// operation exactly the way updateZapret is.
//
// Without the lock the window is real and reachable: a connect gets here while
// RunZapretAutoUpdate, which fires 45 seconds after the daemon starts (or when
// the user presses Update), is mid-install. Both then run os.RemoveAll on the
// same staging directory and swap it into the same place, which ends as a failed
// install with an incoherent message, a staging directory deleted from under a
// live unpack, or a bundle directory holding files of two releases under one
// version stamp.
//
// It is a race on the file system rather than on memory, so -race never sees it
// and no amount of repetition reproduces it on demand. The lock itself is the
// thing that can be checked, so that is what this checks.
func TestTheEmbeddedInstallHoldsTheBypassLock(t *testing.T) {
	d, _ := newTestDaemon(t)
	// No winws and no cmd.exe: what these check is the bundle on disk, not the
	// packet filter.
	stubStarts(d)
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
		d.installEmbeddedZapretIfMissing(context.Background(), dir)
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
