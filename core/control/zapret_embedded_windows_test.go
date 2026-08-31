//go:build windows

package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Divaaaan/tenebra/core/zapret"
)

// withRealEmbeddedBundle puts the actual compiled-in installer back on a test
// daemon. newTestDaemon stubs it out so no ordinary connect test unpacks four
// megabytes and hands a real winws.exe to the runner; the tests below are the
// ones that are about those bytes, so they want the real thing.
func withRealEmbeddedBundle(d *Daemon) {
	d.zapretEmbed = zapret.InstallEmbedded
}

// TestTheUpdaterFallsBackToTheEmbeddedBundle walks every way an install from
// upstream can end with nothing installed. Each one used to leave the user with
// no bypass at all, and the third is not hypothetical: upstream published 1.10.2
// before this client pinned it, so every install made in between got the "there
// is a newer bundle, update Tenebra" notice and zero bypass behind it.
//
// The two calls below are what RunZapretAutoUpdate does, in its order: it lays
// the floor at start and checks upstream on its own cadence. Fetching used to
// hang off the connect instead, which is where its sixty-second budget outlived
// the desktop bridge's; the fallback itself is unchanged and still has to hold
// however the check ends.
func TestTheUpdaterFallsBackToTheEmbeddedBundle(t *testing.T) {
	pinned := zapret.Release{Version: "1.10.1", ArchiveURL: "https://github.com/x/b.zip"}

	cases := []struct {
		name   string
		latest func(context.Context) (zapret.Release, error)
		apply  func(context.Context, string, zapret.Release) error
		// says is a fragment the log has to carry, so each refusal stays
		// distinguishable from the others in a bug report.
		says string
	}{
		{
			// No network: the feed cannot even be asked.
			name: "the release feed is unreachable",
			latest: func(context.Context) (zapret.Release, error) {
				return zapret.Release{}, errors.New("dial tcp: no route to host")
			},
			says: "проверка обновления не удалась",
		},
		{
			// GitHub answers the API but the asset host does not, or the transfer
			// dies halfway: the feed was fine, the archive never arrived.
			name:   "the archive itself will not download",
			latest: func(context.Context) (zapret.Release, error) { return pinned, nil },
			apply: func(context.Context, string, zapret.Release) error {
				return errors.New("zapret: обрыв при скачивании: unexpected EOF")
			},
			says: "проверка обновления не удалась",
		},
		{
			// Upstream is ahead of every checksum this build carries. Nothing is
			// downloaded at all — the client will not run unpinned code as SYSTEM.
			name: "the published version carries no pin",
			latest: func(context.Context) (zapret.Release, error) {
				return zapret.Release{Version: "1.99.0", ArchiveURL: "https://github.com/x/b.zip"}, nil
			},
			apply: func(context.Context, string, zapret.Release) error {
				t.Error("downloaded a version this client does not pin")
				return nil
			},
			says: "новее вшитых в Tenebra проверок",
		},
		{
			// The archive arrived and was not what upstream published. Refusing it is
			// correct; leaving the machine bare afterwards was not.
			name:   "the downloaded archive fails its checksum",
			latest: func(context.Context) (zapret.Release, error) { return pinned, nil },
			apply: func(context.Context, string, zapret.Release) error {
				return fmt.Errorf("%w: контрольная сумма не совпала", zapret.ErrIntegrity)
			},
			says: "целостност",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _ := newTestDaemon(t)
			withRealEmbeddedBundle(d)
			events := captureLogs(t, d)
			dir := filepath.Join(d.store.Dir(), zapretDirName)

			d.zapretLatest = c.latest
			if c.apply != nil {
				d.zapretApply = c.apply
			}

			if from, to, _, err := d.updateZapret(context.Background()); err != nil {
				d.reportZapretUpdateOutcome(from, to, err)
			}
			d.installEmbeddedZapretIfMissing(context.Background(), dir)

			if got := zapret.Version(dir); got != zapret.EmbeddedVersion {
				t.Fatalf("installed version = %q, want the embedded %q", got, zapret.EmbeddedVersion)
			}
			if n := len(zapret.Discover(dir, dirFileNames(dir))); n == 0 {
				t.Fatal("the embedded fallback left no strategies to run")
			}
			if _, err := os.Stat(filepath.Join(dir, "bin", "winws.exe")); err != nil {
				t.Fatalf("the embedded fallback left no winws: %v", err)
			}
			// The status the UI reads has to move with the disk, or the user is told
			// there is no bypass while one sits installed.
			d.mu.Lock()
			reported := d.state.ZapretVersion
			d.mu.Unlock()
			if reported != zapret.EmbeddedVersion {
				t.Errorf("reported version = %q, want %q", reported, zapret.EmbeddedVersion)
			}

			// Both halves have to be on the log: what upstream refused, and what was
			// installed instead. Either one alone leaves a bug report unanswerable.
			if _, ok := loudest(*events, c.says); !ok {
				t.Errorf("nothing on the log named the refusal (%q): %v", c.says, *events)
			}
			if _, ok := loudest(*events, "вшитая сборка "+zapret.EmbeddedVersion); !ok {
				t.Errorf("nothing on the log said the embedded bundle went in: %v", *events)
			}
		})
	}
}

// TestTheEmbeddedBundleLeavesAnInstalledOneAlone: whatever is already on disk is
// the user's — dragged in by hand, or downloaded by this app and newer than this
// binary. Neither may be replaced by the copy compiled in here, in either
// direction: overwriting the older one throws away the lists and the strategy
// they settled on, and overwriting the newer one is a silent rollback of a bypass
// that works.
func TestTheEmbeddedBundleLeavesAnInstalledOneAlone(t *testing.T) {
	for _, version := range []string{"1.9.9", "1.99.0"} {
		t.Run(version, func(t *testing.T) {
			d, _ := newTestDaemon(t)
			withRealEmbeddedBundle(d)
			dir := seedBundle(t, d, version)
			marker := filepath.Join(dir, "seeded.txt")
			if err := os.WriteFile(marker, []byte("mine"), 0o644); err != nil {
				t.Fatal(err)
			}

			d.installEmbeddedZapretIfMissing(context.Background(), dir)

			if got := zapret.Version(dir); got != version {
				t.Fatalf("installed version = %q, want the untouched %q", got, version)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("the installed bundle was replaced: %v", err)
			}
		})
	}
}

// TestTheEmbeddedFloorIsNotRolledBackByAnOlderRelease: once the floor is down it
// is an ordinary installed bundle, stamped with its version like any other, so
// the updater compares against it instead of installing over it. An older pinned
// release has to lose that comparison — otherwise every check after a fallback
// walks the bypass backwards.
func TestTheEmbeddedFloorIsNotRolledBackByAnOlderRelease(t *testing.T) {
	d, _ := newTestDaemon(t)
	withRealEmbeddedBundle(d)
	dir := filepath.Join(d.store.Dir(), zapretDirName)

	d.installEmbeddedZapretIfMissing(context.Background(), dir)
	if got := zapret.Version(dir); got != zapret.EmbeddedVersion {
		t.Fatalf("the floor did not go in: version = %q", got)
	}

	// Now the network is back, and what it publishes is older than the floor.
	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{Version: "1.10.0", ArchiveURL: "https://github.com/x/b.zip"}, nil
	}
	d.zapretApply = func(context.Context, string, zapret.Release) error {
		t.Error("installed a release older than the embedded bundle already on disk")
		return nil
	}

	if _, _, updated, err := d.updateZapret(context.Background()); err != nil || updated {
		t.Fatalf("updateZapret = (updated %v, err %v), want (false, nil)", updated, err)
	}
	if got := zapret.Version(dir); got != zapret.EmbeddedVersion {
		t.Errorf("version after the check = %q, want the floor %q", got, zapret.EmbeddedVersion)
	}
}

// TestTheBypassArrivesWithoutConnecting is the regression test for the second
// half of the release that shipped the bypass unreachable.
//
// Installing the bundle used to hang off connecting, and nothing else. On a
// machine that had not connected since installing, the bundle directory did not
// exist — so every control that acts on the bypass answered "load a bypass bundle
// first", including the re-pick button, which is the one a user presses precisely
// when video has stopped working. The bytes are compiled into this binary and
// need neither network nor permission, so there is nothing to wait for.
//
// The daemon's own background job is where this belongs: it already owns keeping
// the bundle present and current, and it starts with the daemon.
func TestTheBypassArrivesWithoutConnecting(t *testing.T) {
	d, _ := newTestDaemon(t)
	withRealEmbeddedBundle(d)
	dir := filepath.Join(d.store.Dir(), zapretDirName)

	if got := len(zapret.Discover(dir, dirFileNames(dir))); got != 0 {
		t.Fatalf("a fresh daemon already has %d strategies; the test proves nothing", got)
	}

	// An already-cancelled context runs the install and then leaves the loop at its
	// first select, so the job is exercised without a timer or a goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.RunZapretAutoUpdate(ctx)

	if got := zapret.Version(dir); got != zapret.EmbeddedVersion {
		t.Fatalf("bundle version after start-up = %q, want the embedded %q", got, zapret.EmbeddedVersion)
	}
	if got := len(zapret.Discover(dir, dirFileNames(dir))); got == 0 {
		t.Fatal("no strategies on disk: the bypass controls would still answer \"load a bundle first\"")
	}

	// The command behind the settings screen's bundle list reads that directory,
	// and answering it is what the interface needs to show a bypass at all.
	if r := d.Handle(context.Background(), Request{ID: 1, Cmd: CmdListZapret}); !r.Ok {
		t.Fatalf("list_zapret after start-up: %s", r.Error)
	}
}
