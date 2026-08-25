# Tenebra for Android — alpha

A **libbox VPN client** for Android, structured as a faithful re-homing of SagerNet's
`sing-box-for-android` (SFA) tunnel plumbing onto Tenebra's core. It builds into an APK
on CI, installs by hand, and brings a tunnel up on a device. It is an **alpha**: there
is no store listing, no signed release APK yet, and the routing surface is a fraction
of the desktop client's.

Unlike the iOS port there is **no 50 MB memory cap**: an Android `VpnService` is an
ordinary process, so the engine and config generation both live in it and the
memory-driven app/extension split iOS needs does not apply here.

`minSdk` 23, `targetSdk` 35, ABIs `arm64-v8a` / `armeabi-v7a` / `x86_64`. The app
carries its own version (`versionName` in `app/build.gradle.kts`), separate from the
desktop client's.

## Honest status

| Artifact | State | How it was checked |
|---|---|---|
| Gradle project (`settings`/`build`/`app` `.gradle.kts`, version catalog, wrapper) | Builds | `./gradlew :app:assembleDebug` runs in [CI](../.github/workflows/android.yml) on pushes to `main` and on pull requests, whenever `core-bridge/`, `core/`, `mobile/`, `ui-android/` or the build script changed |
| Kotlin — `core/`, `store/`, `net/`, `ui/`, `bg/**` | Compiles, and runs on a device | The APK assembles in CI, and the fixes in `git log -- ui-android/` came off hardware: the default-network monitor handing the engine our own tun (connected, no traffic) and the `targetSdk` 35 cleartext block swallowing the live-switch call are not paper findings |
| `app/libs/tenebra.aar` | Built on every CI run, never committed | [`scripts/build-libbox-android.sh`](../scripts/build-libbox-android.sh) binds `mobile/` and `experimental/libbox` in one gomobile pass. It is git-ignored (tens of MB, statically linked Go runtime), so a local build needs the NDK or the artifact from a CI run |
| `bg/**` against the generated `.aar` | Settled — the bind emits libbox's **modern** surface | The APK compiles against `Libbox.setup(SetupOptions)`, `CommandServer.startOrReloadService` and `findConnectionOwner → ConnectionOwner`; a classic-surface `.aar` would not resolve. The `verify against generated tenebra.aar` comments in `bg/**` predate the first bind and are stale |
| `core-bridge/` + `mobile/` (repo root) | Plain Go generator + gomobile wrapper; unit-tested, cross-builds | `go test ./core-bridge/...`; `cd mobile && GOOS=android GOARCH=arm64 go build ./...` |
| The release APK | Unsigned by Gradle **on purpose**; CI signs it | The `release` build type declares no `signingConfig`; the workflow `zipalign`s and `apksigner`s it from a keystore in secrets. Until those secrets exist a tag ships no APK and the workflow fails loudly rather than skipping quietly |
| The tunnel itself | Brought up by hand on a device | Nothing automated stands up a real tunnel here, exactly as on the desktop platforms |

## What works today

- **Import** a subscription URL; the profile JSON the core returns is stored verbatim.
- **Node list** with per-node TCP connect-time latency badges on the desktop ping
  scale, plus an **AUTO** row that keeps the exit on the lowest-ping node.
- **Connect / disconnect** through `VpnService` + libbox, with a foreground
  notification and an optional Quick Settings tile.
- **Switching the exit on a live tunnel** with no reconnect: the service captures the
  run's clash-api endpoint and secret from the generated config, and the selector is
  PUT to the tapped node's outbound tag. Best-effort — while disconnected, or if the
  call fails, the saved selection applies on the next connect instead.
- **Connect on boot** (a `BootReceiver`, gated on the toggle and on VPN consent
  already existing), subscription refresh / removal, and an about block with the app,
  core and engine versions.
- **In-app logs, a crash report and a shareable diagnostics text**, so a tester can
  report a failure without `adb`.

Russian is the primary locale; an English resource set is not written yet.

## Layout

```
core-bridge/                the pure config generator at the repo root (plain Go lib)
mobile/                     gomobile wrapper: binds core-bridge + libbox into one .aar
ui-android/
  settings.gradle.kts       :app module, pluginManagement, repositories
  build.gradle.kts          root: plugin versions (apply false)
  gradle/libs.versions.toml  version catalog (single source of pins)
  gradlew(.bat) + wrapper   committed, so ./gradlew works on a clean checkout
  app/
    build.gradle.kts        Compose app; implementation(files("libs/tenebra.aar"))
    proguard-rules.pro      gomobile keep-rules (minify OFF until proven on CI)
    libs/                   the fused tenebra.aar lands here (git-ignored)
    src/main/
      AndroidManifest.xml   permissions + <service TenebraVpnService> + QS tile + boot
      res/xml/network_security_config.xml  cleartext for loopback only (clash-api)
      java/com/tenebra/android/
        TenebraApplication.kt   creates the notification channel
        CrashLog.kt             last uncaught exception, kept for the next launch
        bg/                     tunnel plumbing (mirror of io.nekohasekai.sfa.bg)
          TenebraVpnService.kt    VpnService + libbox PlatformInterface; openTun -> fd
          BoxService.kt           libbox driver: CommandServer + StartOrReloadService
          PlatformWrapper.kt      PlatformInterface defaults (interface monitor, etc.)
          DefaultNetworkMonitor.kt underlying-network tracking for the engine
          ServiceNotification.kt  foreground channel + notification
          TenebraTileService.kt   optional Quick Settings toggle
          BootReceiver.kt         connect after a reboot when the user asked for it
          ClashControl.kt         this run's clash-api endpoint + secret
          LogStore.kt             in-app engine/app log buffer
          DiagnosticsReport.kt    the shareable report, run through
          DiagnosticsScrubber.kt  the scrubber that strips node credentials first
          TunnelState.kt          process-wide status StateFlow the UI observes
        core/
          TenebraCore.kt          single indirection over the gomobile symbol
          ConfigGenerator.kt      import / generate / order / tags envelopes (the ABI)
          Profile.kt              display-side profile projection (read-only)
        net/
          ClashApiClient.kt       the live selector PUT (loopback, secret-gated)
          NodePinger.kt           concurrent TCP connect-time latency
        store/ProfileRepository.kt profile blob (file) + selection (prefs), shared
        ui/                       Compose: MainActivity, MainViewModel, MainScreen,
                                  SettingsScreen, LogsScreen, theme/
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
   boots in-process. The config's clash-api endpoint and secret are captured into
   `ClashControl` first, so the exit can be steered while the tunnel is up.
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

## libbox API: modern vs classic

libbox's Kotlin surface **changed shape** across sing-box versions, and SFA tracks the
current one. This client targets the **modern** surface, matching the pinned engine
(`sing-box` 1.13.14), namely: `Libbox.setup(SetupOptions)`,
`CommandServer(handler, platformInterface)` + `commandServer.startOrReloadService(config, OverrideOptions)`,
`PlatformInterface.findConnectionOwner(...) : ConnectionOwner`, `localDNSTransport()`,
`systemCertificates()`, and element getters as **methods** (`prefix.address()` /
`prefix.prefix()`, `options.dnsServerAddress.value`).

That is no longer an assumption: the APK compiles against those symbols, so a bind
emitting the classic surface (`findConnectionOwner` returning `Int`,
`Libbox.newService` instead of `startOrReloadService`) would fail the build rather
than ship. The `verify against generated tenebra.aar` comments scattered through
`bg/**` are the pre-bind reminders and can be swept; the engine pin lives in
`mobile/go.mod` and in the build script's `singbox_version`, which must agree.

## Bring-up order

1. **Build `tenebra.aar`.** CI does it on every run via
   [`scripts/build-libbox-android.sh`](../scripts/build-libbox-android.sh); locally it
   needs the Android NDK + SDK. The script installs the sing-box-pinned gomobile fork,
   then runs one `gomobile bind` over the `mobile/` wrapper and `experimental/libbox`
   together (`-javapkg io.nekohasekai`, the API-23 libbox tags). It is git-ignored and
   lands in `app/libs/`.
2. **Build the APK**: `cd ui-android && ./gradlew assembleDebug`.
3. **Sideload**: `adb install app/build/outputs/apk/debug/app-debug.apk` — no store.
4. **Connect**: import a subscription URL → the node list fills → **Connect** grants
   the VPN consent and brings the tunnel up.

CI signs debug APKs with a shared keystore (`ANDROID_DEBUG_KEYSTORE_B64`) so builds
from different runs install over one another; without the secret Gradle generates a
throwaway key per run and a tester has to uninstall first.

## Distribution, Doze, and permissions

- **Sideload / F-Droid**, no store gate. Google Play has its own VPN policy, but the
  alpha path is a direct APK, which avoids it entirely. There is no Android equivalent
  of the iOS App Store 5.4 organization requirement for sideloading.
- **Persistence under Doze**: the tunnel runs as a **foreground service** with
  `foregroundServiceType="systemExempted"` (the correct type for a user-started VPN on
  Android 14+). For survival across reboots the app has its own boot-connect toggle,
  and **always-on VPN** in system settings remains the mechanism the platform
  supports. The service itself does **not** auto-reconnect after a system kill
  (`START_NOT_STICKY`), so the tun consent stays meaningful.
- **Honest permission note:** `INTERNET`, `ACCESS_NETWORK_STATE`, `FOREGROUND_SERVICE`
  (+`_SYSTEM_EXEMPTED`), `POST_NOTIFICATIONS`, `BIND_VPN_SERVICE` and
  `RECEIVE_BOOT_COMPLETED` are all used by the code here.
  `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS` is declared for a battery-exemption prompt
  that is **not written yet** — wire it or drop it before a release.
- **Cleartext** is permitted for loopback only, and nothing else: `targetSdk` 35 blocks
  plain HTTP by default, and the clash-api the live node-switch talks to is on
  `127.0.0.1` behind a per-run secret.

## What is deliberately not here yet

- **`smart` split routing.** Every connect uses the core's defaults — `global`, LAN
  bypass on — because `smart` needs the app to cache binary `.srs` rule-sets and pass
  `routing.ruleSetDir`, and that caching is unbuilt. Nothing in the UI changes the
  routing mode.
- **The rest of the routing surface.** The mobile generator takes a mode, LAN
  bypass, IPv4-only, a kill switch, the two DNS resolvers and a rule-set directory;
  this client sets none of them and takes the defaults. Per-app split tunnelling,
  multi-hop and custom rules are not in the mobile surface at all — they live in the
  desktop daemon.
- **The DPI bypass.** It is a Windows packet filter (`core/zapret`); on Android the
  tunnel carries censored services through the exit node like everything else.
- **Traffic stats in the UI** (libbox `CommandClient` status stream). Logs are
  surfaced; throughput is not.
- **A signed release APK.** The workflow builds and signs one on a tag; the keystore
  secrets do not exist yet, so no release carries an APK.
- **Launcher / notification icons** are placeholder vectors; a design pass adds proper
  adaptive + density assets.
