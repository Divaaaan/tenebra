# Development guide

Everything you need to build, run and test Tenebra from a fresh checkout. For
*what* the pieces are, read [architecture.md](architecture.md); for the core ↔ UI
wire format, [control-protocol.md](control-protocol.md). This document is the
*how*.

## Prerequisites

| Tool | Version | Used for |
|------|---------|----------|
| [Go](https://go.dev/dl/) | 1.24+ | the core and the `tenebra-core` sidecar |
| [Node.js](https://nodejs.org/) | 22+ (with npm) | the React front end |
| [Rust](https://rustup.rs/) (stable) | latest stable | the Tauri desktop shell |
| PowerShell | Windows built-in / [PS 7+](https://github.com/PowerShell/PowerShell) | `scripts/fetch-resources.ps1` |

CI builds the core on Go 1.26 and the desktop bundle with Node 24, so those are
known-good; the minimums above are what `go.mod` and the front end actually
require. The desktop app builds for **Windows, macOS and Linux** (this guide is
written from the Windows side; the platform-specific parts are in
[porting/macos.md](porting/macos.md) and [porting/linux.md](porting/linux.md)),
and the Go core builds and tests on every platform — it deliberately avoids
OS-specific imports outside the adapters. The Android client has its own
toolchain (NDK + Gradle) and its own guide,
[porting/android.md](porting/android.md).

Tauri has its own platform prerequisites (a C toolchain, WebView2 on Windows).
If `npm run tauri build` complains about a missing system dependency, check the
[Tauri 2 prerequisites](https://v2.tauri.app/start/prerequisites/).

## Repository layout

```
core/           Go, platform-agnostic (Windows service/pipe plumbing aside):
  model/        normalized proxy node + config types
  subscription/ parse vless/hysteria2/ss/trojan/vmess links and subscription bodies
  profile/      named profiles and their atomic on-disk store (profiles.json)
  routing/      smart/global/direct + per-app split -> sing-box route/dns blocks
  singbox/      assemble a full sing-box config as plain JSON (no sing-box dependency)
  fallback/     pure REALITY->Hysteria2->AmneziaWG fallback state machine
  zapret/       drive the DPI-bypass bundle, embedded + downloaded (Windows only)
  logrot/       size-capped log writer; the Windows service logs through it
  control/      the line-delimited JSON protocol and the daemon that drives it
core-bridge/    the same config generator as a library for the mobile clients
mobile/         gomobile wrapper: binds core-bridge + libbox into one artifact
adapters/
  windows/      spawn & supervise the sing-box process; read traffic via its clash API
  macos/        the same under the root LaunchDaemon (utun)
  linux/        the same under the root systemd service (/dev/net/tun)
cmd/
  tenebra-core/ the core binary; protocol on stdin/stdout, a named pipe on Windows,
                or a unix socket on macOS/Linux
ui-desktop/
  src-tauri/    the Rust/Tauri shell (sidecar bridge, tray, autostart)
  src/          the React + TypeScript front end
ui-android/     Kotlin/Compose client (VpnService + libbox), alpha
ui-ios/         SwiftUI + Network Extension scaffold, never compiled
deploy/         the privileged daemon's service definitions per platform
packaging/arch/ PKGBUILD building core, app and unit for Arch Linux
scripts/
  fetch-resources.ps1  download the pinned sing-box binary and wintun.dll
  fetch-resources.sh   the same for macOS and Linux (plus the rule-sets)
  build-libbox-android.sh  one gomobile bind -> the fused tenebra.aar
  build-libbox.sh      the same bind for Apple platforms (xcframework)
  macos/, linux/       install/remove the privileged daemon there
```

## The Go core

The core's only third-party dependencies are **go-winio** and **x/sys**, both
Windows plumbing (the named-pipe transport and the service entry point);
everything else is standard library, and everything in it runs offline. It does
not link sing-box; it *generates* a sing-box config as plain JSON, which the
sidecar hands to a real sing-box process at runtime.

To exercise the named-pipe transport without installing a service, run the core
from a console with `go run ./cmd/tenebra-core --pipe` and connect a client to
`\\.\pipe\tenebra`; transports and the pipe ACL are documented in
[control-protocol.md](control-protocol.md#transports). The Windows-only paths
(`//go:build windows`) are covered by `go test` on a Windows machine — CI's
ubuntu/macos jobs compile around them.

Build and vet:

```
go build ./...
go vet ./...
```

Run the tests (this is the command CI runs, minus the flags below):

```
go test ./...
```

To mirror CI exactly — race detector on, no result caching:

```
go test ./... -race -count=1
```

CI also runs [`staticcheck`](https://staticcheck.dev/). To run it locally:

```
go install honnef.co/go/tools/cmd/staticcheck@latest
staticcheck ./...
```

Build just the sidecar (handy for poking at the protocol by hand):

```
go build -o tenebra-core ./cmd/tenebra-core      # or tenebra-core.exe on Windows
```

The sidecar reads the control protocol on **stdin** and writes responses and
events on **stdout**; all logs go to **stderr**. You can drive it from a
terminal — type one JSON object per line:

```
$ ./tenebra-core
{"id":1,"cmd":"status"}
{"id":1,"ok":true,"data":{"state":"idle"}}
```

It stores profiles under your per-user config dir by default
(`%AppData%\tenebra` on Windows, `~/.config/tenebra` on Linux,
`~/Library/Application Support/tenebra` on macOS). Override it with
`TENEBRA_CONFIG_DIR` to keep experiments out of your real config:

```
TENEBRA_CONFIG_DIR=./scratch ./tenebra-core
```

### Core environment variables

| Variable | Effect |
|----------|--------|
| `TENEBRA_CONFIG_DIR` | Directory for `profiles.json`, `settings.json`, `lastgood.json`. Defaults to the per-user config dir; as a Windows service, to `%ProgramData%\Tenebra\data`. |
| `TENEBRA_SINGBOX` | Path to the `sing-box` binary to run. Defaults to one resolved next to the executable; as a Windows service, to the bundled copy in `resources\` next to `tenebra-core.exe`. |
| `TENEBRA_LOG_LEVEL` | Log threshold: `debug`, `info` (default), `warn`, `error`. Read once at start-up, so raising it means restarting the core. It governs both the `log` events the UI shows and what reaches the process log on disk. |

### Logs

The core logs to stderr when it runs as a sidecar or in a console, and to
`%ProgramData%\Tenebra\service.log` as a Windows service. The service log
**rotates by size** (8 MiB per file, three rotated generations, 32 MiB for the
whole set — `core/logrot`); the desktop shell's `core.log` is rolled over at
spawn when it has grown past 8 MiB.

Debug is off by default, and deliberately not a stored preference: to investigate
something, set `TENEBRA_LOG_LEVEL=debug`, restart the core, reproduce, then unset
it. At `debug` the core narrates the reasoning behind each connect decision — the
resolved candidate order with measured round-trips, each transport strategy tried
and how its failure was classified, per-target node-probe stages, what the
tun-conflict guard saw in the route table.

For a report someone else has to read, use `collect_diagnostics` (Settings →
Diagnostics → *Save diagnostics report*): it writes one file with the state,
build versions, routes, last connect walk and log tail, with subscription tokens
and node credentials masked.

## The Windows service

On Windows the core also runs as a service named **`tenebra`**, serving the
control protocol on the named pipe so the tunnel outlives any one UI process.
The installer owns the service's lifecycle through NSIS hooks
(`ui-desktop/src-tauri/installer-hooks.nsh`):

- **Install** registers `tenebra` (`start=auto`, image path
  `$INSTDIR\tenebra-core.exe`) and starts it. Because a service needs
  machine scope, the installer is **per-machine** (`installMode:
  "perMachine"`) and asks for elevation once per install or update.
- **Update** stops the service before files are replaced, re-points the
  registration at the (possibly new) install directory, and starts it again.
  The registration itself survives updates.
- **Uninstall** stops and deletes the service.

Running as LocalSystem, the service keeps its state in
**`%ProgramData%\Tenebra`**: `service.log`, and the profile store under
`data\`. The store is created with a protected DACL — full control for SYSTEM
and Administrators, no access for anyone else — because profiles carry
subscription credentials; unprivileged users reach that data through the pipe
protocol (see [control-protocol.md](control-protocol.md#transports)), never
the files. Both locations are deliberately left behind on uninstall; delete
`%ProgramData%\Tenebra` by hand for a full wipe. To remove the service
manually: `sc.exe stop tenebra`, then `sc.exe delete tenebra` (elevated).

In service mode the core resolves the bundled `sing-box.exe` (with
`wintun.dll` and the `.srs` rule-sets beside it) from `resources\` next to
`tenebra-core.exe` — the installed layout — falling back to a flat
next-to-the-executable layout. The `TENEBRA_CONFIG_DIR` / `TENEBRA_SINGBOX`
variables still win if set machine-wide.

**Migration from the per-user installs (≤ 0.2.x).** Earlier releases
installed per-user into `%LOCALAPPDATA%\Tenebra` with their registration in
HKCU; the stock Tauri installer does not reconcile the two scopes. On the
first per-machine install the installer therefore retires the old copy for
the installing user: it removes the HKCU uninstall entry, the autostart Run
value, the per-user `tenebra://` handler (which would shadow the machine-wide
one) and the old shortcuts — registry and shortcut surgery only. It
deliberately does **not** execute the old uninstaller and does not delete
inside `%LOCALAPPDATA%\Tenebra`: that directory is user-writable, and an
elevated installer executing or recursively deleting through it would hand
its privileges to anything planted there. The leftover files are inert once
their entry points are gone; remove the directory by hand if the disk space
matters. Per-user profile stores (`%AppData%\tenebra`) are not migrated into
the service store — re-import the subscription in the app.

None of this changes development flows: a console `tenebra-core` (stdio or
`--pipe`) keeps its per-user paths, and `npm run tauri dev` still uses the
stdio sidecar.

## The desktop app

All desktop commands run from the `ui-desktop/` directory unless noted.

### 1. Fetch the bundled binaries

sing-box and `wintun.dll` are **not** committed — they are downloaded at pinned
versions so builds stay reproducible. Run this once (and again when the pinned
versions change):

```
powershell -File scripts/fetch-resources.ps1
```

It places `sing-box.exe` and `wintun.dll` in
`ui-desktop/src-tauri/resources/`, which Tauri bundles into the installer.

### 2. Build the core sidecar where Tauri expects it

Tauri loads the sidecar as an external binary named for the target triple. Build
it into `src-tauri/binaries/`:

```
go build -o ui-desktop/src-tauri/binaries/tenebra-core-x86_64-pc-windows-msvc.exe ./cmd/tenebra-core
```

(Replace the triple for another target, e.g.
`tenebra-core-aarch64-apple-darwin`.)

### 3. Install front-end dependencies

```
cd ui-desktop
npm install      # or `npm ci` for a clean, lockfile-exact install
```

### 4. Run in development

```
npm run tauri dev
```

This compiles the Rust shell, starts Vite, and opens the app. The shell tries to
spawn the real `tenebra-core` sidecar; if it can't (e.g. you skipped step 2), it
falls back to an in-process **mock backend** so the UI still runs. To force the
mock for UI-only work — no Go build, no sidecar:

```
# PowerShell
$env:TENEBRA_MOCK = "1"; npm run tauri dev
```

The mock serves believable fake profiles and state; it never touches sing-box or
a real tunnel.

### 5. Build the installer

```
npm run tauri build
```

CI builds the NSIS installer specifically:

```
npm run tauri build -- --bundles nsis
```

The output lands in `ui-desktop/src-tauri/target/release/bundle/`. The installer
is **unsigned** — code signing is not set up yet. It installs per-machine and
manages the `tenebra` Windows service via the hooks in
`src-tauri/installer-hooks.nsh` (see [The Windows service](#the-windows-service)),
so running it takes one UAC elevation. Building it does not.

### Front-end-only scripts

The front end has a vitest unit suite alongside the type-check gate.

| Command | What it does |
|---------|--------------|
| `npm run dev` | Vite dev server (UI only, no Tauri shell) |
| `npm test` | run the vitest unit suite once |
| `npm run test:watch` | vitest in watch mode |
| `npm run typecheck` | `tsc --noEmit` — the front-end type check |
| `npm run build` | type-check then build the front-end bundle to `dist/` |
| `npm run preview` | serve the built `dist/` locally |
| `npm run tauri <cmd>` | proxy to the Tauri CLI (`dev`, `build`, …) |

## Running the tests

There are three test surfaces. The first two are pure and run anywhere; the third
is the cross-language check of the wire protocol.

### Go unit tests

```
go test ./...
```

Roughly twenty `_test.go` files cover subscription parsing, the profile store,
routing/DNS and split-tunnel rule generation, the sing-box config builder, the
fallback walk, the control protocol and the leak-check logic, plus the Windows
runner's process and clash-API handling. All of it is offline — fixtures use
obviously fake hosts and keys.

### Front-end tests

```
cd ui-desktop
npm test          # vitest unit suite
npm run typecheck # tsc --noEmit
```

A vitest suite (jsdom, no real Tauri or network) covers the lib helpers, the
Tauri API client, the connection-state hook and the screens. The protocol types
in `src/api/types.ts` mirror the Go protocol by hand, so a type error here often
means the front end and the core have drifted.

### End-to-end: the real sidecar

`ui-desktop/src-tauri/tests/sidecar_e2e.rs` spawns the **real `tenebra-core`
binary** and round-trips the line-delimited JSON protocol over its stdin/stdout —
`status`, `import_link`, `list_profiles`, `set_split`, then `leak_check` — and
asserts the responses are well-formed, id-correlated, and normalized exactly as
the front end expects. It proves the Rust `SidecarBackend` and the Go core agree
on the wire format. It uses only fake links and an isolated temp store, so
**nothing dials out**.

It runs from `ui-desktop/src-tauri`:

```
cd ui-desktop/src-tauri
cargo test
```

The e2e **self-skips** (passes as a no-op) if the core binary hasn't been built,
so a fresh `cargo test` stays green. To make it run for real, build the sidecar
into `src-tauri/binaries/` first (step 2 above), then run `cargo test` again.

### What is *not* covered automatically

Standing up an actual tunnel — a tun device + a live sing-box dialing a real
server — is **not** in any automated test, on any platform. It needs
administrator rights (root on macOS and Linux) and real server credentials, so
it is done by hand; see [the maintainer notes](#known-gaps) for where that has
happened and where it has not.

## Coding conventions

- **English** for code, comments, identifiers, commit messages and docs.
- **Go:** keep the core **standard-library only** — no new module dependencies.
  Run `gofmt` (or `go fmt ./...`); keep `go vet ./...` and `staticcheck ./...`
  clean. No real servers, subscription URLs, node IPs or keys anywhere — tests
  use obviously fake data.
- **TypeScript:** keep `npm run typecheck` and `npm test` clean. Mirror any
  protocol change in `src/api/types.ts`.
- **Styling:** all colours, spacing, radii and motion go through the design
  tokens in `ui-desktop/src/styles/tokens.css` (CSS custom properties; dark is
  the default, light flips via `[data-theme="light"]`). Don't hard-code hex
  colours or magic pixel values in component styles.
- **i18n:** user-facing strings live in `ui-desktop/src/i18n/strings.ts` and must
  be provided for both supported languages (English and Russian); the shape is
  checked at compile time.
- **No telemetry, ever.** This is a VPN — see the hard rules in
  [architecture.md](architecture.md#hard-rules).

## Troubleshooting

**`fetch-resources.ps1` fails to download.** It retries with back-off, but a
blocked or flaky network can still defeat it. The URLs and pinned versions are at
the top of the script — you can download `sing-box.exe` (from the SagerNet
release) and `wintun.dll` (from wintun.net) by hand and drop them into
`ui-desktop/src-tauri/resources/`.

**The app starts but shows demo data / says "using demo backend".** The shell
couldn't spawn the sidecar and fell back to the mock. Build the core into
`ui-desktop/src-tauri/binaries/tenebra-core-<triple>.exe` (step 2) and restart.
Make sure `TENEBRA_MOCK` is *not* set.

**`cargo test` prints `SKIP: tenebra-core binary not built`.** Expected on a
fresh checkout — the e2e skips itself until the sidecar exists. Build it into
`src-tauri/binaries/` to run it for real.

**`go test` passes but the tunnel does nothing.** The Go tests never start a real
tunnel. Connecting for real needs the bundled sing-box present and, on Windows,
**administrator rights** for wintun. Run the app elevated and watch stderr / the
Logs screen.

**Windows says it can't create the network adapter / access denied.** wintun
needs elevation. Launch the app (or the dev build) as administrator.

**sing-box isn't found at runtime.** The sidecar looks next to its own
executable, then honours `TENEBRA_SINGBOX`. Point that variable at a known-good
`sing-box` binary to be sure.

**`npm run tauri build` fails on a system dependency.** That's a Tauri host
requirement, not a Tenebra one — see the
[Tauri prerequisites](https://v2.tauri.app/start/prerequisites/).

## Known gaps

For contributors deciding where to dig in, the honest open items:

- **Live tunnel validation.** No automated test stands up a real tunnel on any
  platform — in tests "connected" is always a fake runner. The Windows path is
  run by hand against real servers routinely; the macOS and Linux ones have had
  no privileged live run signed off (see
  [porting/macos.md](porting/macos.md#open-questions-and-risks) and
  [porting/linux.md](porting/linux.md#open-questions-and-risks)).
- **Platform adapters.** `adapters/windows`, `adapters/macos` and
  `adapters/linux` all exist, and Android runs libbox in-process through
  `ui-android/` rather than an adapter. iOS (Network Extension) is a scaffold
  that has never been compiled — that is the one still unwritten.
- **Installer code-signing.** The installer is not Authenticode-signed, so Windows
  SmartScreen warns on first run. The tagged `release` workflow already builds and
  publishes it and minisign-signs the in-app updater artifacts; Authenticode
  signing of the initial download is the remaining gap.

See [CONTRIBUTING.md](../CONTRIBUTING.md) for how to pick something up and propose
a change.
