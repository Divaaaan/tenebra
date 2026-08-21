package zapret

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A published bundle is versioned 1.9.9 → 1.10.1, which string comparison gets
// backwards. Getting it backwards means an "update" that installs an older
// bundle over a newer one and reports success.
func TestNewerComparesNumerically(t *testing.T) {
	cases := []struct {
		local, remote string
		want          bool
	}{
		{"1.9.9", "1.10.1", true},
		{"1.10.1", "1.9.9", false},
		{"1.10.1", "1.10.1", false},
		{"1.10", "1.10.1", true},
		{"", "1.10.1", true},  // unknown local: take what upstream publishes
		{"1.10.1", "", false}, // nothing published: never "update" to nothing
		{"1.10.1", "v1.11", true},
		// A pre-release suffix is not orderable, so comparison stops at the numeric
		// part: 1.10.1-beta reads as 1.10, and the published 1.10.1 is newer — which
		// is also what should happen, a final release beating a beta of itself.
		{"1.10.1-beta", "1.10.1", true},
	}
	for _, c := range cases {
		if got := Newer(c.local, c.remote); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.local, c.remote, got, c.want)
		}
	}
}

func TestVersionFromName(t *testing.T) {
	cases := map[string]string{
		"zapret-discord-youtube-1.10.1.zip":    "1.10.1",
		"zapret-discord-youtube-1.10.1":        "1.10.1",
		"zapret-discord-youtube-1.10.1.tar.gz": "1.10.1",
		"bundle.zip":                           "",
	}
	for name, want := range cases {
		if got := VersionFromName(name); got != want {
			t.Errorf("VersionFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

// releaseServer serves a GitHub-shaped release pointing at archivePath.
func releaseServer(t *testing.T, version, archivePath string, prerelease bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/archive.zip", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, archivePath)
	})
	mux.HandleFunc("/release", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":   version,
			"prerelease": prerelease,
			"assets": []map[string]any{
				// The .rar comes first on purpose: only the zip can be installed, so
				// picking by position rather than by extension would break every update.
				{"name": fmt.Sprintf("zapret-%s.rar", version), "browser_download_url": srv.URL + "/archive.rar"},
				{"name": fmt.Sprintf("zapret-%s.zip", version), "browser_download_url": srv.URL + "/archive.zip", "size": 1234},
			},
		})
	})
	return srv
}

// latest points the package at the test server and returns what it parses, so
// the asset choice and the draft/pre-release rules are exercised for real.
func latest(t *testing.T, srv *httptest.Server) (Release, error) {
	t.Helper()
	saved := releaseAPI
	releaseAPI = srv.URL + "/release"
	t.Cleanup(func() { releaseAPI = saved })
	return LatestRelease(context.Background(), srv.Client())
}

// Only the zip can be installed, and upstream publishes .rar and .tar.gz beside
// it. Picking by position instead of extension would make every update fail as
// what looks like a network fault.
func TestLatestReleasePicksTheZip(t *testing.T) {
	archive := bundleZip(t, "zapret-discord-youtube-1.11.0", nil)
	srv := releaseServer(t, "1.11.0", archive, false)

	rel, err := latest(t, srv)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if rel.Version != "1.11.0" {
		t.Errorf("version = %q, want 1.11.0", rel.Version)
	}
	if !strings.HasSuffix(rel.ArchiveURL, ".zip") {
		t.Errorf("archive = %q, want the zip asset", rel.ArchiveURL)
	}
}

// A pre-release is an upstream experiment. Installing one on someone's machine
// unasked is not what "keep the bypass current" means.
func TestLatestReleaseRefusesPrerelease(t *testing.T) {
	archive := bundleZip(t, "zapret-discord-youtube-1.11.0", nil)
	srv := releaseServer(t, "1.11.0", archive, true)

	if _, err := latest(t, srv); err == nil {
		t.Fatal("a pre-release was accepted as the latest bundle")
	}
}

// An update must not cost the user the entries they added by hand, nor the node
// addresses kept out of the packet filter — losing the latter silently puts the
// bypass back on top of the tunnel's own handshakes.
func TestApplyKeepsLocalStateAndStampsVersion(t *testing.T) {
	archive := bundleZip(t, "zapret-discord-youtube-1.10.1", map[string]string{
		"utils/check_updates.enabled": "true",
		"lists/list-general-user.txt": "# Never leave this file empty\ndomain.example.abc\n",
	})
	srv := releaseServer(t, "1.10.1", archive, false)

	dir := filepath.Join(t.TempDir(), "zapret")
	if _, err := Install(archive, dir); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lists", "list-general-user.txt"), []byte("my-site.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ExcludeNodes(dir, []string{"95.163.176.178"}); err != nil {
		t.Fatal(err)
	}

	rel, err := latest(t, srv)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if err := Apply(context.Background(), srv.Client(), dir, rel); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	user, err := os.ReadFile(filepath.Join(dir, "lists", "list-general-user.txt"))
	if err != nil || string(user) != "my-site.example\n" {
		t.Errorf("user domain list was not carried over: %q (%v)", user, err)
	}
	excl, err := os.ReadFile(filepath.Join(dir, "lists", "ipset-exclude-user.txt"))
	if err != nil || !strings.Contains(string(excl), "95.163.176.178") {
		t.Errorf("node exclusions were lost by the update: %q (%v)", excl, err)
	}
	if got := Version(dir); got != "1.10.1" {
		t.Errorf("installed version = %q, want 1.10.1", got)
	}
	// Upstream's own updater is disabled: two mechanisms fetching the same
	// releases on different schedules is one more than can be reasoned about.
	if _, err := os.Stat(filepath.Join(dir, "utils", "check_updates.enabled")); !os.IsNotExist(err) {
		t.Error("the bundle's built-in update check was left enabled")
	}
	// The staging directory must not survive; a leftover would be re-used blindly.
	if _, err := os.Stat(dir + ".new"); !os.IsNotExist(err) {
		t.Error("the staging directory was left behind")
	}
}

// A truncated download or a "release" that is not a bundle must leave the
// working installation exactly as it was. Unpacking over the live directory
// would turn one bad night at the mirror into a machine with no bypass at all.
func TestApplyKeepsTheOldBundleWhenTheNewOneIsBad(t *testing.T) {
	good := bundleZip(t, "zapret-discord-youtube-1.10.1", nil)
	dir := filepath.Join(t.TempDir(), "zapret")
	if _, err := Install(good, dir); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	if err := WriteVersion(dir, "1.10.1"); err != nil {
		t.Fatal(err)
	}

	// An archive with no winws.exe: a plausible wrong download.
	junk := filepath.Join(t.TempDir(), "junk.zip")
	if err := os.WriteFile(junk, []byte("not a zip at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := releaseServer(t, "1.11.0", junk, false)

	rel, err := latest(t, srv)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if err := Apply(context.Background(), srv.Client(), dir, rel); err == nil {
		t.Fatal("Apply accepted an archive that is not a bundle")
	}

	if _, err := os.Stat(filepath.Join(dir, "bin", "winws.exe")); err != nil {
		t.Fatalf("the working bundle did not survive a failed update: %v", err)
	}
	if got := Version(dir); got != "1.10.1" {
		t.Errorf("version after a failed update = %q, want the old 1.10.1", got)
	}
}
