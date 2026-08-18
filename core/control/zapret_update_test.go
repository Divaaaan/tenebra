package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Divaaaan/tenebra/core/zapret"
)

// seedBundle lays out a minimal installed bundle inside the daemon's store and
// stamps it with a version.
func seedBundle(t *testing.T, d *Daemon, version string) string {
	t.Helper()
	dir := filepath.Join(d.store.Dir(), zapretDirName)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"general.bat", filepath.Join("bin", "winws.exe")} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if version != "" {
		if err := zapret.WriteVersion(dir, version); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A newer published bundle is installed and reported. The version has to reach
// the status too: a stale bypass fails exactly like a dead node, so "which
// version am I running" is the one question that separates the two.
func TestUpdateZapretInstallsANewerBundle(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.SetSettings(settingsAt(t, t.TempDir()))
	dir := seedBundle(t, d, "1.10.1")

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{Version: "1.11.0", ArchiveURL: "https://example.invalid/b.zip"}, nil
	}
	applied := 0
	d.zapretApply = func(_ context.Context, target string, rel zapret.Release) error {
		applied++
		if target != dir {
			t.Errorf("update targeted %q, want the installed bundle at %q", target, dir)
		}
		return zapret.WriteVersion(target, rel.Version)
	}

	from, to, updated, err := d.updateZapret(context.Background())
	if err != nil {
		t.Fatalf("updateZapret: %v", err)
	}
	if !updated || from != "1.10.1" || to != "1.11.0" {
		t.Fatalf("update reported (%q → %q, updated=%v), want 1.10.1 → 1.11.0", from, to, updated)
	}
	if applied != 1 {
		t.Errorf("apply ran %d times, want once", applied)
	}
	if got := d.snapshotState().ZapretVersion; got != "1.11.0" {
		t.Errorf("status reports bypass version %q, want 1.11.0", got)
	}
}

// Nothing newer published means nothing happens: replacing a bundle costs the
// user their bypass for the seconds the filter is down, and paying that for an
// identical version is pure loss.
func TestUpdateZapretSkipsWhenCurrent(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.SetSettings(settingsAt(t, t.TempDir()))
	seedBundle(t, d, "1.11.0")

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{Version: "1.11.0", ArchiveURL: "https://example.invalid/b.zip"}, nil
	}
	d.zapretApply = func(context.Context, string, zapret.Release) error {
		t.Fatal("the bundle was replaced although the installed version is current")
		return nil
	}

	_, _, updated, err := d.updateZapret(context.Background())
	if err != nil {
		t.Fatalf("updateZapret: %v", err)
	}
	if updated {
		t.Error("reported an update that did not happen")
	}
}

// A failed check is an ordinary event — GitHub unreachable, no network — and
// must leave the installed bundle exactly as it was.
func TestUpdateZapretSurvivesAFailedCheck(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.SetSettings(settingsAt(t, t.TempDir()))
	seedBundle(t, d, "1.10.1")

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{}, errors.New("dial tcp: no route to host")
	}
	d.zapretApply = func(context.Context, string, zapret.Release) error {
		t.Fatal("a failed check still tried to install something")
		return nil
	}

	from, _, updated, err := d.updateZapret(context.Background())
	if err == nil {
		t.Fatal("a failed check reported success")
	}
	if updated {
		t.Error("reported an update after a failed check")
	}
	if from != "1.10.1" {
		t.Errorf("installed version reported as %q, want the untouched 1.10.1", from)
	}
}

// The toggle round-trips through the response, the status and the settings file,
// so a user who pins a version keeps it across a restart.
func TestZapretAutoUpdateTogglePersists(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	h.daemon.SetSettings(settingsAt(t, dir))

	// On by default: the bypass is the component that expires.
	if !h.daemon.snapshotState().ZapretAutoUpdate {
		t.Fatal("automatic bundle updates are not on by default")
	}

	h.send(Request{ID: 1, Cmd: CmdSetZapretAutoUpdate, On: false})
	var st State
	h.dataInto(h.await(), &st)
	if st.ZapretAutoUpdate {
		t.Fatal("disarming was not reflected in the response")
	}

	h2 := newHarness(t)
	h2.daemon.SetSettings(settingsAt(t, dir))
	if h2.daemon.snapshotState().ZapretAutoUpdate {
		t.Error("the pinned-version choice did not survive the restart")
	}
}
