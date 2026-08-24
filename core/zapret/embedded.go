package zapret

import (
	_ "embed"
	"fmt"
	"os"
)

// embeddedArchive is a bundle release compiled into this binary.
//
// Why a megabyte and a half of asset rides along in the executable: the download
// path can fail in ways the user cannot do anything about, and every one of them
// ends with no bypass at all. Upstream publishes a new release and every install
// made before the matching pin ships gets ErrUntrustedVersion and installs
// nothing — that is not hypothetical, it is what 1.10.2 did to every fresh
// install until the pin landed. And the networks this product exists for are the
// networks where GitHub itself is blocked: there the very first connect cannot
// fetch a bundle, and the bypass that would have unblocked GitHub is the thing
// waiting on GitHub.
//
// So the archive is shipped, not fetched. It is the same file the release page
// serves — the checksum below is checked against the pin table on every build
// (see the embedded test), so this asset cannot drift away from the version it
// claims to be, and it goes through the same verified-bytes install path a
// download does.
//
// The embedded copy is a floor, never a ceiling: it is installed only when there
// is no bundle at all, and the updater replaces it the moment upstream publishes
// something newer this client pins. Refreshing it means dropping in the new
// release archive, moving the two lines below to its name and version, and
// adding the pin — leave any of the three behind and the build goes red.
//
//go:embed bundled/zapret-discord-youtube-1.10.2.zip
var embeddedArchive []byte

// EmbeddedVersion is the release embeddedArchive holds.
//
// It is written out rather than parsed from the file name because the name is a
// compile-time literal in the directive above and nothing carries it into the
// binary. The test that hashes the archive against pinnedArchives[EmbeddedVersion]
// is what keeps the three in agreement.
const EmbeddedVersion = "1.10.2"

// InstallEmbedded unpacks the bundle compiled into this binary into dir and
// stamps it with EmbeddedVersion, so the updater can tell what is installed and
// upgrade past it later.
//
// It goes through the same staging that Apply uses — unpack beside the live
// directory, carry the user's own lists over, swap in only once the archive
// proved to be a bundle — rather than writing into dir directly. The caller
// reaches here when there are no strategies installed, but "no strategies" is
// not the same as "nothing there": an interrupted install can leave a directory
// holding the user's added domains and nothing else, and unpacking over it would
// take those with it.
//
// The bytes need no checksum at this point. A download is verified because it
// arrived over a connection an adversary may hold; these bytes arrived inside
// the signed binary that is running this code, and anything able to rewrite them
// already owns the process.
func InstallEmbedded(dir string) ([]Strategy, error) {
	staging := dir + ".new"
	defer os.RemoveAll(staging)

	if _, err := InstallFromArchive(embeddedArchive, staging); err != nil {
		return nil, fmt.Errorf("zapret: вшитая сборка не встала: %w", err)
	}
	carryUserFiles(dir, staging)
	if err := WriteVersion(staging, EmbeddedVersion); err != nil {
		return nil, err
	}
	if err := swap(dir, staging); err != nil {
		return nil, err
	}

	// Re-read the strategies from where they now live: the ones InstallFromArchive
	// reported point into the staging directory, which no longer exists.
	return Discover(dir, dirFileNames(dir)), nil
}

// dirFileNames lists the file names in dir, or nothing when it cannot be read.
func dirFileNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}
