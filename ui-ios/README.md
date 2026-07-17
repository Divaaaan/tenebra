# Tenebra for iOS — scaffold

This directory is a **structural scaffold** for the iOS client, not a working app.
It was authored on a Windows host with no Apple toolchain, so **none of the Swift
here has been compiled and no framework here has been built.** It exists to give
the iOS port a correct-by-shape starting point — targets, bundle ids, entitlements,
the app↔extension split, and the config-generator bridge — that someone continues
on a Mac. Read [docs/porting/ios.md](../docs/porting/ios.md) first for the
architecture and the reasoning; this README is the build/status companion to it.

## Honest status

| Artifact | State | How it was checked |
|---|---|---|
| `project.yml` (XcodeGen) | Structurally valid YAML | Parsed; **not** run through `xcodegen generate` |
| `core-bridge/` (repo root, shared) | Compiles for iOS **and** Android; unit-tested on host | `GOOS=android GOARCH=arm64 go build ./core-bridge` + `go test ./core-bridge/...` |
| `Support/*.plist`, `*.entitlements` | Valid property lists | Parsed with a plist reader |
| `App/*.swift`, `Extension/*.swift` | **Unverified stubs** | Not compiled — no swiftc on the authoring host |
| `scripts/build-libbox.sh` | **Unverified plan** | `bash -n` syntax only; needs macOS to run |
| `Frameworks/*.xcframework` | **Do not exist yet** | Produced by `build-libbox.sh` on a Mac |

Nothing here builds into an app until the steps below are done on a Mac. Every
Swift file carries a `SCAFFOLD — NOT compiled or verified` header, and every
libbox/TenebraCore call site is guarded with `#if canImport(...)` so the intent is
explicit rather than faked.

## Layout

```
core-bridge/                shared gomobile binding at the repo root — one package,
                            two artifacts: the iOS .xcframework AND an Android .aar
  bridge.go                 gomobile surface (//go:build ios || android):
                            GenerateConfig / ImportSubscription / OrderNodes / Version
  generate.go import.go     build-tag-free logic behind that surface, so it is
    order.go                host-testable with `go test ./core-bridge/...`
ui-ios/
  project.yml               XcodeGen manifest: host app + NE extension targets
  App/                      SwiftUI host app (thin client; never touches packets)
    TenebraApp.swift          @main entry
    ContentView.swift         node list + connect toggle
    TunnelManager.swift       NETunnelProviderManager provisioning + status
  Extension/                NEPacketTunnelProvider (links libbox, runs sing-box)
    PacketTunnelProvider.swift   startTunnel/stopTunnel + libbox lifecycle
    PlatformInterface.swift      libbox PlatformInterface (utun fd, monitors, …)
  Support/
    App/                    host-app Info.plist + entitlements
    Extension/              extension Info.plist + entitlements
  Frameworks/               gomobile xcframeworks (git-ignored; built on a Mac)
scripts/
  build-libbox.sh           builds TenebraCore.xcframework + Libbox.xcframework
```

The two Go modules stay separate on purpose (see
[ios.md](../docs/porting/ios.md#architecture)): Tenebra's core is bound into its
own small `TenebraCore.xcframework` (from the shared `core-bridge/` package, which
also binds to an Android `.aar`), and sing-box's `Libbox.xcframework` is used
unmodified. They meet only at a JSON string.

## Bring-up order (do these on a Mac, in this order)

The ordering is deliberate — **prove the memory budget before building anything
that depends on it.** This mirrors the staged plan in
[ios.md](../docs/porting/ios.md#staged-plan).

1. **I1 — memory spike, *without* the Network Extension. Do this first.**
   Build both xcframeworks (`scripts/build-libbox.sh`), then run the engine
   **inside the host app** as a local SOCKS proxy — no NE, no 50 MB cap — and
   drive real traffic while watching RSS. This is the single highest-risk unknown
   in the whole port (see the memory section below). It validates the gomobile
   bind, the two-module split, and the `GenerateConfig → StartOrReloadService`
   handoff, and gives the first honest read on whether the protocol mix fits under
   50 MB once the cap applies. **Do not build the NE until this passes.**
2. **I2 — NE scaffold.** Wire the `NEPacketTunnelProvider` (`Extension/`), the App
   Group, provisioning via `NETunnelProviderManager` (`TunnelManager`), and the
   libbox `CommandServer`/`CommandClient` IPC over the App Group socket. Bring a
   minimal tunnel up and **re-measure memory under the real jetsam cap.**
3. **I3 — SwiftUI client.** Flesh out node list + connect toggle + basic settings
   + subscription import against the extension.
4. **I4 — on-demand rules.** Auto-reconnect on untrusted Wi-Fi (`onDemandRules`).
5. **I5 — TestFlight internal.** Up to 100 internal testers, no App Review.

## Building the frameworks

```
# on macOS, with Xcode + Go >= 1.24.7
./scripts/build-libbox.sh        # -> ui-ios/Frameworks/{TenebraCore,Libbox}.xcframework
cd ui-ios
brew install xcodegen
xcodegen generate                # -> Tenebra.xcodeproj (git-ignored)
open Tenebra.xcodeproj
```

`build-libbox.sh` uses SagerNet's **fork** of gomobile (`v0.1.13`), not upstream
`golang.org/x/mobile` — upstream repeatedly breaks on new Xcode releases.
`Libbox.xcframework` is built from sing-box's own `make lib_apple` at the pinned
tag (`1.13.13`, kept in sync with the desktop sidecar) so the engine matches
upstream exactly. `TenebraCore.xcframework` is our `core-bridge` package, which
imports no sing-box and so needs none of libbox's build tags.

## The 50 MB memory cap (the top risk)

An iOS packet-tunnel provider is killed by jetsam the moment the **whole extension
process** exceeds ~50 MB resident (fatal, no warning). This covers the Go runtime,
gVisor, the QUIC stack, and every linked library — not just heap. sing-box already
hits this ceiling on fast WireGuard uploads
([sing-box #3976](https://github.com/SagerNet/sing-box/issues/3976), open and
unresolved). Consequences baked into this scaffold:

- The **host app** generates configs and downloads rule-sets; the **extension**
  only reads and runs them. Config generation (`GenerateConfig`) is kept out of the
  capped process.
- Rule-sets must be the **binary `.srs`** format, cached by the app into the App
  Group — never fetched or expanded inside the extension.
- The engine's Go runtime memory limit (libbox `Setup`) must be set **well below**
  50 MB (~40 MB), paired with aggressive GC in the core.
- 50 MB is version-dependent and not contractual — **measure on real devices**,
  which is exactly what step I1 is for.

## Entitlements and provisioning

- The `packet-tunnel-provider` capability is **self-serve** — enable **Network
  Extensions** in Xcode → Signing & Capabilities (or on the developer portal).
  There is no approval email to Apple; it is included in the standard Apple
  Developer Program. Both the app and the extension carry it (see the two
  `*.entitlements` files) and share the App Group `group.com.tenebra.ios`.
- Bundle ids follow the desktop convention (`com.tenebra.desktop`):
  app `com.tenebra.ios`, extension `com.tenebra.ios.tunnel`. They are neutral —
  no domain or infrastructure is implied. Change the prefix in `project.yml` if you
  own a different reverse-DNS namespace.
- `project.yml` leaves `DEVELOPMENT_TEAM` blank. Set it on the Mac; the Network
  Extension entitlement cannot be signed without a real Apple Developer team.

## Distribution

- **TestFlight internal** (up to 100 testers) needs **no App Review** and is the
  fast path for an alpha. The **first external** build *is* sent to Apple's beta
  App Review and must pass the full guidelines, including the VPN rules.
- **App Store — Guideline 5.4 is a hard gate:** VPN apps may only be published by
  developers enrolled as an **organization** (a D-U-N-S number), never an
  individual account. TestFlight and sideloading avoid the App-Store distribution
  terms; a public App Store release does not. This is a non-code prerequisite —
  see [ios.md](../docs/porting/ios.md#distribution).

## iOS CI — planned, deliberately not added yet

There is intentionally **no iOS job in `.github/workflows`.** A gomobile + Xcode
build is heavy and easy to get wrong, and a broken iOS job would redden the
otherwise-green cross-platform CI for a target that does not build end to end yet.
Add it only once `build-libbox.sh` is proven on a Mac. When that time comes, the
job should, on a `macos-14` (Apple Silicon) runner:

1. Install Go >= 1.24.7 and `xcodegen` (`brew install xcodegen`).
2. Run `scripts/build-libbox.sh` — or, better, restore the two xcframeworks from a
   cache / Git LFS keyed on the sing-box tag, since the gomobile bind is slow and
   its inputs only change on an engine bump.
3. `cd ui-ios && xcodegen generate`.
4. `xcodebuild build -project Tenebra.xcodeproj -scheme Tenebra \
   -destination 'generic/platform=iOS Simulator'` — a **compile-only** check
   (unsigned, no device) to keep the NE entitlement out of CI. Signed device builds
   and TestFlight uploads need secrets and belong in the release workflow, not CI.

Until then, the checkable slice of this scaffold on any host is: `project.yml`
parses as YAML, and the shared Go bridge compiles for both mobile targets and
passes its unit tests (`GOOS=android GOARCH=arm64 go build ./core-bridge`,
`GOOS=darwin GOARCH=arm64 go build -tags ios ./core-bridge`, and
`go test ./core-bridge/...`).

## What this needs from a maintainer with a Mac + Apple account

- A Mac with Xcode to build the frameworks, compile the Swift, and iterate.
- An **Apple Developer Program** membership to sign the Network Extension
  entitlement, run on a device, and use TestFlight. For any App Store presence
  later, an **organization** enrollment (Guideline 5.4) — an individual account
  cannot ship a VPN.
