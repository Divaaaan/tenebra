package zapret

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// SHA256 is the archive's checksum as published beside it, lowercase hex.
	// GitHub computes this itself when the asset is uploaded, so it is not a
	// number the publisher types — but it arrives over the same connection as the
	// archive, so it is never trusted on its own: it can only agree or disagree
	// with the pin baked in for that version. A disagreement is a tamper signal
	// (see expectedArchiveSum); an unpinned version is refused regardless of what
	// this field says (see ErrUntrustedVersion).
	SHA256 string `json:"sha256,omitempty"`
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
			// Digest is GitHub's own SHA-256 of the uploaded asset, published as
			// "sha256:<hex>". It rides the same TLS connection as the archive, so it
			// is only ever cross-checked against the pin baked in for the version —
			// never trusted as the sole authority. Upstream publishes no checksum or
			// signature file of its own (no SHA256SUMS, no minisign/gpg, nothing in
			// the notes), so there is no out-of-band anchor; an unpinned version is
			// reported and left uninstalled (see verify.go).
			Digest string `json:"digest"`
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
			rel.ArchiveURL, rel.Size, rel.SHA256 = a.URL, a.Size, normalizeSum(a.Digest)
			break
		}
	}
	if rel.Version == "" || rel.ArchiveURL == "" {
		return Release{}, errors.New("zapret: в релизе нет .zip-сборки")
	}
	// Refuse the release here rather than at download time. This is the point
	// where the answer is still just a fact about a feed, and saying "the release
	// page is pointing somewhere it should not" is a far more useful thing to have
	// in a log than a download that failed later for a reason nobody kept.
	if err := checkArchiveURL(rel.ArchiveURL); err != nil {
		return Release{}, err
	}
	if rel.Size <= 0 {
		return Release{}, fmt.Errorf("%w: релиз %s не сообщает размер сборки", ErrIntegrity, versionLabel(rel.Version))
	}
	// A pinned version whose feed publishes a contradicting sum is refused here,
	// where the answer is still a fact about the feed rather than a failed
	// download. An UNPINNED version is deliberately NOT refused here: under
	// Variant A it is a legitimate "there is something newer than this client
	// trusts", which the caller reports and declines to install — so it has to
	// survive parsing with its version and URL intact, to be named in that notice.
	if _, err := expectedArchiveSum(rel); err != nil && !errors.Is(err, ErrUntrustedVersion) {
		return Release{}, err
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
// The archive is verified before anything is unpacked: it may only come from the
// release host over https, its length has to match the release, and its SHA-256
// has to match the sum pinned into this client for that version. A version this
// client does not pin is refused with ErrUntrustedVersion — the digest published
// beside the asset is not accepted in a pin's place, because it rides the same
// connection as the archive. A mismatch is refused and nothing is installed — see
// verify.go for why this matters more here than for an ordinary download.
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

	// Decide what the archive has to be before fetching it. Both answers are
	// refusals the user can act on, and neither of them needs a download first.
	if err := checkArchiveURL(rel.ArchiveURL); err != nil {
		return err
	}
	wantSum, err := expectedArchiveSum(rel)
	if err != nil {
		return err
	}

	archive, err := download(ctx, client, rel, wantSum)
	if err != nil {
		return err
	}

	staging := dir + ".new"
	defer os.RemoveAll(staging)
	// Unpack the exact bytes that download hash-verified, straight from memory.
	// Writing the archive to a temp file and re-opening it to install would open a
	// window between the check and the install (a TOCTOU: another local process
	// could swap the file's contents in between), and the bundle becomes a .bat
	// this service hands to cmd.exe as LocalSystem — the one place that window
	// must not exist. What was hashed is what is installed, with no disk round
	// trip in between.
	if _, err := InstallFromArchive(archive, staging); err != nil {
		return err
	}
	// Carry over the files the user (and this app) own, so an update does not
	// silently discard a domain someone added by hand or the node addresses kept
	// out of the packet filter.
	carryUserFiles(dir, staging)
	if err := WriteVersion(staging, rel.Version); err != nil {
		return err
	}
	// The bundle's own update checker is already off: Install disables it on every
	// install path (see disableBundleUpdater), this one included.

	return swap(dir, staging)
}

// userFiles are the bundle files that carry local state rather than upstream
// content: the user's own domain additions and exclusions, the node addresses
// Excluder.Exclude writes, and the memory it writes them from — an update is a
// restart of the bypass, and losing that memory there would mean the first
// connect after an update is the one that has to resolve everything again.
var userFiles = []string{
	filepath.Join("lists", "list-general-user.txt"),
	filepath.Join("lists", "list-exclude-user.txt"),
	filepath.Join("lists", excludeFile),
	filepath.Join("lists", nodeCacheFile),
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

// download fetches the release archive into memory and returns the verified
// bytes, having checked that what arrived is the archive the release describes:
// rel.Size bytes long, and hashing to wantSum.
//
// The bytes are held in memory rather than a temp file so the caller can unpack
// the exact object that was hashed — see Apply. The hash is computed while the
// body is being read, so the buffer that is returned is the buffer that was
// summed; there is no re-read of anything that could have changed. The bundle is
// a couple of megabytes and the ceiling is 64 MiB, so a buffer costs nothing a
// temp file would not.
func download(ctx context.Context, client *http.Client, rel Release, wantSum string) ([]byte, error) {
	// The declared size is checked before the transfer as well as after it: a
	// release claiming more than the ceiling is refused without spending the
	// bandwidth to find out.
	if rel.Size <= 0 {
		return nil, fmt.Errorf("%w: релиз %s не сообщает размер сборки", ErrIntegrity, versionLabel(rel.Version))
	}
	if rel.Size >= maxArchiveBytes {
		return nil, fmt.Errorf("%w: сборка заявлена на %d байт — это не сборка обхода", ErrIntegrity, rel.Size)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.ArchiveURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := guardRedirects(client).Do(req)
	if err != nil {
		// A redirect refused by the guard arrives here wrapped in a *url.Error, and
		// it is a refusal rather than a network fault — unwrap it so the caller sees
		// which of the two happened.
		if errors.Is(err, ErrIntegrity) {
			return nil, err
		}
		return nil, fmt.Errorf("zapret: не скачать сборку: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zapret: скачивание вернуло %s", resp.Status)
	}

	var buf bytes.Buffer
	buf.Grow(int(rel.Size)) // rel.Size is > 0 and < maxArchiveBytes, checked above
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(&buf, sum), io.LimitReader(resp.Body, maxArchiveBytes))
	if err != nil {
		return nil, fmt.Errorf("zapret: обрыв при скачивании: %w", err)
	}
	if n == 0 {
		return nil, errors.New("zapret: скачался пустой файл")
	}
	if n >= maxArchiveBytes {
		return nil, errors.New("zapret: сборка неправдоподобно большая — скачивание прервано")
	}
	if err := verifyArchive(n, rel, wantSum, hex.EncodeToString(sum.Sum(nil))); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// verifyArchive compares what arrived with what the release promised.
//
// Length first, because it is the cheaper answer and the more legible one: a
// truncated download and a swapped archive both fail the checksum, and only the
// length says which of the two happened.
func verifyArchive(n int64, rel Release, wantSum, gotSum string) error {
	if n != rel.Size {
		return fmt.Errorf("%w: размер не совпал — заявлено %d байт, скачано %d", ErrIntegrity, rel.Size, n)
	}
	if gotSum != wantSum {
		return fmt.Errorf("%w: контрольная сумма не совпала — ждали %s, скачали %s", ErrIntegrity, wantSum, gotSum)
	}
	return nil
}

// guardRedirects returns a copy of client whose redirects are held to the same
// host policy as the original URL.
//
// A release URL on github.com redirects to the asset storage, so redirects have
// to be followed — but the hop is where "an address from a feed" turns into "an
// address from a header", and an unchecked 302 to plain http elsewhere would put
// back exactly the hole the check on the first URL closes. The client is copied
// rather than modified because it belongs to the caller.
func guardRedirects(client *http.Client) *http.Client {
	guarded := *client
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// net/http's own default, kept because replacing CheckRedirect removes it.
		if len(via) >= 10 {
			return errors.New("zapret: слишком много перенаправлений при скачивании")
		}
		return checkArchiveURL(req.URL.String())
	}
	return &guarded
}
