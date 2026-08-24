package zapret

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrIntegrity marks every refusal to install a bundle whose bytes this code
// could not vouch for: an archive offered from somewhere other than the release
// host, a length that disagrees with the release, a checksum that does not match
// the sum pinned for that version, or a pinned version whose feed publishes a
// different sum than the one baked in.
//
// It is a sentinel rather than a plain error because the callers upstream treat
// it differently from a failed download. A network that is down is an ordinary
// event, logged quietly and retried in twelve hours. An archive that arrived and
// did not match is not: something between the release page and this machine
// changed the bytes, and the bytes in question become a .bat that cmd.exe runs as
// LocalSystem. That has to be loud.
var ErrIntegrity = errors.New("zapret: сборка не прошла проверку целостности")

// ErrUntrustedVersion marks a published bundle newer than any checksum pinned
// into this client. It is deliberately NOT an ErrIntegrity: nothing was tampered
// with, there is simply no baked-in sum to verify the archive against.
//
// The only checksum upstream offers is the digest GitHub publishes beside each
// asset, and it travels the same TLS connection as the archive it describes — so
// a proxy whose root the machine trusts (the very adversary this product exists
// for) can rewrite both to agree. Auto-installing on the strength of that digest
// would mean running unverified code as LocalSystem, which is the whole thing
// being prevented. The safe answer is to report that a newer bundle exists and
// keep the working one until a Tenebra build that carries the new pin ships. See
// pinnedArchives, and the control layer, which turns this into a "there is an
// update, update Tenebra" notice rather than an alarm.
var ErrUntrustedVersion = errors.New("zapret: версия сборки новее вшитых в клиент проверок — обновите Tenebra")

// archiveHosts are the exact hosts a bundle archive may be fetched from: the
// release page itself and the asset storage it redirects to.
//
// Kept to specific GitHub-owned names on purpose. A wildcard over
// githubusercontent.com would also accept raw.githubusercontent.com and
// gist.githubusercontent.com, which serve arbitrary user content anyone can
// publish to — precisely the kind of place a bundle that becomes SYSTEM-run code
// must never come from. The download URL redirects to
// release-assets.githubusercontent.com today; objects.githubusercontent.com was
// the target before it and is kept so a rollback on GitHub's side does not break
// updates. Both are checksum-gated regardless (see expectedArchiveSum), so this
// list is the coarse "is this even GitHub" filter, not the whole defence.
var archiveHosts = []string{
	"github.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
}

// archiveURLAllowExtra, when non-nil, lets a test accept one extra origin — its
// own httptest server — without turning the host policy off, so the policy stays
// exercised for every other host.
//
// It is a function rather than a string on purpose: `-ldflags -X` can set a
// package-level string variable at link time but not a function value, so this
// hook cannot be used to point a release build at another origin the way a
// settable origin string could. It is assigned only from _test.go files, so a
// shipped binary always leaves it nil.
var archiveURLAllowExtra func(*url.URL) bool

// checkArchiveURL reports whether raw is somewhere this program is willing to
// fetch executable content from.
//
// The URL arrives inside a JSON document from the network, and everything
// downstream treats it as trusted: the client follows its redirects, the archive
// is unpacked into the daemon's directory, and a batch file out of that archive
// is handed to cmd.exe by a service running as LocalSystem. "Whatever the feed
// said" is not an acceptable answer to "where does this code come from", and
// plain http is not an acceptable answer to "who could have written it".
func checkArchiveURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%w: адрес сборки не разобрать (%v)", ErrIntegrity, err)
	}
	if archiveURLAllowExtra != nil && archiveURLAllowExtra(u) {
		return nil
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("%w: сборку предлагают скачать по %s, а не по https (%s)", ErrIntegrity, u.Scheme, raw)
	}
	// Credentials in a download URL are not something a release page publishes,
	// and "https://github.com@evil.example/" is how a host check is fooled by
	// eye rather than by parser.
	if u.User != nil {
		return fmt.Errorf("%w: в адресе сборки есть логин — это не адрес релиза (%s)", ErrIntegrity, raw)
	}
	if !archiveHostAllowed(u.Hostname()) {
		return fmt.Errorf("%w: сборку предлагают скачать с %s, а не с github.com (%s)", ErrIntegrity, u.Hostname(), raw)
	}
	return nil
}

// archiveHostAllowed reports whether host is one of archiveHosts.
//
// The match is exact, after lowercasing and dropping a trailing dot (a legal way
// to write an absolute name that would otherwise slip past equality). archiveHosts
// carries no wildcards, so a lookalike label such as github.com.evil.example or
// evil-githubusercontent.com cannot match.
func archiveHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	for _, allowed := range archiveHosts {
		if host == allowed {
			return true
		}
	}
	return false
}

// pinnedArchives are the bundle releases whose archives this project has hashed
// itself, keyed by upstream tag. Only a version listed here is auto-installed.
//
// Why the pin is the whole authority, not a supplement to the feed: the release
// feed, the digest GitHub publishes beside each asset, and the archive itself all
// travel the same TLS connection, so anything able to forge that connection — a
// corporate or censor proxy whose root the machine trusts, which on the networks
// this app is built for is not hypothetical — can rewrite all three to agree. A
// sum compiled into the binary is the one reference that is not on that
// connection. It is also public and reproducible: anyone can download the release
// and check these numbers, which the API's own digest field does not offer.
//
// A version absent from this table is NOT installed from the feed's digest (the
// same-connection value just described). The updater reports that a newer bundle
// exists (see ErrUntrustedVersion) and leaves the working one in place; the pin
// lands in the next Tenebra release. This is the deliberate cost of the design: a
// bundle can trail upstream by one client release rather than have a MITM install
// arbitrary code as SYSTEM. Refresh it when cutting a release — download the asset
// from more than one vantage point, confirm the sums agree, then add the version:
//
//	curl -sL -o b.zip https://github.com/Flowseal/zapret-discord-youtube/releases/download/<tag>/zapret-discord-youtube-<tag>.zip
//	sha256sum b.zip
//
// Upstream ships no signature or checksum file of its own — verified against the
// release feed: recent releases carry only .zip/.rar/.tar.gz, with no minisign,
// gpg or SHA256SUMS asset and nothing in the notes — so there is no out-of-band
// anchor to verify a new version against automatically. A human pinning it is the
// anchor.
var pinnedArchives = map[string]string{
	"1.10.2": "5eaac9fb2e4b1abd693487452a3ff3f4dfe9578a45f9ddddfa4bc1f5a6bb62d5",
	"1.10.1": "f748d61fec75e4edc992cb5b09d554e914197c68c690384aceb61f143d8f76c9",
	"1.10.0": "6b7c5a66cfd055b8e361f8b5fb00f00b167260f21b1c03d589f6008417fb94a2",
}

// PinnedSum returns the checksum baked into this client for a bundle version, or
// "" when the version carries no pin and so cannot be auto-installed.
//
// The control layer calls it to tell "install this" from "report it and wait for
// a Tenebra update" before it stops the running bypass or fetches anything — the
// trust decision belongs beside the pin table, not spread across callers.
func PinnedSum(version string) string {
	return pinnedArchives[strings.TrimPrefix(strings.TrimSpace(version), "v")]
}

// expectedArchiveSum reports the SHA-256 the downloaded archive must have, and
// refuses when this client cannot vouch for the version.
//
// The pin is the only source of that answer. A version with no pin is refused
// with ErrUntrustedVersion — not installed from the digest published beside the
// asset, which rides the same connection as the archive and so proves nothing
// against a forged-TLS adversary. When both a pin and a published digest exist
// and disagree, that is a tamper-or-reupload signal worth a person's eyes, so it
// is refused as ErrIntegrity rather than resolved in either direction.
//
// Fail-closed is the whole point. On a release this client does not pin, the
// bypass simply does not update, the tunnel keeps working without it, the log
// says why and names the version, and dropping the archive in by hand still
// works. That is a smaller loss than auto-running unverified code as LocalSystem.
func expectedArchiveSum(rel Release) (string, error) {
	version := strings.TrimPrefix(strings.TrimSpace(rel.Version), "v")
	pinned := pinnedArchives[version]
	if pinned == "" {
		return "", fmt.Errorf("%w (%s)", ErrUntrustedVersion, versionLabel(rel.Version))
	}
	if published := normalizeSum(rel.SHA256); published != "" && published != pinned {
		return "", fmt.Errorf(
			"%w: релиз %s опубликован с суммой %s, а вшитая сумма этой версии — %s",
			ErrIntegrity, rel.Version, published, pinned)
	}
	return pinned, nil
}

// versionLabel renders an empty version readably inside a refusal.
func versionLabel(v string) string {
	if strings.TrimSpace(v) == "" {
		return "без номера"
	}
	return v
}

// normalizeSum accepts a checksum the way GitHub publishes it ("sha256:<hex>")
// as well as bare hex, and returns lowercase hex — or "" for anything that is
// not a SHA-256.
//
// Anything unrecognised becomes "" rather than an error so it lands in the same
// branch as "nothing published": a malformed digest is exactly as unusable as a
// missing one, and giving the two different outcomes would only add a way for the
// stricter path to be skipped.
func normalizeSum(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) != 64 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return s
}
