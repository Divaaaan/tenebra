package zapret

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// versionFile records which release of the bundle is installed.
//
// It lives inside the bundle directory rather than in the app's settings so the
// version can never drift from the files it describes: replacing the directory
// replaces the marker with it, and a bundle the user dropped in by hand simply
// has no marker, which reads as "unknown" instead of a confident wrong answer.
// The name is prefixed so it cannot collide with anything upstream ships.
const versionFile = "tenebra-bundle-version.txt"

// versionInName matches the version in a release file or folder name, e.g.
// "zapret-discord-youtube-1.10.1.zip".
var versionInName = regexp.MustCompile(`(\d+(?:\.\d+)+)`)

// Version reports the installed bundle's release, or "" when unknown (no bundle,
// or one installed from a source that carried no version).
func Version(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, versionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteVersion records the installed release inside the bundle directory.
func WriteVersion(dir, version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	return os.WriteFile(filepath.Join(dir, versionFile), []byte(version+"\n"), 0o644)
}

// VersionFromName extracts a release version from an archive or folder name.
//
// It is how an imported bundle gets a version at all: the release archive is
// named after its version, and reading it means the first update check compares
// against something real instead of re-downloading a bundle the user just
// installed by hand.
func VersionFromName(name string) string {
	base := filepath.Base(name)
	// Trim known archive extensions so a name like "…-1.10.1.tar.gz" does not
	// contribute digits from the extension itself.
	for _, ext := range []string{".zip", ".rar", ".gz", ".tar", ".7z"} {
		base = strings.TrimSuffix(strings.ToLower(base), ext)
	}
	return versionInName.FindString(base)
}

// Newer reports whether remote is a later release than local.
//
// Versions are compared component by component as numbers, so 1.10.1 correctly
// beats 1.9.9 — a string comparison would get that backwards, and getting it
// backwards means silently re-installing an older bundle over a newer one.
// An unknown local version (empty) counts as older: the bundle came from
// somewhere that carried no version, and taking the published release is the
// safe resolution. An unparseable remote version is never taken.
func Newer(local, remote string) bool {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return false
	}
	local = strings.TrimSpace(local)
	if local == "" {
		return true
	}
	l, r := versionParts(local), versionParts(remote)
	for i := 0; i < len(l) || i < len(r); i++ {
		var a, b int
		if i < len(l) {
			a = l[i]
		}
		if i < len(r) {
			b = r[i]
		}
		if a != b {
			return b > a
		}
	}
	return false
}

// versionParts splits a version into its numeric components, ignoring any
// leading "v" and any suffix that is not a number.
func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	fields := strings.Split(v, ".")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		// Stop at the first non-numeric component ("1.10.1-beta" -> 1.10.1): a
		// pre-release suffix is not something this comparison can order, and
		// guessing would be worse than ignoring it.
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
