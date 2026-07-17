# Porting to Android

A plan for the Android port, and the counterpart to [ios.md](ios.md). For the
shared design this builds on, read [architecture.md](../architecture.md); for the
core ↔ UI protocol that is re-homed here, [control-protocol.md](../control-protocol.md).
The Go core, the two-artifact split, and the `GenerateConfig → StartOrReloadService`
handoff are shared with iOS — this doc covers what is different on Android, and
unlike iOS most of it is buildable on CI today.

## Overview

Like iOS and unlike the desktop, Android cannot run sing-box as a **sidecar**
process: an app may open a TUN device only through the system `VpnService`, and the
engine has to run **in-process**, compiled as a library and linked into the app.
So the same two inversions as iOS apply:

- **The engine moves in-process.** Instead of a bundled `sing-box` binary, the
  engine is `libbox` — sing-box's `experimental/libbox` package built into an
  Android `.aar` with gomobile — linked directly into the app.
- **The control protocol moves off stdio.** The line-delimited JSON over
  stdin/stdout from [control-protocol.md](../control-protocol.md) has no pipe to
  cross here. It re-homes onto libbox's in-process `CommandServer`/`CommandClient`.

What carries over cleanly is the part that matters most: Tenebra's core is a pure
config **generator** with no sing-box import (see
[architecture.md](../architecture.md#core-go--core)). It stays a generator here
too — gomobile-bound into its own small `.aar` and called in-process — so the
protocol semantics (`connect`, node selection, routing, the fallback walk) are
unchanged; only the transport under them changes.

The official sing-box Android client (**SFA**, GPLv3 — the same license as
Tenebra) is the reference implementation and can be read and mirrored legally. Its
`VpnService` glue is small and reusable: the service wires the platform-owned
pieces to libbox and gets out of the way.

Android is **materially easier than iOS** on the two things that dominate the iOS
plan:

- **No 50 MB jetsam cap.** A `VpnService` is an ordinary foreground app process,
  not a memory-capped Network Extension. The single biggest risk in the iOS port —
  fitting the whole engine under ~50 MB RSS — simply does not exist here. The
  engine still should not be wasteful, but there is no fatal ceiling to measure
  against.
- **No store gate to build.** Sideloading a `VpnService` app needs no D-U-N-S,
  no organization enrollment, and no review — see [Distribution](#distribution).

## Architecture

The standard Android VPN shape — one app, a bound service that owns the tunnel:

- a **UI layer** — node list, connect toggle, settings, subscription import — that
  provisions and observes the tunnel but never touches packets;
- a **`VpnService`** — a foreground service the system keeps alive while the tunnel
  is up — that links `libbox` and runs the engine on the FD the OS hands it.

```
 app process                                       VpnService (same or :service process)
 -----------                                       -------------------------------------
 UI (node list, connect)     config JSON  ------->  libbox = sing-box (unmodified)
 tenebra-core.aar (generator)                            |
                                                         +--> tun fd from
   GenerateConfig(json) ----->  StartOrReloadService(json)     VpnService.Builder.establish()
                             libbox CommandServer/Client        (routes/DNS/MTU set on the
                             cached .srs rule-sets               Builder, not by sing-box)
```

**Two Go modules, kept separate** — the same split as desktop and iOS:

1. **Tenebra core** is gomobile-bound into its **own** `.aar` (`tenebra-core.aar`)
   from the shared `core-bridge` package. The very same package binds to the iOS
   `.xcframework` — one config generator, both mobile targets. It exposes a few
   string-in/string-out calls: `GenerateConfig(profileJSON) → configJSON`, plus
   `ImportSubscription`, `OrderNodes`, and `Version`.
2. **sing-box `libbox.aar`** is used **unmodified** as the engine (Java package
   `io.nekohasekai.libbox`).
3. The UI calls `GenerateConfig`, then hands the resulting JSON to the service's
   `CommandServer.StartOrReloadService(json)`, which boots sing-box internally.

Do **not** merge the two Go modules. Keeping Tenebra's generator free of any
sing-box import preserves its stdlib-only purity, its independent version cadence,
and the clean generator-vs-engine boundary. They are two artifacts that meet only
at a JSON string — the same contract as everywhere else in the project.

**How the tunnel FD is obtained — no root.** `VpnService.Builder.establish()`
returns a `ParcelFileDescriptor` for the TUN device once the user grants the
one-time VPN consent (the system `prepare()` dialog). That FD is passed to libbox,
which reads and writes packets on it. Crucially, **Android — not sing-box — owns
routing**: addresses, routes, DNS servers, per-app rules, and MTU are configured
on the `VpnService.Builder`. So the generated config sets `tun.externalTun` (see
`GenerateConfig` in `core-bridge/bridge.go`), which makes the tun inbound omit
`auto_route`; sing-box drives packets on the FD but does not try to install routes
underneath the service. Getting this wrong — leaving `auto_route` on — double-manages
routing and breaks connectivity, so it is a load-bearing flag, not a detail.

**UI shell note.** As with iOS, the tunnel plumbing is native (Kotlin +
`VpnService`) regardless of what draws the UI; the desktop Tauri WebView shell is
not assumed to port. The `ui-android/` scaffold is owned separately from this
toolchain work.

## Building the core: gomobile

`libbox` (and Tenebra's own core) are built for Android with **gomobile**, but not
upstream gomobile — sing-box maintains a fork (`github.com/sagernet/gomobile`)
because upstream repeatedly breaks. The build is driven by
[`scripts/build-libbox-android.sh`](../../scripts/build-libbox-android.sh), the
mirror of the iOS `scripts/build-libbox.sh`:

1. Clone sing-box at the pinned tag (`v1.13.13`, kept in sync with
   `scripts/fetch-resources.*` and the desktop sidecar).
2. `make lib_install` — installs the SagerNet gomobile fork **at the version the
   tag's own Makefile pins** (`v0.1.12` for 1.13.13). The script does **not**
   hardcode a gomobile version; the tag's Makefile is the single source of truth,
   and both artifacts are bound with that one binder so the engine and our core can
   never drift apart. (The iOS script reads the same version out of that Makefile
   for the identical reason.)
3. **Artifact 1 — `tenebra-core.aar`:** `gomobile bind -target android -androidapi 23
   -javapkg com.tenebra.core -trimpath -o ui-android/app/libs/tenebra-core.aar ./core-bridge`.
   No build tags: the core imports no sing-box, and `-target android` sets
   `GOOS=android`, which activates the `android` side of the
   `//go:build ios || android` bridge automatically.
4. **Artifact 2 — `libbox.aar`:** `make lib_android` (= `go run ./cmd/internal/build_libbox
   -target android`), then copy `libbox.aar` into `ui-android/app/libs/`. The
   engine is built with sing-box's own entrypoint, so it is bit-for-bit upstream;
   every protocol Tenebra uses is a subset of what that build ships. (`make lib_android`
   also emits a `libbox-legacy.aar` for API 21; the alpha ships the API-23 `libbox.aar`.)

Practical constraints:

- **Build tags are load-bearing — but they are sing-box's problem here, not ours.**
  Because `libbox.aar` is built from `make lib_android`, it carries exactly the tag
  set sing-box ships for Android (`with_gvisor`, `with_quic`, `with_utls`,
  `with_wireguard`, `with_clash_api`, and the linker workarounds). We do not
  hand-roll the tag list, so a protocol can never be silently missing from a
  home-grown flag choice.
- **Toolchain.** Go ≥ 1.24.7 (the pinned sing-box tag's floor), Android **NDK r28**,
  and **OpenJDK 17** for the Gradle build. gomobile needs the NDK even for the
  pure-Go `tenebra-core.aar`, because the bind emits a JNI/C bridge the NDK's clang
  compiles; point it at the NDK via `ANDROID_NDK_HOME`.
- **Linux/WSL/CI, not necessarily native Windows.** The `.aar` builds on Linux
  (gomobile's android bind uses the NDK's clang, not Xcode) — which is why Android
  gets a CI job and iOS does not. The build script was authored on Windows and has
  **not** been run there; native-Windows binds are unverified (use WSL). The Gradle
  APK build runs anywhere with the JDK + SDK.
- **Framework size.** `libbox.aar` is tens of MB because it statically bundles the
  Go runtime, gVisor and quic-go. On Android that counts against the *download*
  size only — there is no runtime memory cap it competes with.

## Continuous integration

[`.github/workflows/android.yml`](../../.github/workflows/android.yml) is a
standalone workflow (kept out of `ci.yml` so an Android hiccup never reddens the
desktop/core checks). On `ubuntu-latest`:

- **Triggers.** Pushes and PRs that touch `core-bridge/**`, `core/**`,
  `ui-android/**`, the build script, or the workflow itself build a **debug APK**
  (uploaded as an artifact). A `v*` tag builds a **signed release APK** and attaches
  it to the GitHub release. (GitHub ignores `paths` filters for tag pushes, so a
  release tag always runs regardless of what it touched.)
- **Toolchain.** `setup-go` (1.26, ≥ the sing-box floor), `setup-java@v4`
  (temurin 17), `android-actions/setup-android@v4` (SDK + build-tools), and
  `nttld/setup-ndk@v1` pinned to `r28` (the generic SDK setup does not pin an NDK).
- **Engine cache.** `actions/cache` stores **only** `ui-android/app/libs/libbox.aar`
  under the key `libbox-aar-singbox-1.13.13`. libbox is the slow artifact and its
  only input is the sing-box version, so it is rebuilt only on an engine bump — the
  same reason [ios.md](ios.md#ios-ci--planned-deliberately-not-added-yet) suggests
  caching the xcframework. `tenebra-core.aar` is deliberately **not** cached: it is
  cheap and it changes whenever `core-bridge/` or `core/` changes, so the script
  rebuilds it every run to stay correct. **Bump the cache key whenever
  `singbox_version` changes**, or a stale engine will be restored.
- **Build then assemble.** On a cache miss the script builds both `.aar`s; on a hit
  it rebuilds only the core and reuses the cached engine. Then
  `./gradlew :app:assembleDebug` (or `assembleRelease` on a tag).

The workflow references `ui-android/` (the Gradle project, owned by the client
scaffold work) — until that module lands, the Android job has nothing to assemble.
The toolchain half (this doc, the build script, the workflow) is complete and lands
first; the job goes green once `ui-android/` exists.

## Release signing

**Android requires every update to be signed by the same key as the install it
replaces**, or the update is rejected. A **debug** keystore is auto-generated
per-machine, so two CI runners (or a runner and a laptop) would sign with different
keys and their APKs could not update one another. For an alpha that ships updates
around a closed tester ring, that is fatal.

So the release build is signed with **one long-lived keystore held in CI secrets**,
used for every build. Create it once (keep the `.jks` and passwords somewhere safe
and private — losing them means testers must uninstall/reinstall to move to a new
key):

```sh
keytool -genkeypair -v \
  -keystore tenebra-release.jks \
  -storetype PKCS12 \
  -alias tenebra \
  -keyalg RSA -keysize 4096 \
  -validity 10000
# answer the prompts; remember the store password and (if different) key password.

# base64-encode the keystore for the GitHub secret (no line wrapping):
base64 -w0 tenebra-release.jks    # Linux
base64 -i tenebra-release.jks     # macOS
```

Then add four repository secrets:

| Secret | Value |
|---|---|
| `ANDROID_KEYSTORE_B64` | the base64 string above |
| `ANDROID_KEYSTORE_PASS` | the keystore (store) password |
| `ANDROID_KEY_ALIAS` | `tenebra` (or whatever `-alias` you chose) |
| `ANDROID_KEY_PASS` | the key password (same as the store password unless you set a separate one) |

The release job **signs the APK in CI, not in Gradle**: `assembleRelease` produces
an *unsigned* APK, and the job then `zipalign`s it and signs it with `apksigner`
using the decoded keystore. This keeps the signing contract entirely in the
workflow and out of `ui-android/`'s `build.gradle` — **the release build type must
therefore leave the APK unsigned** (declare no `signingConfig`). Never commit the
keystore or the passwords; the base64 lives only in the secret.

## Differences from iOS, and Android-specific risks

- **No 50 MB memory cap.** The headline iOS risk is gone (see
  [ios.md#memory-budget](ios.md#memory-budget)). Keep the engine reasonable, but
  there is no fatal jetsam ceiling and no pre-NE "memory spike" gate to pass first.
- **No organization / D-U-N-S requirement.** Apple's Guideline 5.4 (VPN apps only
  from organization accounts) has no Android equivalent for sideloaded apps.
- **Doze and OEM battery management (top Android risk).** Android aggressively
  suspends background apps; some OEMs (Xiaomi, Huawei, Samsung, …) are harsher than
  stock. Mitigations: run the tunnel in a **foreground service** with a
  `foregroundServiceType` that includes `systemExempted`/`specialUse` as
  appropriate, and offer **always-on VPN + block-connections-without-VPN** (a system
  setting the app can guide the user to), which keeps the tunnel resilient across
  Doze and reboots without the app being foregrounded.
- **Per-app VPN (allow/disallow lists).** `VpnService.Builder.addAllowedApplication`
  / `addDisallowedApplication` scope which apps route through the tunnel. Useful, but
  every excluded app is a leak path to reason about deliberately.
- **MTU.** The MTU set on the `VpnService.Builder` must leave headroom for the
  outbound protocol's overhead (WireGuard/Hysteria2/etc.); too high silently drops
  or fragments large packets. It needs measurement, not a guessed constant.
- **VpnService consent + revocation.** The first connect shows the system consent
  dialog; the OS can revoke the tunnel (`onRevoke`) when another VPN starts, which
  the service must handle cleanly.

## Distribution

- **Sideload from GitHub Releases.** The signed APK is attached to the tag's GitHub
  release; testers download and install it directly (enabling "install unknown
  apps" once). A `VpnService` app needs **no store review and no special
  entitlement** — the VPN consent dialog is the only gate, shown to the user at
  first connect.
- **Play Store is optional and later.** Google Play *does* review VPN apps (the
  `VpnService` + foreground-service policies), but the store is not required to ship
  an alpha. Sideloading avoids it entirely; a Play listing can follow once the app
  is past alpha.
- **Updates.** Because every build is signed with the one long-lived keystore
  above, a newer APK installs straight over an older one for the closed tester ring.

## Honest status

- The gomobile bind and the Gradle APK build have **not** been run on the authoring
  host (Windows, no NDK). `scripts/build-libbox-android.sh` was checked with
  `bash -n` only; the workflow YAML was validated but not executed. **The first real
  bind and the first APK are on CI** — treat the script and workflow as an
  executable plan until that run is green.
- `gomobile init` is intentionally omitted from the build script: sing-box's proven
  `lib_install → lib_android` path runs no init, and modern gomobile binds on
  demand. If the first CI bind proves otherwise, adding `gomobile init` after
  `make lib_install` is a one-line fix.
- The workflow depends on the `ui-android/` Gradle project (owned separately). Its
  release build type must leave the APK unsigned for the CI signing step to own the
  keystore.

## Open questions and risks

- **First-run toolchain unknowns.** Whether `make lib_android` and the core bind
  succeed unmodified on the CI image (NDK r28 discovery, the sing-box `//go:linkname`
  linker workarounds under Go 1.26, `gomobile init` necessity) is only proven once
  the job runs. This is the main thing to watch on the first green.
- **`ui-android` contract.** The workflow assumes the module id `:app`, the standard
  AGP output paths, and an unsigned release build type. Those must match the client
  scaffold.
- **Shared release tag.** Desktop `release.yml` and this workflow both publish to
  the same `v*` release; this job only *adds* the APK asset and does not rewrite the
  body, but the two runs are not ordered across workflows.
- **OEM battery behavior.** Doze/OEM aggressiveness is device-specific and can only
  be characterized on real hardware, not in CI.
