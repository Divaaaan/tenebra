package zapret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveUpdateAgainstUpstream exercises the whole update path against the real
// release feed: query, then either the full download/verify/unpack/stamp (when
// the newest release is pinned) or the Variant A refusal (when it is not). It is
// skipped unless TENEBRA_LIVE is set, so the ordinary test run stays offline and
// deterministic.
//
//	TENEBRA_LIVE=1 go test ./core/zapret -run TestLiveUpdate -v
//
// It exists because every part of this that can break — an asset naming change,
// a redirect the client will not follow, an archive layout that no longer has
// bin/winws.exe — breaks against upstream, not against a fixture.
func TestLiveUpdateAgainstUpstream(t *testing.T) {
	if os.Getenv("TENEBRA_LIVE") == "" {
		t.Skip("set TENEBRA_LIVE=1 to run the live update check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	rel, err := LatestRelease(ctx, nil)
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	// The API digest is logged as the number a maintainer STARTS from when adding
	// this release to pinnedArchives — but it rides the same connection as the
	// archive, so it must be confirmed out of band (download from another vantage
	// point, sha256sum) before it is trusted as a pin.
	t.Logf("latest = %s, asset = %s (%d bytes, api digest %s — verify out of band before pinning)",
		rel.Version, rel.ArchiveURL, rel.Size, rel.SHA256)

	dir := filepath.Join(t.TempDir(), "zapret")

	if PinnedSum(rel.Version) == "" {
		// Variant A against real upstream: the newest release is newer than any pin
		// this build carries, so it must be refused rather than installed on the
		// strength of the same-connection digest. This is exactly the state a
		// maintainer resolves by pinning the version — the run still confirms the
		// query, the URL policy and the refusal all work end to end.
		err := Apply(ctx, nil, dir, rel)
		if !errors.Is(err, ErrUntrustedVersion) {
			t.Fatalf("an unpinned upstream release was not refused with ErrUntrustedVersion: %v", err)
		}
		t.Logf("latest upstream %s is not pinned — refused as designed; add it to pinnedArchives to ship it", rel.Version)
		return
	}

	// A pinned latest: exercise the whole path for real — download, verify against
	// the pin, unpack, stamp.
	if err := Apply(ctx, nil, dir, rel); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	strategies := Discover(dir, listNames(t, dir))
	t.Logf("installed %s, %d strategies, version file = %q", dir, len(strategies), Version(dir))
	if len(strategies) == 0 {
		t.Fatal("no strategies after a live install")
	}
	if Version(dir) != rel.Version {
		t.Fatalf("version stamp = %q, want %q", Version(dir), rel.Version)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "winws.exe")); err != nil {
		t.Fatalf("winws missing after a live install: %v", err)
	}
	t.Logf("covered domains: %d", len(Covered(dir)))
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}
