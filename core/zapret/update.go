package zapret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// releaseAPI is the upstream release feed. The bundle is published as a GitHub
// release, one archive per version, and there is no update feed beyond that — so
// this is the source of truth for "is there a newer one". A var rather than a
// const so tests can point it at a local server; nothing else reassigns it.
var releaseAPI = "https://api.github.com/repos/Flowseal/zapret-discord-youtube/releases/latest"

const (
	// maxArchiveBytes bounds a download. Published bundles are ~1.5 MB; 64 MiB is
	// far above any real one and far below "fills the disk" for a truncated or
	// hostile response.
	maxArchiveBytes = 64 << 20

	// userAgent is required: the GitHub API answers 403 to a request without one.
	userAgent = "tenebra-zapret-updater"
)

// Release is a published bundle version and the archive to fetch it from.
type Release struct {
	// Version is the release tag, e.g. "1.10.1".
	Version string `json:"version"`
	// ArchiveURL is the .zip asset's download URL.
	ArchiveURL string `json:"-"`
	// Size is the asset's size in bytes, as reported by the API.
	Size int64 `json:"size,omitempty"`
}

// LatestRelease reports the newest published bundle.
//
// Only .zip assets are considered. Upstream publishes .rar and .tar.gz beside
// the zip, and the installer reads zip alone — picking an archive this program
// cannot open would turn every update into a failure that looks like a network
// problem.
func LatestRelease(ctx context.Context, client *http.Client) (Release, error) {
	if client == nil {
		client = defaultClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseAPI, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("zapret: не проверить обновление: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("zapret: проверка обновления вернула %s", resp.Status)
	}

	var payload struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	// Bound the body too: the size limit belongs on every response read, not only
	// on the archive.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		return Release{}, fmt.Errorf("zapret: не разобрать ответ об обновлении: %w", err)
	}
	// A draft or pre-release is not what an automatic update should install on
	// someone's machine unasked.
	if payload.Draft || payload.Prerelease {
		return Release{}, errors.New("zapret: последний релиз — черновик или пре-релиз")
	}

	rel := Release{Version: strings.TrimSpace(payload.TagName)}
	for _, a := range payload.Assets {
		if strings.EqualFold(filepath.Ext(a.Name), ".zip") {
			rel.ArchiveURL, rel.Size = a.URL, a.Size
			break
		}
	}
	if rel.Version == "" || rel.ArchiveURL == "" {
		return Release{}, errors.New("zapret: в релизе нет .zip-сборки")
	}
	return rel, nil
}

// defaultClient is the HTTP client used when the caller supplies none. The
// timeout covers a slow mirror without letting a stuck connection hold an
// update check open forever.
func defaultClient() *http.Client {
	return &http.Client{Timeout: 90 * time.Second}
}

// Apply downloads a release and replaces the bundle at dir with it.
//
// The replacement is staged: the archive is unpacked beside the live bundle and
// verified (Install refuses anything without strategies and bin/winws.exe), and
// only then swapped in. A download that arrives truncated, or a "release" that
// is not a bundle, therefore leaves the working installation untouched — the
// alternative, unpacking over the live directory, turns one bad night at the
// mirror into a machine with no bypass at all.
//
// The caller must stop winws first. On Windows a running executable pins its
// directory, so the swap fails while a strategy is up — and installing a new
// bypass under a running old one is not something to do quietly anyway.
func Apply(ctx context.Context, client *http.Client, dir string, rel Release) error {
	if rel.ArchiveURL == "" {
		return errors.New("zapret: нечего скачивать")
	}
	if client == nil {
		client = defaultClient()
	}

	archive, err := download(ctx, client, rel.ArchiveURL)
	if err != nil {
		return err
	}
	defer os.Remove(archive)

	staging := dir + ".new"
	defer os.RemoveAll(staging)
	if _, err := Install(archive, staging); err != nil {
		return err
	}
	// Carry over the files the user (and this app) own, so an update does not
	// silently discard a domain someone added by hand or the node addresses kept
	// out of the packet filter.
	carryUserFiles(dir, staging)
	if err := WriteVersion(staging, rel.Version); err != nil {
		return err
	}
	// Upstream ships its own update checker, which every strategy batch invokes on
	// launch. With this updater in place that is a second mechanism fetching the
	// same releases on its own schedule; disabling it leaves exactly one thing
	// deciding what version is installed.
	_ = os.Remove(filepath.Join(staging, "utils", "check_updates.enabled"))

	return swap(dir, staging)
}

// userFiles are the bundle files that carry local state rather than upstream
// content: the user's own domain additions and exclusions, and the node
// addresses ExcludeNodes writes.
var userFiles = []string{
	filepath.Join("lists", "list-general-user.txt"),
	filepath.Join("lists", "list-exclude-user.txt"),
	filepath.Join("lists", "ipset-exclude-user.txt"),
}

// carryUserFiles copies local state from the installed bundle into the staged
// one. Missing files are skipped: a fresh install has none.
func carryUserFiles(oldDir, newDir string) {
	for _, rel := range userFiles {
		data, err := os.ReadFile(filepath.Join(oldDir, rel))
		if err != nil {
			continue
		}
		dst := filepath.Join(newDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		_ = os.WriteFile(dst, data, 0o644)
	}
}

// swap moves the staged bundle into place, keeping the old one until the move
// succeeds so a failure can be undone.
func swap(dir, staging string) error {
	previous := dir + ".old"
	_ = os.RemoveAll(previous)

	if _, err := os.Stat(dir); err == nil {
		if err := os.Rename(dir, previous); err != nil {
			return fmt.Errorf("zapret: не отодвинуть старую сборку (обход ещё запущен?): %w", err)
		}
	}
	if err := os.Rename(staging, dir); err != nil {
		// Put the working bundle back rather than leaving the user with none.
		if rbErr := os.Rename(previous, dir); rbErr != nil {
			return fmt.Errorf("zapret: обновление не встало и старая сборка не вернулась (%v): %w", rbErr, err)
		}
		return fmt.Errorf("zapret: не установить новую сборку: %w", err)
	}
	_ = os.RemoveAll(previous)
	return nil
}

// download fetches url into a temporary file and returns its path. The caller
// removes it.
func download(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("zapret: не скачать сборку: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("zapret: скачивание вернуло %s", resp.Status)
	}

	tmp, err := os.CreateTemp("", "tenebra-zapret-update-*.zip")
	if err != nil {
		return "", err
	}
	path := tmp.Name()

	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxArchiveBytes))
	closeErr := tmp.Close()
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("zapret: обрыв при скачивании: %w", err)
	}
	if closeErr != nil {
		os.Remove(path)
		return "", closeErr
	}
	if n == 0 {
		os.Remove(path)
		return "", errors.New("zapret: скачался пустой файл")
	}
	if n >= maxArchiveBytes {
		os.Remove(path)
		return "", errors.New("zapret: сборка неправдоподобно большая — скачивание прервано")
	}
	return path, nil
}
