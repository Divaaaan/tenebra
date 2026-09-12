# Test signed desktop bytes before reserving a stable version

The existing tag-triggered release workflow and `TENEBRA_RELEASE_HOLD` remain
available. The manual **Signed desktop candidate** workflow provides a second
path: build signed files without a tag or release, test those exact files, then
promote the same bytes. A failed native test can be fixed in a new commit and a
new preparation run without moving a tag or consuming `v0.6.0`.

## Launch requirements

The new workflow must first be reviewed and merged into the repository's default
branch (`main`). GitHub will not offer a new `workflow_dispatch` workflow that
exists only in this feature branch. Dispatch with `--ref main`; `source_sha`
must equal the full main commit selected by that dispatch. This check applies
to both preparation and promotion. If main changes after preparation, prepare
and accept a new candidate. No premature stable tag is a workaround.

The existing `TAURI_SIGNING_PRIVATE_KEY` and
`TAURI_SIGNING_PRIVATE_KEY_PASSWORD` secrets are used only by preparation's
signing steps. Preparation has `contents: read`; promotion has `contents: write`
and `actions: read`, with no signing key. The two workflows share the
`tenebra-release` concurrency group. Neither workflow is started merely by
pushing this feature branch.

The signatures here are Tauri/minisign updater signatures. They do not provide
Windows Authenticode trust or macOS notarization. Existing platform setup
limitations still apply.

## Prepare

From the reviewed, exact main checkout (PowerShell example):

```powershell
$source = git rev-parse HEAD
gh workflow run desktop-candidate.yml --ref main -f mode=prepare -f source_sha=$source
```

The workflow first runs the full existing CI suite, then builds Windows NSIS,
macOS universal DMG/app updater, Linux deb/AppImage and Arch pkgrel 1. All six
files receive signatures, including DMG and Arch as additional downloadable
signatures. The Arch candidate uses `TENEBRA_SOURCE_COMMIT=<full SHA>`;
ordinary `makepkg` retains its existing `v${pkgver}` source tag by default.

Assembly cryptographically verifies both the artifact signature and minisign's
authenticated comment against the public key in the exact source commit. It
requires every platform file and five existing core build reports with the
pinned Go version, target, clean VCS state and exact revision. It creates one
`tenebra-signed-desktop-<run ID>-<attempt>` Actions artifact, with 18 files and
`candidate.json`. Its summary reports the artifact ID and archive SHA256. Runs
that are missing a platform cannot produce the final artifact.

`candidate.json` records source commit/tree, workflow/run/attempt, Go version,
every file's size/SHA256 and the final stable updater URLs. It is data, not an
acceptance claim. Build reports identify the recorded CI core builds; they do
not prove that every packaged executable was extracted and run. Native
acceptance must independently verify installed/extracted payload identity.

Version-specific notes come from `git show HEAD:docs/releases/<version>.md`.
Their bytes are part of the candidate and acceptance file list, are repeated
in the updater manifest, and become the release body with its ownership marker.
Inspection and promotion compare them to the same source commit. Missing notes
or later edits to the release body fail closed; no network-generated notes are
added during publication.

Do not rerun only failed jobs across attempts: final assembly accepts the four
parts from the current attempt only. Rerun all jobs to produce a new candidate.
Do not rerun a preparation after its artifact has been accepted; a changed run
attempt invalidates the old acceptance. Artifacts expire after 30 days.

## Download and accept

An operator obtains the exact run ID, attempt, source SHA, artifact ID and
`sha256` digest from the completed GitHub run/API. Save these as a JSON request
with numeric `prepareRunId`, `prepareAttempt`, `artifactId`, and string
`sourceSha`, `artifactSha256`. No secret belongs in this request.

The read-only inspector requires Node 24, `unzip`, a read-capable
`GITHUB_TOKEN`/`GH_TOKEN`, and a checkout at that exact source commit:

```sh
node scripts/candidate-github.mjs inspect request.json fresh-candidate-directory
```

It checks the actual successful main dispatch, workflow path, run attempt,
artifact ownership, GitHub archive digest, downloaded archive digest, exact
commit/tree, all file hashes and all six cryptographic signatures. ZIP entries
are read into buffers and must be unique flat filenames. No application is
executed. It writes the files, `candidate.json` and `acquisition.json` to a new
directory; it refuses directory reuse.

Perform the native install/service/ordinary UI/tunnel/protection checks on the
signed candidate, retaining private detailed evidence and extracted/installed
GUI/core/engine hashes. Neither an earlier unsigned CI run nor an equal Git
tree can stand in for those bytes: Go embeds the actual VCS revision, and the
bundles are rebuilt.

After actual acceptance, create a separate JSON object with this contract:

- `schema: 1`, `kind: "tenebra-desktop-acceptance"`, `state: "pass"`;
- exact `version`, `sourceSha`, `sourceTree`, `prepareRunId`, `prepareAttempt`;
- `artifactId`, archive `artifactSha256`, actual `manifestSha256` from acquisition;
- `files`: the complete ordered file/size/SHA256 list from `candidate.json`;
- `evidence`: redacted `{kind, sha256}` references, including a combined actual
  `windows-install-service-ui-tunnel-protection` report. Additional platform
  evidence can be referenced in the same array.

This JSON is an explicit operator attestation, not a machine inference from a
green build. The promotion code validates its identity, hashes and required
native evidence category; the operator remains responsible for reviewing the
referenced evidence. Private logs, subscriptions and credentials must not be
included: this compact acceptance JSON becomes a public release asset.

## Promote the same bytes

Dispatch `desktop-candidate.yml` on the same current main SHA with
`mode=promote`, the exact `source_sha`, and the acceptance JSON supplied through
the `acceptance_json` input. Use the GitHub UI or `gh workflow run --json`
with a JSON inputs file so quoting preserves the acceptance text. Promotion
redownloads the exact accepted artifact and repeats provenance, signatures,
all file hashes and acceptance checks before any release mutation.

It persists a draft ownership marker binding the candidate, artifact, source,
run and acceptance digests, then creates the immutable stable tag at that same
source SHA. No build or re-sign operation occurs. It uploads the accepted files
plus deterministic `latest.json`/`beta.json` and provenance/acceptance/intent
JSON. Each uploaded asset is read back and SHA-verified before publication.
Publication is followed by the existing public-asset/channel CAS checks.

Only the workflow's `GITHUB_TOKEN` performs these mutations. GitHub suppresses
new workflows triggered by that token's tag/release events, so promotion does
not rebuild the tag or trigger Winget/Android delivery. No separate Winget
dispatch is part of this path.

Interrupted promotion can be retried with exactly the same inputs. It resumes
only an owned draft/tag with matching intent and source; already uploaded bytes
are checked and skipped, and only missing assets are added. Mismatched assets,
unknown tags/releases or changed acceptance are never overwritten. A known,
already public complete release is verified without file mutation, then its
channel CAS can be completed if publication succeeded before an interruption.
An incomplete GitHub `starter` upload is retained and fails closed rather than
being deleted automatically. No force-push, retag or clobber path exists.
For a retained `starter` asset, first read the exact owned draft and retain its
intent and asset ID as evidence. A maintainer must separately approve removal
of that single incomplete asset, without touching the tag, notes or accepted
files. Then the same promotion inputs can upload the missing asset and resume.
The workflow does not perform that deletion or recover mismatched uploaded bytes.

## Verification scope

`node --test ".github/scripts/*.test.mjs" "scripts/*.test.mjs"` exercises actual
Ed25519/BLAKE2b signatures, malformed records, payload and provenance mutations,
strict archive/run/artifact identity, receipt substitution, Arch commit source
selection, file staging and simulated interruptions after draft/tag/upload/
publication. Network mutation is behind the injected API boundary; tests do
not create a GitHub release or run produced binaries. The hosted signing and
promotion workflow still requires its first real run after source review.
