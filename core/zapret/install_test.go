package zapret

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bundleZip writes a zip that mirrors a real zapret release: everything under a
// single versioned folder, a couple of strategies, the service script, and the
// binary the bundle is useless without.
func bundleZip(t *testing.T, root string, extra map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bundle.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	members := map[string]string{
		"general.bat":       "@echo off\r\n",
		"general (ALT).bat": "@echo off\r\n",
		"service.bat":       "@echo off\r\n",
		"bin/winws.exe":     "MZ",
		"lists/list.txt":    "example.com\n",
	}
	for k, v := range extra {
		members[k] = v
	}
	for name, body := range members {
		full := name
		if root != "" {
			full = root + "/" + name
		}
		w, err := zw.Create(full)
		if err != nil {
			t.Fatalf("zip create %s: %v", full, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return path
}

func TestInstallStripsTheVersionedWrapper(t *testing.T) {
	src := bundleZip(t, "zapret-discord-youtube-1.10.1", nil)
	dir := filepath.Join(t.TempDir(), "zapret")

	got, err := Install(src, dir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Without stripping, every upgrade would nest the bundle one level deeper
	// and invalidate stored strategy paths.
	if _, err := os.Stat(filepath.Join(dir, "bin", "winws.exe")); err != nil {
		t.Fatalf("winws not at the expected place: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d strategies, want 2 (service.bat excluded): %+v", len(got), got)
	}
}

func TestInstallHandlesAFlatArchive(t *testing.T) {
	src := bundleZip(t, "", nil)
	dir := filepath.Join(t.TempDir(), "zapret")

	if _, err := Install(src, dir); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "general.bat")); err != nil {
		t.Fatalf("flat archive not unpacked as-is: %v", err)
	}
}

func TestInstallReplacesAPreviousBundle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "zapret")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "old-strategy.bat")
	if err := os.WriteFile(stale, []byte("@echo off"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := bundleZip(t, "zapret-1.11", nil)
	got, err := Install(src, dir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// A leftover strategy from an older bundle would be offered to the user and
	// launched against a winws it was never written for.
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale strategy from the previous bundle survived the re-import")
	}
	for _, s := range got {
		if s.Name == "old-strategy" {
			t.Error("stale strategy was offered")
		}
	}
}

// An archive is untrusted input; `../` in a member name is how one writes
// outside the target directory.
func TestInstallRefusesPathTraversal(t *testing.T) {
	src := bundleZip(t, "zapret", map[string]string{"../escaped.txt": "nope"})
	dir := filepath.Join(t.TempDir(), "zapret")

	if _, err := Install(src, dir); err == nil {
		t.Fatal("Install accepted an archive escaping its directory")
	}
}

func TestInstallRejectsSomethingThatIsNotZapret(t *testing.T) {
	// A zip of lists with no winws is a blocklist, not a bypass bundle; saying
	// so beats installing it and failing later at launch.
	path := filepath.Join(t.TempDir(), "lists.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("hosts.txt")
	_, _ = w.Write([]byte("ads.example\n"))
	_ = zw.Close()
	_ = f.Close()

	if _, err := Install(path, filepath.Join(t.TempDir(), "z")); err == nil {
		t.Fatal("Install accepted an archive with no strategies")
	}
}

// TestInstallDisablesTheBundlesOwnUpdater: the bundle ships an update checker
// that every strategy batch invokes on launch. On a censored network that is a
// GitHub request standing between the user and the bypass that would unblock
// GitHub, and it competes with this app's own updater over what version is
// installed. Importing a bundle by hand must switch it off exactly like
// downloading one does.
func TestInstallDisablesTheBundlesOwnUpdater(t *testing.T) {
	src := bundleZip(t, "zapret-discord-youtube-1.10.1", map[string]string{
		"utils/check_updates.enabled": "true",
	})
	dir := filepath.Join(t.TempDir(), "zapret")

	if _, err := Install(src, dir); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "utils", "check_updates.enabled")); !os.IsNotExist(err) {
		t.Fatalf("check_updates.enabled survived the import (err = %v)", err)
	}
}

// TestInstallDirDisablesTheBundlesOwnUpdater: same for the folder the user drops
// in already unpacked — the file arrives with the bundle either way.
func TestInstallDirDisablesTheBundlesOwnUpdater(t *testing.T) {
	src := t.TempDir()
	for name, body := range map[string]string{
		"general.bat":                 "@echo off\r\n",
		"bin/winws.exe":               "MZ",
		"utils/check_updates.enabled": "true",
	} {
		full := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	dir := filepath.Join(t.TempDir(), "zapret")

	if _, err := InstallDir(src, dir); err != nil {
		t.Fatalf("InstallDir: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "utils", "check_updates.enabled")); !os.IsNotExist(err) {
		t.Fatalf("check_updates.enabled survived the import (err = %v)", err)
	}
}

func TestInstallSaysWhatIsWrongWithRar(t *testing.T) {
	_, err := Install(filepath.Join(t.TempDir(), "bundle.rar"), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "RAR") {
		t.Fatalf("err = %v, want it to name RAR and point at the zip release", err)
	}
}
