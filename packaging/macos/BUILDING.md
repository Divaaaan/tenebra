# Building a macOS package from an exact commit

The local command and `.github/workflows/macos-package.yml` use the same build
pipeline. They create an unsigned Installer PKG containing ad-hoc signed
executables. They require no Apple Developer account, keychain, signing secrets,
root privileges, GUI session, installed daemon or VPN connection. The result is a
review artifact, not a notarized public release or proof of native acceptance.

Prepare an ordinary user build environment on macOS 15 or newer:

- Git and Xcode command line tools (including `lipo`, `codesign`, `pkgbuild`,
  `productbuild`, `xcrun` and the macOS SDK).
- Go at the exact version in `.go-version`; automatic toolchain switching is
  disabled. An explicit `--go` path allows a private toolchain without changing
  the global installation.
- Node 24 with npm. `--node` selects a private Node installation; its `bin`
  directory is placed first on `PATH` for npm and Tauri's frontend command.
- Rust/Cargo and the `aarch64-apple-darwin` target. Universal packages also need
  `x86_64-apple-darwin`. The script checks installed targets and installs nothing.
- Network access to the locked npm/Cargo/Go dependencies and the existing pinned
  resource URLs in `scripts/fetch-resources.sh`.

Commit all source changes first. Run from the selected checkout or worktree:

```sh
bash scripts/macos/build-distribution.sh \
  --arch arm64 \
  --output-dir /absolute/path/to/new-output-directory \
  --go /absolute/path/to/go \
  --node /absolute/path/to/node
```

`arm64` is the default. Use `--arch universal` for both Apple Silicon and Intel
slices. The output directory must not exist and its parent must exist. The
script rejects tracked changes, untracked files, inconsistent source versions,
and the wrong Go compiler before fetching resources. It never modifies source
versions. A future coherent `0.6.2` commit produces `Tenebra-0.6.2-macos-arm64.pkg`
(or `universal`); a `0.6.1` commit remains `0.6.1` and cannot establish that the
future fixes are present.

Add `--plan-json` to validate source, versions and the Go pin and print the exact
execution selection without building or creating an output directory. This
still requires Node 24 and the selected Go compiler. It is a preflight, not
native package validation.

The build clones the exact selected HEAD into a private, ordinary Git checkout.
A linked worktree has produced missing Go VCS metadata with the pinned compiler;
the normal clone retains `.git/` and the verifier requires the exact commit and
`vcs.modified=false`. Ignored files, stale resources and existing `node_modules`
from the original checkout are not copied. The script removes only its own
scratch clone and staged output after success or failure. A failed build does
not publish a partially populated final output directory.

Resources come from the existing checksum-pinned fetch script, including the
engine and SRS files. The engine is signed individually before Tauri bundles it.
The core is built with explicit target slices, `-trimpath`, `-buildvcs=true` and
at most four compiler jobs. `scripts/verify-core-build.mjs` checks each core's
Go version, source revision, target and clean VCS status. Universal cores are
merged with `lipo`. npm uses `ci`, Tauri uses its local locked CLI, Cargo uses
`--locked`, and `tauri.ci.conf.json` disables updater artifacts. Nested app
executables are ad-hoc signed before sealing and verifying the outer app. Each
final bundled core slice is verified again before invoking:

```sh
bash scripts/macos/build-pkg.sh \
  --app /absolute/path/to/Tenebra.app \
  --output /absolute/path/to/Tenebra-VERSION-macos-ARCH.pkg \
  --go /absolute/path/to/go \
  --arch arm64 # or universal
```

The package builder validates bundle versions, signatures, architecture slices
and minimum macOS floors and builds the privileged health checker. It does not
install the package. The compatibility floor remains macOS 15.0; the pinned
engine is one input to that floor. The package's launchd service owns the
privileged tunnel; the normal desktop application runs as the logged-in user.

The final directory contains:

- The versioned PKG.
- `core-build-*.json` and `core-packaged-*.json`, with exact embedded Go metadata
  and SHA-256 hashes before bundling and for the final app's core slices.
- `manifest.json`, with the source commit/tree, source lockfile hashes, compiler,
  npm, SDK and OS versions, bundled resource and app file hashes, package/report
  hashes, and `nativeAcceptance: "not_run"`.
- `SHA256SUMS`, covering every other output file, including the manifest. Verify
  it from inside the output directory with `shasum -a 256 -c SHA256SUMS`.

The source and dependency versions are selected explicitly; this is not a claim
of byte-for-byte reproducibility across SDK or Rust compiler versions. The
workflow currently uses Rust stable and records its exact version. Go/npm
content caches may be reused by their toolchains; no prior build's target
folder, app, package or generated resources are accepted as build input.

## CI and release integration

The standalone workflow supports manual dispatch and reusable workflow calls.
Supply a full lowercase source commit SHA from this repository and `arm64` or
`universal`. It checks out and validates exactly that SHA, uses Node 24 and Go
from `.go-version`, builds with the same command and uploads only review
artifacts. Official Actions are pinned to full commit SHAs; repository token
permissions are `contents: read`. It has no publish step, release tag, updater
channel write or signing secrets.

Existing release, candidate and publisher workflows are intentionally unchanged.
An accepted future release can call this reusable workflow for package evidence,
or invoke the same local command from its own checked commit. Acceptance and
publication remain explicit separate decisions; an uploaded PKG never promotes
itself. See [the integration and acceptance gates](../../docs/macos-release-integration.md)
for the native Terminal-close, GUI-close, service restart, reboot, sleep, TUN,
DNS, IPv6, reconnect and upgrade checks that still require a fixed candidate.

Run the focused pipeline protocol tests without compiling the desktop app:

```sh
/path/to/node24 --test scripts/macos/build-distribution.test.mjs
```

They execute the production pipeline in temporary Git fixtures at version
`0.6.2`, replacing native/compiler boundaries only. They cover clean linked
worktree snapshots, version and Go rejection, arm64/universal selection,
per-executable signing order, artifact hashes and cleanup after failure. They do
not substitute for an actual native build or install/upgrade acceptance.
