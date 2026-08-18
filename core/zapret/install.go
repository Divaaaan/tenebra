package zapret

import (
	"archive/zip"
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
	if strings.EqualFold(filepath.Ext(archivePath), ".rar") {
		return nil, errors.New("zapret: RAR не поддерживается — возьми .zip с страницы релизов")
	}

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("zapret: не читается архив: %w", err)
	}
	defer zr.Close()

	prefix := commonPrefix(zr.File)

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
	for _, f := range zr.File {
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
	return strategies, nil
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
