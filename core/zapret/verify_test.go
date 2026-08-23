package zapret

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installedBundle seeds a working bundle at dir and returns it, so a refusal can
// be checked against something that must survive.
func installedBundle(t *testing.T, version string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "zapret")
	if _, err := Install(bundleZip(t, "zapret-discord-youtube-"+version, nil), dir); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	if err := WriteVersion(dir, version); err != nil {
		t.Fatal(err)
	}
	return dir
}

// equalLengthBundles builds two valid bundles carrying different payloads and
// pads the shorter to the length of the other, so the pair is indistinguishable
// by byte count. That is the point: a length check alone lets the second one
// through, and what gets through here ends up as a .bat run by cmd.exe under
// LocalSystem.
func equalLengthBundles(t *testing.T, root string) (honest, swapped string) {
	t.Helper()
	honest = honestBundle(t, root)
	swapped = bundleZip(t, root, map[string]string{
		"payload.bat": "@echo off - this is not what upstream published",
	})
	// Trailing bytes after the central directory do not stop a zip from opening
	// (that is how self-extracting archives work), so padding keeps both
	// installable.
	if a, b := fileSize(t, honest), fileSize(t, swapped); a < b {
		padTo(t, honest, b)
	} else if b < a {
		padTo(t, swapped, a)
	}
	return honest, swapped
}

// padTo appends zero bytes to a file until it is n bytes long.
func padTo(t *testing.T, path string, n int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > n {
		t.Fatalf("%s is %d bytes, cannot be padded down to %d", path, len(data), n)
	}
	if err := os.WriteFile(path, append(data, make([]byte, n-int64(len(data)))...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// honestBundle is a bundle a release feed can describe truthfully.
func honestBundle(t *testing.T, root string) string {
	t.Helper()
	return bundleZip(t, root, map[string]string{
		"payload.bat": "@echo off - published by upstream",
	})
}

// The whole point of the exercise: the feed describes one archive and the wire
// delivers another. Nothing downstream can catch this — Install only checks that
// the archive has a .bat and a bin/winws.exe, and both are there — and the .bat
// it accepts is then run through cmd.exe by a service account. The download has
// to be the thing that refuses.
func TestApplyRefusesASwappedArchive(t *testing.T) {
	honest, swapped := equalLengthBundles(t, "zapret-discord-youtube-1.11.0")
	// Pinned to the honest sum, so the version clears the pin gate and the refusal
	// is the checksum on the swapped bytes, not "unknown version".
	pinFor(t, "1.11.0", fileSum(t, honest))

	dir := installedBundle(t, "1.10.1")
	srv := releaseServer(t, releaseFeed{version: "1.11.0", announce: honest, serve: swapped})

	rel, err := latest(t, srv)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	err = Apply(context.Background(), srv.Client(), dir, rel)
	if err == nil {
		t.Fatal("an archive that does not match the published checksum was installed")
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Errorf("refusal is not an integrity error: %v", err)
	}
	if !strings.Contains(err.Error(), "контрольная сумма") {
		t.Errorf("the refusal does not say the checksum was wrong: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "payload.bat")); err == nil {
		t.Fatal("the swapped payload reached the bundle directory")
	}
	if got := Version(dir); got != "1.10.1" {
		t.Errorf("version after a refused update = %q, want the old 1.10.1", got)
	}
}

// The feed publishes a byte count and the code has always parsed it. Parsing a
// number and never comparing it is worse than not reading it at all: it reads as
// a check that is not there.
func TestApplyRefusesASizeMismatch(t *testing.T) {
	honest := honestBundle(t, "zapret-discord-youtube-1.11.0")
	// Pinned so the version clears the pin gate and reaches the length check.
	pinFor(t, "1.11.0", fileSum(t, honest))

	dir := installedBundle(t, "1.10.1")
	// The checksum tells the truth about what is served; only the announced
	// length is wrong, so this test can fail for one reason only.
	srv := releaseServer(t, releaseFeed{
		version:  "1.11.0",
		announce: honest,
		size:     fileSize(t, honest) + 4096,
	})

	rel, err := latest(t, srv)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	err = Apply(context.Background(), srv.Client(), dir, rel)
	if err == nil {
		t.Fatal("an archive whose length disagrees with the release was installed")
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Errorf("refusal is not an integrity error: %v", err)
	}
	if !strings.Contains(err.Error(), "размер") {
		t.Errorf("the refusal does not say the size was wrong: %v", err)
	}
	if got := Version(dir); got != "1.10.1" {
		t.Errorf("version after a refused update = %q, want the old 1.10.1", got)
	}
}

// A download URL taken from a feed is attacker-reachable input the moment the
// feed is. Plain http on some other host is the cheapest possible version of
// that, and it has to be refused before a single byte is fetched.
func TestApplyRefusesAnArchiveOffGitHub(t *testing.T) {
	honest := honestBundle(t, "zapret-discord-youtube-1.11.0")
	dir := installedBundle(t, "1.10.1")

	// A second server standing in for "somewhere that is not github.com". It
	// serves a perfectly good bundle: the reason to refuse is where it came from.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, honest)
	}))
	t.Cleanup(other.Close)

	srv := releaseServer(t, releaseFeed{
		version:  "1.11.0",
		announce: honest,
		url:      other.URL + "/archive.zip",
	})

	rel, err := latest(t, srv)
	if err == nil {
		// The refusal may come at parse time, which is better still — but then
		// Apply must not accept the release either.
		err = Apply(context.Background(), srv.Client(), dir, rel)
		if err == nil {
			t.Fatal("a bundle was fetched from a host outside github.com over plain http")
		}
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Errorf("refusal is not an integrity error: %v", err)
	}
	if got := Version(dir); got != "1.10.1" {
		t.Errorf("version after a refused update = %q, want the old 1.10.1", got)
	}
}

// pinFor installs a checksum for version for the duration of the test, so the
// pin mechanism can be exercised without depending on which upstream releases
// happen to be pinned today.
func pinFor(t *testing.T, version, sum string) {
	t.Helper()
	saved, had := pinnedArchives[version]
	pinnedArchives[version] = sum
	t.Cleanup(func() {
		if had {
			pinnedArchives[version] = saved
			return
		}
		delete(pinnedArchives, version)
	})
}

// A pinned version is the one case where this program knows the answer
// independently of the connection carrying it. If the feed disagrees, the
// disagreement is the finding — resolving it in either direction would throw away
// the only check that survives a forged TLS connection.
func TestPinnedVersionOverridesTheFeed(t *testing.T) {
	honest := honestBundle(t, "zapret-discord-youtube-1.11.0")
	pinFor(t, "1.11.0", strings.Repeat("ab", 32))

	dir := installedBundle(t, "1.10.1")
	srv := releaseServer(t, releaseFeed{version: "1.11.0", announce: honest})

	rel, err := latest(t, srv)
	if err == nil {
		err = Apply(context.Background(), srv.Client(), dir, rel)
	}
	if err == nil {
		t.Fatal("a release contradicting its pinned checksum was installed")
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Errorf("refusal is not an integrity error: %v", err)
	}
	if !strings.Contains(err.Error(), strings.Repeat("ab", 32)) {
		t.Errorf("the refusal does not name the pinned sum: %v", err)
	}
	if got := Version(dir); got != "1.10.1" {
		t.Errorf("version after a refused update = %q, want the old 1.10.1", got)
	}
}

// The pin also answers for a release whose feed publishes no checksum at all:
// what the program verified for itself does not depend on the feed still
// carrying a digest field.
func TestPinnedVersionInstallsWithoutAPublishedChecksum(t *testing.T) {
	honest := honestBundle(t, "zapret-discord-youtube-1.11.0")
	pinFor(t, "1.11.0", fileSum(t, honest))

	dir := installedBundle(t, "1.10.1")
	srv := releaseServer(t, releaseFeed{version: "1.11.0", announce: honest, digest: "none"})

	rel, err := latest(t, srv)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if err := Apply(context.Background(), srv.Client(), dir, rel); err != nil {
		t.Fatalf("a release matching its pinned checksum was refused: %v", err)
	}
	if got := Version(dir); got != "1.11.0" {
		t.Errorf("installed version = %q, want 1.11.0", got)
	}
}

// The heart of Variant A: a version this client does not pin is not installed
// automatically, no matter what checksum the feed carries for it. The digest
// GitHub publishes beside the asset is NOT an out-of-band anchor — it rides the
// same TLS connection as the archive, so the trusted-root proxy this product
// defends against forges both to agree. A release with a truthful published
// digest and one with none are refused the same way, the refusal is
// ErrUntrustedVersion rather than a tamper alarm, and the working bundle stays.
func TestApplyRefusesAnUnpinnedVersion(t *testing.T) {
	cases := map[string]string{
		"with a truthful published digest": "",     // the feed's digest matches the archive
		"with no digest at all":            "none", // the feed publishes none
	}
	for name, digest := range cases {
		t.Run(name, func(t *testing.T) {
			// 1.11.0 is deliberately absent from pinnedArchives.
			if PinnedSum("1.11.0") != "" {
				t.Fatal("test assumes 1.11.0 is unpinned")
			}
			honest := honestBundle(t, "zapret-discord-youtube-1.11.0")
			dir := installedBundle(t, "1.10.1")
			srv := releaseServer(t, releaseFeed{version: "1.11.0", announce: honest, digest: digest})

			rel, err := latest(t, srv)
			if err == nil {
				err = Apply(context.Background(), srv.Client(), dir, rel)
			}
			if err == nil {
				t.Fatal("an unpinned version was installed automatically")
			}
			if !errors.Is(err, ErrUntrustedVersion) {
				t.Errorf("refusal is not ErrUntrustedVersion: %v", err)
			}
			// It must not masquerade as a tamper alarm: that is a different event
			// with a different message, and crying wolf on every new upstream
			// release is how the real one gets scrolled past.
			if errors.Is(err, ErrIntegrity) {
				t.Errorf("an ordinary new version was reported as an integrity failure: %v", err)
			}
			if got := Version(dir); got != "1.10.1" {
				t.Errorf("version after a refused update = %q, want the old 1.10.1", got)
			}
			if _, err := os.Stat(filepath.Join(dir, "payload.bat")); err == nil {
				t.Fatal("the unpinned bundle's payload reached the bundle directory")
			}
		})
	}
}

// A release URL on github.com legitimately redirects to the asset storage, so
// redirects are followed — which means the hop is a second place the destination
// is chosen by something other than this program. Checking only the first URL
// would leave the hole open one 302 further along.
func TestApplyRefusesARedirectOffGitHub(t *testing.T) {
	honest := honestBundle(t, "zapret-discord-youtube-1.11.0")
	// Pinned so the version clears the pin gate and the download actually starts —
	// otherwise Apply refuses as "unknown version" before a redirect is ever seen,
	// and this test would not be exercising the redirect guard at all.
	pinFor(t, "1.11.0", fileSum(t, honest))
	dir := installedBundle(t, "1.10.1")

	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, honest)
	}))
	t.Cleanup(elsewhere.Close)

	srv := releaseServer(t, releaseFeed{
		version:  "1.11.0",
		announce: honest,
		redirect: elsewhere.URL + "/archive.zip",
	})

	rel, err := latest(t, srv)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	err = Apply(context.Background(), srv.Client(), dir, rel)
	if err == nil {
		t.Fatal("the download followed a redirect to a host outside github.com")
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Errorf("refusal is not an integrity error: %v", err)
	}
	if got := Version(dir); got != "1.10.1" {
		t.Errorf("version after a refused update = %q, want the old 1.10.1", got)
	}
}

// The host rule is the one part of this that a lookalike name defeats silently,
// so it is worth stating case by case what counts as GitHub and what does not.
func TestCheckArchiveURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://github.com/Flowseal/zapret-discord-youtube/releases/download/1.10.1/b.zip", true},
		{"https://release-assets.githubusercontent.com/github-production-release-asset/1/2?sig=x", true}, // today's asset host
		{"https://objects.githubusercontent.com/b.zip", true},                                            // the asset host before it
		{"https://GitHub.com./b.zip", true},                                                              // case and a trailing dot are both legal spellings

		{"http://github.com/b.zip", false}, // https or nothing
		{"ftp://github.com/b.zip", false},  //
		{"", false},                        // no scheme, no host
		{"https://evil.example/b.zip", false},
		{"https://github.com.evil.example/b.zip", false},         // the real host is the last label
		{"https://evil-githubusercontent.com/b.zip", false},      // a lookalike, not a subdomain
		{"https://githubusercontent.com.evil.example", false},    //
		{"https://githubusercontent.com/b.zip", false},           // the bare apex is not an asset host
		{"https://raw.githubusercontent.com/o/r/main/x", false},  // user content: anyone can publish here
		{"https://gist.githubusercontent.com/u/id/raw/x", false}, // gists too
		{"https://github.com@evil.example/b.zip", false},         // the host here is evil.example
		{"https://user:pass@github.com/b.zip", false},            // a release page publishes no credentials
	}
	for _, c := range cases {
		err := checkArchiveURL(c.url)
		if c.ok && err != nil {
			t.Errorf("checkArchiveURL(%q) refused a GitHub address: %v", c.url, err)
		}
		if !c.ok && err == nil {
			t.Errorf("checkArchiveURL(%q) accepted an address it should not have", c.url)
		}
		if !c.ok && err != nil && !errors.Is(err, ErrIntegrity) {
			t.Errorf("checkArchiveURL(%q) refused with a non-integrity error: %v", c.url, err)
		}
	}
}
