package zapret

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Install unpacks a zapret bundle archive into dir and reports the strategies it
// found.
//
// The archive is taken exactly as downloaded. A release page hands out a zip
// whose contents sit under a single versioned folder
// (`zapret-discord-youtube-1.10.1/…`); that wrapper is stripped so upgrading to
// a new release does not nest the bundle one level deeper every time and quietly
// break every stored strategy path.
//
// Only .zip is handled. RAR is a proprietary format with no decoder in the
// standard library, and adding one to a privacy tool for the sake of an archive
// format is a dependency to audit forever — the upstream project publishes zip
// releases, so the honest answer to a .rar is to say so.
func Install(archivePath, dir string) ([]Strategy, error) {
	// A directory is accepted as readily as an archive: the user may have
	// unpacked the release already, and telling them to re-zip it would be
	// busywork the program can do itself.
	if info, err := os.Stat(archivePath); err == nil && info.IsDir() {
		return InstallDir(archivePath, dir)
	}
	if strings.EqualFold(filepath.Ext(archivePath), ".rar") {
		return nil, errors.New("zapret: RAR не поддерживается — возьми .zip с страницы релизов")
	}

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("zapret: не читается архив: %w", err)
	}
	defer zr.Close()

	return installZipFiles(zr.File, dir)
}

// InstallFromArchive unpacks an in-memory bundle archive into dir.
//
// The updater uses this rather than the path-based Install so the bytes that were
// hash-verified are the exact bytes unpacked: there is no file on disk to reopen
// between the checksum and the install, and therefore no window in which a local
// process could swap the archive's contents (a TOCTOU) before it becomes a .bat
// run by cmd.exe under LocalSystem. See Apply.
func InstallFromArchive(data []byte, dir string) ([]Strategy, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("zapret: не читается архив: %w", err)
	}
	return installZipFiles(zr.File, dir)
}

// installZipFiles unpacks the members of an opened zip into dir and reports the
// strategies it found. It is shared by Install (from a file) and
// InstallFromArchive (from memory) so the two cannot drift in how they strip the
// wrapper directory, reject traversal, or decide what counts as a bundle.
func installZipFiles(files []*zip.File, dir string) ([]Strategy, error) {
	prefix := commonPrefix(files)

	// Unpack into a fresh directory, so a re-import cannot leave stale strategy
	// files from a previous version lying beside the new ones — the user would
	// then be offered strategies that no longer match the bundled winws.
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("zapret: не очистить каталог: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("zapret: не создать каталог: %w", err)
	}

	var names []string
	for _, f := range files {
		rel := strings.TrimPrefix(f.Name, prefix)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}

		// Reject path traversal before touching the filesystem: an archive is
		// untrusted input, and `../` in a member name is how one writes outside
		// the directory it was told to unpack into.
		dest := filepath.Join(dir, filepath.FromSlash(rel))
		if !strings.HasPrefix(dest, filepath.Clean(dir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("zapret: подозрительный путь в архиве: %q", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return nil, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, err
		}
		if err := extractFile(f, dest); err != nil {
			return nil, err
		}
		if filepath.Dir(rel) == "." {
			names = append(names, rel)
		}
	}

	strategies := Discover(dir, names)
	if len(strategies) == 0 {
		return nil, errors.New("zapret: в архиве нет стратегий (.bat) — это точно сборка zapret?")
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "winws.exe")); err != nil {
		return nil, errors.New("zapret: в архиве нет bin/winws.exe — это точно сборка zapret?")
	}
	disableBundleUpdater(dir)
	return strategies, nil
}

// InstallDir copies an already-unpacked bundle into dir.
//
// It follows the same rule as the archive path: if the given folder is just a
// wrapper holding the real bundle (which is what unpacking a release usually
// produces — `zapret-discord-youtube-1.10.1/` inside the folder you unpacked
// into), descend into it, so the installed layout is identical either way and
// stored strategy paths keep working.
func InstallDir(src, dir string) ([]Strategy, error) {
	root, err := bundleRoot(src)
	if err != nil {
		return nil, err
	}

	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("zapret: не очистить каталог: %w", err)
	}
	if err := copyTree(root, dir); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	strategies := Discover(dir, names)
	if len(strategies) == 0 {
		return nil, errors.New("zapret: в папке нет стратегий (.bat) — это точно сборка zapret?")
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "winws.exe")); err != nil {
		return nil, errors.New("zapret: в папке нет bin/winws.exe — это точно сборка zapret?")
	}
	disableBundleUpdater(dir)
	return strategies, nil
}

// disableBundleUpdater turns off the update checker the bundle ships with, which
// every strategy batch invokes on launch.
//
// It is switched off at the last moment a bundle is installed rather than only
// on the update path, because the file arrives with any bundle — one the user
// dropped in as readily as one this app downloaded. Left enabled it reaches
// GitHub every time a strategy starts, on a network where reaching GitHub is
// exactly what is in doubt: the bypass then waits on a request that the DPI it
// has not raised yet is stalling. It also makes two mechanisms decide what
// version is installed, and the other one (see Apply) knows how to stage a
// download and keep the user's own lists.
//
// Best-effort by design: an absent file is the desired end state, and a bundle
// that cannot be written to is a problem the install itself would already have
// reported.
func disableBundleUpdater(dir string) {
	_ = os.Remove(filepath.Join(dir, "utils", "check_updates.enabled"))
}

// bundleRoot returns the directory that actually holds the bundle: src itself,
// or its single subdirectory when src is a wrapper.
func bundleRoot(src string) (string, error) {
	if hasBundleMarkers(src) {
		return src, nil
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return "", fmt.Errorf("zapret: не прочитать папку: %w", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(src, e.Name()))
		}
	}
	// Exactly one candidate: descending is unambiguous. With several, guessing
	// would be worse than reporting that this is not a bundle.
	if len(dirs) == 1 && hasBundleMarkers(dirs[0]) {
		return dirs[0], nil
	}
	return src, nil
}

// hasBundleMarkers reports whether a directory looks like an unpacked bundle.
func hasBundleMarkers(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "bin", "winws.exe")); err != nil {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".bat") {
			return true
		}
	}
	return false
}

// copyTree copies a directory recursively.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// extractFile writes one archive member to dest.
func extractFile(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("zapret: %s: %w", f.Name, err)
	}
	defer rc.Close()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("zapret: %s: %w", dest, err)
	}
	defer out.Close()

	// Bounded copy: a crafted archive can claim a small compressed size and
	// expand to fill the disk. 256 MiB is far above any real bundle (the
	// published ones are a few megabytes) and far below "fills the disk".
	const maxMember = 256 << 20
	if _, err := io.Copy(out, io.LimitReader(rc, maxMember)); err != nil {
		return fmt.Errorf("zapret: %s: %w", dest, err)
	}
	return nil
}

// commonPrefix returns the single top-level directory every member shares, or ""
// when the archive has none.
//
// Release archives wrap everything in one versioned folder; stripping it keeps
// the installed layout stable across upgrades. An archive packed flat, or with
// several top-level entries, is left exactly as it is.
func commonPrefix(files []*zip.File) string {
	var prefix string
	for _, f := range files {
		name := strings.TrimPrefix(f.Name, "./")
		if name == "" {
			continue
		}
		idx := strings.Index(name, "/")
		if idx < 0 {
			return "" // a member at the root: no shared wrapper
		}
		top := name[:idx]
		if prefix == "" {
			prefix = top
			continue
		}
		if prefix != top {
			return ""
		}
	}
	return prefix
}
