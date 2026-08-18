package zapret

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveUpdateAgainstUpstream exercises the whole update path against the real
// release feed: query, download, unpack, verify, stamp. It is skipped unless
// TENEBRA_LIVE is set, so the ordinary test run stays offline and deterministic.
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
	t.Logf("latest = %s, asset = %s (%d bytes)", rel.Version, rel.ArchiveURL, rel.Size)

	dir := filepath.Join(t.TempDir(), "zapret")
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
