# Local macOS installer

Build from the same checkout/version as the signed app:

```sh
scripts/macos/build-pkg.sh --app /absolute/path/Tenebra.app --output /absolute/path/Tenebra.pkg --go /absolute/path/pinned-go/bin/go --arch arm64
```

`--arch` defaults to `arm64`; `--arch universal` requires both `arm64` and
`x86_64` in the GUI, core and engine, builds the checker once per architecture,
and combines its slices with `lipo`. The generated Installer Distribution
allows only the selected architectures. The app and output paths must be
absolute, and the output must not already exist. The selected Go executable
must be the exact version pinned by this checkout's `.go-version`, running on
either a native `darwin/arm64` or `darwin/amd64` host. An explicit `--go` is
recommended; otherwise `TENEBRA_GO` or the local `go` command is resolved.
The builder sets `GOTOOLCHAIN=local` and never downloads a Go toolchain.

The builder verifies the app seal, every required executable slice's ad-hoc
signature, all three rule sets, notices and matching source/Info.plist release
and build versions. It inspects core build metadata on private thin copies,
checking the exact compiler, module path, `GOOS=darwin` and the appropriate
`GOARCH` separately for every required slice; it never executes the core.
The version comes from the source maintained by `scripts/set-version.mjs`,
so later stable releases such as `0.6.2` require no package-script edit.
It compiles a read-only status checker with the same pinned Go. `pkgbuild` gives
the installed payload root ownership without requiring an elevated build. The
builder also normalizes the Scripts archive to uid 0/gid 0, because pkgbuild's
ownership option applies only to Payload. The pinned sing-box binary requires
macOS 15.0, which is the installer floor. Load commands for every required slice
of the GUI, core, engine and newly built checker must identify macOS and require
no later than 15.0. Compatible lower binary minima are accepted. A future engine
that needs a higher OS version fails packaging until the declared floor is
deliberately reviewed. Use the clean-source distribution builder documented in
[`BUILDING.md`](BUILDING.md) for app, package and
provenance generation together.

Open the resulting PKG in macOS Installer and authenticate using its ordinary
administrator dialog. The application runs as the signed-in user; launchd runs
the existing core plist as root. Installation is limited to the running startup
volume and `/Applications/Tenebra.app`, with bundle relocation disabled. The
core, engine, rule sets and checker live in
`/Library/PrivilegedHelperTools/Tenebra`.

The package and executables have no Developer ID or Apple notarization. Ad-hoc
signatures check integrity, not publisher identity. Gatekeeper may require its
normal per-file local approval; no global security setting is changed.

Before unloading an existing service, preinstall saves only the managed app,
helper files and plist, plus prior launchd loaded state, into root-only
`/Library/PrivilegedHelperTools/.Tenebra-install-backup`. It preserves app
ownership and metadata and never reads or modifies the profile/settings store.
Postinstall requires a correlated `status` response with `ok: true` and matching
`daemon_version`; merely having a socket is insufficient. Start/readiness
failures restore the prior executable payload and prior loaded/unloaded state.
Unknown helper files and service logs are preserved. The backup is removed
only after successful readiness or successful rollback.

A power loss, process kill, or Installer payload-unpack failure between scripts
cannot be made atomic using ordinary package preinstall/postinstall callbacks.
In that case the root-only backup remains. A later install refuses to overwrite
it so an administrator can inspect and recover it. Likewise, a failed rollback
retains its backup and reports failure rather than claiming restoration.
Restarting a prior loaded service restores registration, not its former live
tunnel/session; persisted autoconnect settings remain authoritative.

Local, nonprivileged checks:

```sh
GOTOOLCHAIN=local GOMAXPROCS=4 /absolute/path/pinned-go/bin/go test -race ./cmd/tenebra-healthcheck
python3 packaging/macos/test_installer.py
```

Installer transaction tests use disposable directory roots and a fake launchctl.
Packaging tests exercise the production validators with temporary future-version
and Go-pin fixtures. When native Go and Apple command-line tools are available,
they also compile a tiny temporary Go fixture and inspect real thin/universal
Mach-O metadata and signatures. No test installs the app, runs a built core,
changes launchd state or needs root. Builds do not establish readiness after
install, reboot, sleep or Terminal closure; validate those behaviors against the
fixed candidate using the native acceptance checklist in the build guide.
