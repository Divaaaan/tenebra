package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/zapret"
)

// seedBundle lays out a minimal installed bundle inside the daemon's store and
// stamps it with a version.
func seedBundle(t *testing.T, d *Daemon, version string) string {
	t.Helper()
	dir := filepath.Join(d.store.Dir(), zapretDirName)
	seedBundleAt(t, dir, version)
	return dir
}

// seedBundleAt lays the same bundle out in an arbitrary directory and reports
// the strategies it left there, which is what a stubbed installer has to return.
// The daemon judges an install by what is on disk afterwards, never by where the
// bytes came from, so a stub that writes the files is indistinguishable from the
// real unpack — and four megabytes cheaper.
func seedBundleAt(t *testing.T, dir, version string) []zapret.Strategy {
	t.Helper()
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
	return zapret.Discover(dir, dirFileNames(dir))
}

// A newer PINNED bundle is installed and reported. The version has to reach the
// status too: a stale bypass fails exactly like a dead node, so "which version am
// I running" is the one question that separates the two. Both versions are real
// pins (1.10.0 → 1.10.1) — under Variant A the trust gate only lets a pinned
// version through to install at all.
func TestUpdateZapretInstallsANewerBundle(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.SetSettings(settingsAt(t, t.TempDir()))
	dir := seedBundle(t, d, "1.10.0")

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{Version: "1.10.1", ArchiveURL: "https://example.invalid/b.zip"}, nil
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
	if !updated || from != "1.10.0" || to != "1.10.1" {
		t.Fatalf("update reported (%q → %q, updated=%v), want 1.10.0 → 1.10.1", from, to, updated)
	}
	if applied != 1 {
		t.Errorf("apply ran %d times, want once", applied)
	}
	if got := d.snapshotState().ZapretVersion; got != "1.10.1" {
		t.Errorf("status reports bypass version %q, want 1.10.1", got)
	}
}

// Variant A at the control layer: upstream published something newer than any
// pin this build carries. It must be reported (so the user learns to update
// Tenebra) but never installed, and the running bypass must not even be stopped
// for it — the whole cost of the design is a bundle that trails by one client
// release, in exchange for never installing code as SYSTEM on the strength of a
// checksum that came down the same connection as the archive. 1.99.0 is used so
// the test stays unpinned however the pin table grows.
func TestUpdateZapretReportsButDoesNotInstallAnUnpinnedVersion(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.SetSettings(settingsAt(t, t.TempDir()))
	dir := seedBundle(t, d, "1.10.1")

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{Version: "1.99.0", ArchiveURL: "https://example.invalid/b.zip"}, nil
	}
	d.zapretApply = func(context.Context, string, zapret.Release) error {
		t.Fatal("an unpinned version was installed automatically")
		return nil
	}

	from, to, updated, err := d.updateZapret(context.Background())
	if !errors.Is(err, zapret.ErrUntrustedVersion) {
		t.Fatalf("updateZapret error = %v, want ErrUntrustedVersion", err)
	}
	if updated {
		t.Error("reported an update that did not happen")
	}
	if from != "1.10.1" || to != "1.99.0" {
		t.Errorf("reported (%q → %q), want installed 1.10.1 and available 1.99.0", from, to)
	}
	// The bundle on disk must be exactly the one that was there — unpinned means
	// nothing was fetched, staged or swapped.
	if got := zapret.Version(dir); got != "1.10.1" {
		t.Errorf("installed bundle changed to %q, want the untouched 1.10.1", got)
	}
}

// The manual "check for updates" button must not turn "there is a newer bundle,
// update Tenebra" into a red failure. It comes back as a normal (ok) result
// flagged blocked and carrying the version that is available, so the screen can
// name it rather than show an error the user cannot act on by retrying.
func TestManualUpdateReportsAnUnpinnedVersionAsBlocked(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.SetSettings(settingsAt(t, t.TempDir()))
	seedBundle(t, d, "1.10.1")

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{Version: "1.99.0", ArchiveURL: "https://example.invalid/b.zip"}, nil
	}
	d.zapretApply = func(context.Context, string, zapret.Release) error {
		t.Fatal("the manual check installed an unpinned version")
		return nil
	}

	resp := d.handleUpdateZapret(context.Background(), Request{ID: 7, Cmd: CmdUpdateZapret})
	if !resp.Ok || resp.Error != "" {
		t.Fatalf("manual update returned a failure for an unpinned version: ok=%v err=%q", resp.Ok, resp.Error)
	}
	var out struct {
		Installed string `json:"installed"`
		Latest    string `json:"latest"`
		Updated   bool   `json:"updated"`
		Blocked   bool   `json:"blocked"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !out.Blocked {
		t.Error("an unpinned version was not flagged blocked")
	}
	if out.Updated {
		t.Error("an unpinned version was reported as updated")
	}
	if out.Latest != "1.99.0" || out.Installed != "1.10.1" {
		t.Errorf("reported installed=%q latest=%q, want 1.10.1 and 1.99.0", out.Installed, out.Latest)
	}
}

// A version newer than any pin is neither a failure nor a tamper alarm: it is
// "there is an update, get it by updating Tenebra". It goes out quietly at info
// level, naming the version, and never borrows the integrity wording — an error
// on every new upstream release is how the real alarm gets scrolled past.
func TestAnUnpinnedVersionIsReportedQuietly(t *testing.T) {
	d, _ := newTestDaemon(t)
	events := captureLogs(t, d)

	d.reportZapretUpdateOutcome("1.10.1", "1.99.0", zapret.ErrUntrustedVersion)

	ev, ok := loudest(*events, "1.99.0")
	if !ok {
		t.Fatalf("an available new version was not reported: %v", *events)
	}
	if ev.Level != LogInfo {
		t.Errorf("an available new version logged at %q, want %q", ev.Level, LogInfo)
	}
	if strings.Contains(ev.Msg, "целостност") {
		t.Errorf("an ordinary new version borrowed the integrity alarm: %v", ev.Msg)
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
	resp := h.await()
	var st State
	h.dataInto(resp, &st)
	if st.ZapretAutoUpdate {
		t.Fatal("disarming was not reflected in the response")
	}
	// And it has to be on the wire as a written false, not as an absence. Every
	// other bool here is omitted when off and read back as off, which works
	// because off is their default; this one defaults ON, so a client meeting an
	// absent field reads the user's "off" as "on" and springs the switch back at
	// the next status — over a core that stored the choice correctly.
	if !bytes.Contains(resp.Data, []byte(`"zapret_auto_update":false`)) {
		t.Errorf("disarmed auto-update is not on the wire: %s", resp.Data)
	}

	h2 := newHarness(t)
	h2.daemon.SetSettings(settingsAt(t, dir))
	if h2.daemon.snapshotState().ZapretAutoUpdate {
		t.Error("the pinned-version choice did not survive the restart")
	}
}

// captureLogs collects everything the daemon puts on its log channel.
func captureLogs(t *testing.T, d *Daemon) *[]LogEvent {
	t.Helper()
	var events []LogEvent
	d.SetEmitter(func(name string, body any) {
		if name != "log" {
			return
		}
		if ev, ok := body.(LogEvent); ok {
			events = append(events, ev)
		}
	})
	return &events
}

// loudest returns the highest-severity log line mentioning want, and whether one
// was found at all.
func loudest(events []LogEvent, want string) (LogEvent, bool) {
	rank := map[string]int{LogInfo: 0, LogWarn: 1, LogError: 2}
	var best LogEvent
	found := false
	for _, ev := range events {
		if !strings.Contains(ev.Msg, want) {
			continue
		}
		if !found || rank[ev.Level] > rank[best.Level] {
			best, found = ev, true
		}
	}
	return best, found
}

// An unreachable release feed is an ordinary event: the network is down, GitHub
// is blocked, and the retry is in twelve hours. An archive that arrived and did
// not match its checksum is the opposite — something rewrote code that this
// machine was about to run as LocalSystem — and logging the two at the same
// volume trains the user to scroll past the one that matters.
func TestAFailedVerificationIsLouderThanAFailedCheck(t *testing.T) {
	d, _ := newTestDaemon(t)
	events := captureLogs(t, d)

	d.reportZapretUpdateFailure(errors.New("dial tcp: no route to host"), "1.10.1")
	if ev, ok := loudest(*events, "no route to host"); !ok || ev.Level != LogInfo {
		t.Errorf("an unreachable feed logged as %+v, want an info line", ev)
	}

	*events = nil
	d.reportZapretUpdateFailure(fmt.Errorf("%w: контрольная сумма не совпала", zapret.ErrIntegrity), "1.10.1")
	ev, ok := loudest(*events, "целостност")
	if !ok {
		t.Fatalf("a failed integrity check said nothing about integrity: %v", *events)
	}
	if ev.Level != LogError {
		t.Errorf("a failed integrity check logged at %q, want %q", ev.Level, LogError)
	}
	// An update that was refused genuinely does leave the working bundle running,
	// and that is the reassurance the line carries.
	if !strings.Contains(ev.Msg, "осталась прежняя") {
		t.Errorf("a refused update did not say the old bundle is still there: %q", ev.Msg)
	}
}

// The background install runs unattended and its failures are ordinarily
// shrugged off — the tunnel works without a bypass. A refusal to install
// something that did not verify is not in that category and must not be filed
// under "could not download".
//
// This is the first-install case specifically: nothing is on disk yet, so the
// line must not borrow the update wording about a previous bundle being kept.
func TestAFailedVerificationOnAFirstInstallIsSaidOutLoud(t *testing.T) {
	d, _ := newTestDaemon(t)
	events := captureLogs(t, d)

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{}, fmt.Errorf("%w: контрольная сумма не совпала", zapret.ErrIntegrity)
	}
	d.zapretApply = func(context.Context, string, zapret.Release) error {
		t.Fatal("a release that failed verification was still installed")
		return nil
	}

	if from, to, _, err := d.updateZapret(context.Background()); err != nil {
		d.reportZapretUpdateOutcome(from, to, err)
	}

	ev, ok := loudest(*events, "целостност")
	if !ok {
		t.Fatalf("a refused bundle said nothing about integrity: %v", *events)
	}
	if ev.Level != LogError {
		t.Errorf("a refused bundle logged at %q, want %q", ev.Level, LogError)
	}
	// There was no bundle here to keep. Borrowing the update wording would tell a
	// fresh install that a previous bypass is still running, and the very next
	// line — the embedded floor going in — contradicts it.
	if strings.Contains(ev.Msg, "осталась прежняя") {
		t.Errorf("a first install claimed a previous bundle was kept: %q", ev.Msg)
	}
}
