//go:build windows

package zapret

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedArchiveMatchesItsPin is the whole reason the embedded bundle is
// safe to ship. The archive, the version constant and the pin table are three
// separate edits, and any one of them made without the other two produces a
// binary that installs bytes nobody checked, or claims a version it does not
// hold. All three drifts land here:
//
//	new zip, stale EmbeddedVersion  -> sum disagrees with the old version's pin
//	new EmbeddedVersion, stale zip  -> same, from the other side
//	new zip and version, no pin     -> nothing to compare against, refused below
//
// It is also the check that the asset survived the trip through git: a zip
// mangled by line-ending translation fails here rather than at a user's first
// connect.
func TestEmbeddedArchiveMatchesItsPin(t *testing.T) {
	pinned := pinnedArchives[EmbeddedVersion]
	if pinned == "" {
		t.Fatalf("the embedded bundle claims %s, which pinnedArchives does not pin — "+
			"add the checksum in verify.go, or the binary ships an archive nothing verifies", EmbeddedVersion)
	}
	sum := sha256.Sum256(embeddedArchive)
	if got := hex.EncodeToString(sum[:]); got != pinned {
		t.Fatalf("the embedded archive hashes to %s, but the pin for %s is %s — "+
			"the asset and the pin have drifted apart", got, EmbeddedVersion, pinned)
	}
}

// TestEmbeddedArchiveIsAWholeBundle proves the shipped asset is what the
// installer expects, on the build rather than on a user's machine: strategies to
// offer and the binary they are useless without. A pin only says the bytes are
// the ones that were hashed — it cannot say they are a bundle.
func TestEmbeddedArchiveIsAWholeBundle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "zapret")

	got, err := InstallEmbedded(dir)
	if err != nil {
		t.Fatalf("InstallEmbedded: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("the embedded bundle offers %d strategies: %+v", len(got), got)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "winws.exe")); err != nil {
		t.Fatalf("no bin/winws.exe in the embedded bundle: %v", err)
	}
	// The release wraps everything in a versioned folder; unpacking it verbatim
	// would nest the bundle a level deeper and invalidate every stored strategy
	// path.
	if _, err := os.Stat(filepath.Join(dir, "zapret-discord-youtube-"+EmbeddedVersion)); err == nil {
		t.Error("the release wrapper directory survived the embedded install")
	}
	// Without the stamp the first update check reads "unknown", treats it as older
	// than anything published, and re-downloads the bundle just installed.
	if v := Version(dir); v != EmbeddedVersion {
		t.Errorf("installed version = %q, want %q", v, EmbeddedVersion)
	}
	// The strategy paths have to point at the live directory, not at the staging
	// one they were unpacked into and which is gone by now.
	for _, s := range got {
		if _, err := os.Stat(s.Path); err != nil {
			t.Fatalf("strategy %s points nowhere: %v", s.Name, err)
		}
	}
}

// TestInstallEmbeddedKeepsUserLists: "no strategies installed" is not "nothing
// installed". An interrupted install can leave a directory holding the domains
// the user added by hand, and laying the embedded floor over it must carry those
// across the same way an update does.
func TestInstallEmbeddedKeepsUserLists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "zapret")
	if err := os.MkdirAll(filepath.Join(dir, "lists"), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(dir, "lists", "list-general-user.txt")
	if err := os.WriteFile(mine, []byte("mine.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallEmbedded(dir); err != nil {
		t.Fatalf("InstallEmbedded: %v", err)
	}

	data, err := os.ReadFile(mine)
	if err != nil {
		t.Fatalf("the user's own list did not survive the embedded install: %v", err)
	}
	if string(data) != "mine.example\n" {
		t.Errorf("list-general-user.txt = %q, want the user's own content", data)
	}
}
