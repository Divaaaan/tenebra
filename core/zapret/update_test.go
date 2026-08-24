package zapret

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// releaseFeed describes the fake GitHub release a test serves.
//
// announce and serve are separate on purpose: the feed publishes the size and
// SHA-256 of one file while the server hands over another, which is exactly the
// shape of "something swapped the bytes between the release page and this
// machine".
type releaseFeed struct {
	version    string
	prerelease bool
	// announce is the archive the feed describes. serve is the archive actually
	// delivered; empty means "the announced one", i.e. an honest release.
	announce string
	serve    string
	// digest overrides the published checksum: "" publishes the truth about
	// announce, "none" publishes none at all, anything else is published as-is.
	digest string
	// size overrides the published byte count; 0 publishes the truth.
	size int64
	// url overrides the published browser_download_url.
	url string
	// redirect, when set, is where the archive request is sent instead of being
	// answered with a file.
	redirect string
}

// releaseServer serves a GitHub-shaped release feed and the archive behind it.
func releaseServer(t *testing.T, f releaseFeed) *httptest.Server {
	t.Helper()
	if f.serve == "" {
		f.serve = f.announce
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/archive.zip", func(w http.ResponseWriter, r *http.Request) {
		if f.redirect != "" {
			http.Redirect(w, r, f.redirect, http.StatusFound)
			return
		}
		http.ServeFile(w, r, f.serve)
	})
	mux.HandleFunc("/release", func(w http.ResponseWriter, _ *http.Request) {
		zip := map[string]any{
			"name":                 fmt.Sprintf("zapret-%s.zip", f.version),
			"browser_download_url": srv.URL + "/archive.zip",
			"size":                 fileSize(t, f.announce),
			"digest":               "sha256:" + fileSum(t, f.announce),
		}
		if f.url != "" {
			zip["browser_download_url"] = f.url
		}
		if f.size != 0 {
			zip["size"] = f.size
		}
		switch f.digest {
		case "":
			// the truth about the announced archive
		case "none":
			delete(zip, "digest")
		default:
			zip["digest"] = f.digest
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":   f.version,
			"prerelease": f.prerelease,
			"assets": []map[string]any{
				// The .rar comes first on purpose: only the zip can be installed, so
				// picking by position rather than by extension would break every update.
				{"name": fmt.Sprintf("zapret-%s.rar", f.version), "browser_download_url": srv.URL + "/archive.rar"},
				zip,
			},
		})
	})
	return srv
}

// fileSum is the SHA-256 of a file, lowercase hex — what a release feed
// publishes for its asset.
func fileSum(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// fileSize is the byte count a release feed publishes for its asset.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// allowTestOrigin lets checkArchiveURL and the redirect guard accept one extra
// origin — the given httptest server — without turning the host policy off, so
// the policy stays exercised for every OTHER host (which is what the policy tests
// rely on). The hook is a function, not a settable string, which is the point:
// it is how a test overrides the origin without leaving a `-ldflags -X` handle in
// the shipped binary. Restored at the end of the test.
func allowTestOrigin(t *testing.T, srv *httptest.Server) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server url %q: %v", srv.URL, err)
	}
	host := u.Host
	saved := archiveURLAllowExtra
	archiveURLAllowExtra = func(candidate *url.URL) bool {
		return candidate.Host == host
	}
	t.Cleanup(func() { archiveURLAllowExtra = saved })
}

// latest points the package at the test server and returns what it parses, so
// the asset choice and the draft/pre-release rules are exercised for real.
func latest(t *testing.T, srv *httptest.Server) (Release, error) {
	t.Helper()
	savedAPI := releaseAPI
	releaseAPI = srv.URL + "/release"
	allowTestOrigin(t, srv)
	t.Cleanup(func() { releaseAPI = savedAPI })
	return LatestRelease(context.Background(), srv.Client())
}

// Only the zip can be installed, and upstream publishes .rar and .tar.gz beside
// it. Picking by position instead of extension would make every update fail as
// what looks like a network fault.
func TestLatestReleasePicksTheZip(t *testing.T) {
	archive := bundleZip(t, "zapret-discord-youtube-1.11.0", nil)
	srv := releaseServer(t, releaseFeed{version: "1.11.0", announce: archive})

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
	srv := releaseServer(t, releaseFeed{version: "1.11.0", announce: archive, prerelease: true})

	if _, err := latest(t, srv); err == nil {
		t.Fatal("a pre-release was accepted as the latest bundle")
	}
}

// An update must not cost the user the entries they added by hand, nor the node
// addresses kept out of the packet filter — losing the latter silently puts the
// bypass back on top of the tunnel's own handshakes.
// The version is pinned for the duration so this exercises the install path
// itself: under Variant A only a pinned version is installed at all.
func TestApplyKeepsLocalStateAndStampsVersion(t *testing.T) {
	archive := bundleZip(t, "zapret-discord-youtube-1.11.0", map[string]string{
		"utils/check_updates.enabled": "true",
		"lists/list-general-user.txt": "# Never leave this file empty\ndomain.example.abc\n",
	})
	pinFor(t, "1.11.0", fileSum(t, archive))
	srv := releaseServer(t, releaseFeed{version: "1.11.0", announce: archive})

	dir := filepath.Join(t.TempDir(), "zapret")
	if _, err := Install(archive, dir); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lists", "list-general-user.txt"), []byte("my-site.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExcludeNodes(dir, []string{"95.163.176.178"}); err != nil {
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
	if got := Version(dir); got != "1.11.0" {
		t.Errorf("installed version = %q, want 1.11.0", got)
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
	srv := releaseServer(t, releaseFeed{version: "1.11.0", announce: junk})

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
