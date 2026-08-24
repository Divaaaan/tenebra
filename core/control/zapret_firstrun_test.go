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
// is a statement that this app should not install bundles behind the user's
// back. Treating a first connect as an exception would make that switch a lie —
// and that holds for the embedded copy too, which is otherwise free: the switch
// is about whether this app manages the bundle, not about where the bytes come
// from.
func TestConnectHonoursTheAutoUpdateChoice(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.zapretAutoUpdate = false

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		t.Error("fetched a bundle with automatic updates turned off")
		return zapret.Release{}, errors.New("should not be called")
	}
	d.zapretEmbed = func(string) ([]zapret.Strategy, error) {
		t.Error("installed the embedded bundle with automatic updates turned off")
		return nil, errors.New("should not be called")
	}

	d.installZapretIfMissing(context.Background())
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
