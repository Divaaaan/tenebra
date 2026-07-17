# Tenebra for Android — scaffold

This directory is a **libbox VPN client** for Android, structured as a faithful
re-homing of SagerNet's `sing-box-for-android` (SFA) tunnel plumbing onto Tenebra's
core. It was authored on a **Windows host with no Android SDK/NDK**, so **none of the
Kotlin here has been compiled and the fused `.aar` has not been built.** What is here is a
correct-by-shape project — Gradle module, manifest, the VpnService/BoxService/libbox
bridge, the config-generator wrapper, and a minimal Compose UI — that builds into an
APK on a machine with the toolchain (see [Bring-up order](#bring-up-order)).

Unlike the iOS port there is **no 50 MB memory cap**: an Android `VpnService` is an
ordinary process, so the engine and config generation both live in it and the
memory-driven app/extension split iOS needs does not apply here.

## Honest status

| Artifact | State | How it was checked |
|---|---|---|
| `settings/build/app` `.gradle.kts` | Structurally valid Kotlin DSL | Reviewed; **not** run through Gradle (no Android SDK on host) |
| `gradle/libs.versions.toml` | Valid TOML version catalog | Parsed with `tomllib` |
| `AndroidManifest.xml`, `res/**` | Valid XML | Parsed |
| `gradlew`, `gradlew.bat` | Stock Gradle 8.11.1 wrapper scripts | `bash -n gradlew` OK |
| `core-bridge/` + `mobile/` (repo root) | Plain Go generator + gomobile wrapper; unit-tested, cross-builds | `go test ./core-bridge/...`; `cd mobile && GOOS=android GOARCH=arm64 go build ./...` |
| Kotlin — `core/`, `store/`, `ui/` | Standard AndroidX/Compose; **not compiled** | Bracket-balance smoke test; no `kotlinc`/Android SDK on host |
| Kotlin — `bg/**` | **Unverified against the generated `.aar`** | Reviewed; every libbox/TenebraCore call site the bind must confirm is flagged `verify against generated tenebra.aar` |
| `app/libs/tenebra.aar` | **Does not exist yet** | Built by CI (see below) and dropped in; git-ignored |

Nothing here builds into an APK until the fused `.aar` is produced and the Gradle
build is run on a toolchain host. Files under `bg/` carry a one-line scaffold header
and, where they mirror SFA, a GPL-3.0 upstream attribution.

## Layout

```
core-bridge/                the pure config generator at the repo root (plain Go lib)
mobile/                     gomobile wrapper: binds core-bridge + libbox into one .aar
ui-android/
  settings.gradle.kts       :app module, pluginManagement, repositories
  build.gradle.kts          root: plugin versions (apply false)
  gradle/libs.versions.toml  version catalog (single source of pins)
  gradlew(.bat)             stock wrapper (gradle-wrapper.jar git-ignored; CI restores)
  app/
    build.gradle.kts        Compose app; implementation(files("libs/tenebra.aar"))
    proguard-rules.pro      gomobile keep-rules (minify OFF until proven on CI)
    libs/                   the fused tenebra.aar lands here (git-ignored)
    src/main/
      AndroidManifest.xml   permissions + <service TenebraVpnService> + QS tile
      java/com/tenebra/android/
        TenebraApplication.kt   creates the notification channel
        bg/                     tunnel plumbing (mirror of io.nekohasekai.sfa.bg)
          TenebraVpnService.kt    VpnService + libbox PlatformInterface; openTun -> fd
          BoxService.kt           libbox driver: CommandServer + StartOrReloadService
          PlatformWrapper.kt      PlatformInterface defaults (interface monitor, etc.)
          DefaultNetworkMonitor.kt underlying-network tracking for the engine
          ServiceNotification.kt  foreground channel + notification
          TenebraTileService.kt   optional Quick Settings toggle
          TunnelState.kt          process-wide status StateFlow the UI observes
        core/
          TenebraCore.kt          single indirection over the gomobile symbol
          ConfigGenerator.kt      import / generate / order envelopes (the ABI)
          Profile.kt              display-side profile projection (read-only)
        store/ProfileRepository.kt profile blob (file) + selection (prefs), shared
        ui/                       Jetpack Compose: MainActivity, MainViewModel,
                                  MainScreen, theme/
```

## How the pieces connect

One fused gomobile artifact whose two halves meet only at a JSON string — the same
generator-vs-engine split as desktop and iOS, but bound into a single `.aar`:

- **config generator** — the `mobile/` wrapper over `core-bridge`, class
  `io.nekohasekai.tenebracore.Tenebracore`. No sing-box inside it. Wrapped by
  `core/TenebraCore.kt`.
- **sing-box engine** — the unmodified `libbox`, package `io.nekohasekai.libbox`.

They are bound together — one Go runtime, one `go` support package — into
`tenebra.aar`; binding them separately would duplicate `go/Seq` + `go/Universe` and
fail D8's duplicate-class check.

The connect flow (`prepare -> import -> generate -> StartOrReloadService -> establish`):

1. **Import** — `MainViewModel.importSubscription(url)` → `ConfigGenerator.importSubscription`
   → `Tenebracore.importSubscription` fetches the subscription and returns the full
   profile JSON, stored verbatim by `ProfileRepository`.
2. **Prepare** — the Connect toggle calls `MainActivity.connect()`, which runs
   `VpnService.prepare(this)`. First time, the system consent dialog is shown; on
   approval it starts `TenebraVpnService`.
3. **Generate** — the service reads the stored profile, resolves the selected node's
   selector tag via `Tenebracore.orderNodes(profile, lastGood = serverId)`, and calls
   `Tenebracore.generateConfig(profile, selectedTag)` with **`tun.externalTun = true`**
   → a sing-box config JSON.
4. **Run** — `BoxService.start(config)` calls `Libbox.setup(...)` once, creates a
   `CommandServer`, and `startOrReloadService(config, OverrideOptions())`. The engine
   boots in-process.
5. **establish fd** — libbox calls back into the service's `openTun(options)`. It
   builds a `VpnService.Builder` (addresses from the config, MTU, the **default routes
   `0.0.0.0/0` + `::/0` added by us**, a DNS server) and returns the fd from
   `establish()`. Engine-created sockets are `protect()`'d via `autoDetectInterfaceControl`
   so they bypass the tun.

**Why we add the routes, not sing-box:** with `externalTun`, the core deliberately
omits `auto_route` from the tun inbound (so it never fights the OS routing table), so
`options.autoRoute` is false and the SFA route-adding branch would be skipped. On
Android the `VpnService.Builder` owns routes, so `openTun` claims the full default
route unconditionally. sing-box still sends LAN/`direct` flows out directly — its own
route rules decide that, and those sockets are protected — so nothing loops back.

Status flows the other way through `TunnelState` (a process-wide `StateFlow` the
service updates and the `MainViewModel` observes); the app and service share one
process, so no bound-service/AIDL channel is needed.

## libbox API: modern vs classic — verify after the first bind

libbox's Kotlin surface **changed shape** across sing-box versions, and SFA tracks the
current one. This scaffold targets the **modern** surface, matching the pinned engine
(`sing-box` ≈1.13), namely: `Libbox.setup(SetupOptions)`,
`CommandServer(handler, platformInterface)` + `commandServer.startOrReloadService(config, OverrideOptions)`,
`PlatformInterface.findConnectionOwner(...) : ConnectionOwner`, `localDNSTransport()`,
`systemCertificates()`, and element getters as **methods** (`prefix.address()` /
`prefix.prefix()`, `options.dnsServerAddress.value`).

Every point that differs from the older ("classic") surface is marked in code with
`verify against generated tenebra.aar`. After the first bind, confirm with
`javap io.nekohasekai.libbox.PlatformInterface` / `TunOptions` / `Libbox`. The reliable
tell:

- `findConnectionOwner` returns `ConnectionOwner` **and** `systemCertificates()` exists
  → **modern** (this scaffold is correct as written).
- `findConnectionOwner` returns `Int`, with `packageNameByUid`/`uidByPackageName`
  present, and `Libbox.newService(config, platform)` instead of `startOrReloadService`
  → **classic**; adjust `BoxService` and `PlatformWrapper` at the flagged lines.

This is the Android analogue of the iOS scaffold deferring the exact
`LibboxPlatformInterfaceProtocol` conformance to the Mac — the member set is defined by
the generated headers, not by us.

## Bring-up order

1. **Build `tenebra.aar`** on a host with the Android NDK + SDK (the CI job, or a dev
   box) via `scripts/build-libbox-android.sh`. It is git-ignored; it lands in
   `app/libs/`. The script installs the sing-box-pinned gomobile fork, then runs one
   `gomobile bind` over the `mobile/` wrapper and `experimental/libbox` together
   (`-javapkg io.nekohasekai`, the API-23 libbox tags).
   - Then reconcile the `bg/**` `verify` markers against the generated headers.
2. **Build the APK**: `cd ui-android && ./gradlew assembleDebug`
   (CI restores `gradle-wrapper.jar` first, e.g. `gradle wrapper` or the
   `gradle/actions/setup-gradle` action).
3. **Sideload**: `adb install app/build/outputs/apk/debug/app-debug.apk` — no store.
4. **Connect**: import a subscription URL → the node list fills → **Connect** grants
   the VPN consent and brings the tunnel up.

## Distribution, Doze, and permissions

- **Sideload / F-Droid**, no store gate. Google Play has its own VPN policy, but the
  alpha path for Crimea testers is a direct APK, which avoids it entirely. There is no
  Android equivalent of the iOS App Store 5.4 organization requirement for sideloading.
- **Persistence under Doze**: the tunnel runs as a **foreground service** with
  `foregroundServiceType="systemExempted"` (the correct type for a user-started VPN on
  Android 14+). For survival across reboots/Doze, the user enables **always-on VPN**
  in system settings — the supported mechanism — which restarts the service without a
  boot receiver. The app deliberately does **not** auto-reconnect on its own after a
  system kill (`START_NOT_STICKY`), so the tun consent stays meaningful.
- **Honest permission note:** `INTERNET`, `ACCESS_NETWORK_STATE`, `FOREGROUND_SERVICE`
  (+`_SYSTEM_EXEMPTED`), `POST_NOTIFICATIONS`, and `BIND_VPN_SERVICE` are used by the
  code here. `RECEIVE_BOOT_COMPLETED` and `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS` are
  declared for planned auto-start / battery-exemption UX and are **not yet exercised**
  — wire them or drop them before release.

## What is deliberately not here yet

- **Runtime node switching** without a reconnect (libbox `SelectOutbound` via a
  `CommandClient`). Node selection currently applies on the next connect, resolved to a
  selector tag through `orderNodes`.
- **`smart` split routing** — `ConfigGenerator` defaults to `global` (tunnel
  everything), which needs no cached rule-sets. `smart` needs the app to cache binary
  `.srs` rule-sets and pass `routing.ruleSetDir`; the memory-constrained-fetch concern
  is an iOS one and does not bind here, but the caching is still unbuilt.
- **Traffic stats / logs** surfaced in the UI (libbox `CommandClient` status stream).
- **CI** — owned by the parallel toolchain/CI work, not added from here.
- **Launcher/notification icons** are placeholder vectors; a design pass adds proper
  adaptive + density assets.
