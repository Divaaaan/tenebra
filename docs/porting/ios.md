# Porting to iOS

A plan for the iOS port. For the shared design this builds on, read
[architecture.md](../architecture.md); for the core ↔ UI protocol that has to be
re-homed here, [control-protocol.md](../control-protocol.md). This is a plan, not
a finished adapter — and iOS is the hardest target in the tree. Read the
[Memory budget](#memory-budget) section before estimating anything.

## Overview

Every other platform runs sing-box as a **sidecar** — a separate process the core
spawns and drives over stdin/stdout. iOS makes that impossible. The app sandbox
forbids spawning child processes (no `fork`/`exec`), so there is no sidecar to
supervise. sing-box has to run **in-process**, compiled as a library and linked
directly into a **Network Extension**.

That inverts the desktop model in two concrete ways:

- **The engine moves in-process.** Instead of a bundled `sing-box` binary, the
  engine is `libbox` — sing-box's `experimental/libbox` package built into an
  `.xcframework` with gomobile — linked into a `NEPacketTunnelProvider`
  extension.
- **The control protocol moves off stdio.** The line-delimited JSON over
  stdin/stdout from [control-protocol.md](../control-protocol.md) cannot cross the
  app↔extension boundary. It re-homes onto an in-process IPC: libbox's
  `CommandServer`/`CommandClient` over a Unix-domain socket in a shared App Group
  container.

The one thing that carries over cleanly is the part that matters most: Tenebra's
core is a pure config **generator** with no sing-box import (see
[architecture.md](../architecture.md#core-go--core)). It stays a generator here
too — it just gets compiled to its own small `.xcframework` and called in-process
instead of run as a sidecar. The protocol semantics (`connect`, node selection,
routing, the fallback walk) are unchanged; only the transport under them changes.

The official sing-box Apple client (iOS + macOS, GPLv3 — the same license as
Tenebra) is the reference implementation for all of this and can be read and
mirrored legally. Its Network-Extension glue is a small, reusable native module:
the extension target itself is a four-line shim, with the real logic in a shared
library of a few thousand Swift lines.

## Architecture

Two processes, the standard iOS VPN shape:

- a **SwiftUI host app** — node list, connect toggle, settings, subscription
  import — that provisions and observes the tunnel but never touches packets;
- a **`NEPacketTunnelProvider` extension** — a separate, sandboxed daemon the
  system launches on `startTunnel` and tears down on `stopTunnel` or under memory
  pressure — that links `libbox` and runs the engine.

```
 host app (SwiftUI)                                   NE extension
 -----------------                                    ------------
 NETunnelProviderManager       App Group container    NEPacketTunnelProvider
 Tenebra core (xcframework)    group.<bundle-id>:     libbox = sing-box (unmodified)

   GenerateConfig(json) ----->  config JSON  ------->  StartOrReloadService(json)
                                command.sock                |
                                cached .srs rule-sets       +--> utun fd (no root,
                                                                 granted to the NE)

 host app  <---- libbox CommandClient <-> CommandServer ---->  extension
                      (IPC over command.sock, in the App Group)
```

**Two Go modules, kept separate.** The clean design mirrors the desktop split
exactly:

1. **Tenebra core** is gomobile-bound into its **own** small `.xcframework`
   exposing essentially one call — `GenerateConfig(profileJSON) -> configJSON`
   (string in, string out, gomobile-friendly).
2. **sing-box `Libbox.xcframework`** is used **unmodified** as the engine.
3. The host app calls `GenerateConfig`, then hands the resulting JSON to the
   extension's `CommandServer.StartOrReloadService(json)`, which boots sing-box
   internally.

Do **not** merge the two Go modules. Keeping Tenebra's generator free of any
sing-box import is what preserves its stdlib-only purity, its independent version
cadence, and the clean generator-vs-engine boundary the whole project is built
on. They are two artifacts that meet only at a JSON string, the same contract as
on the desktop.

**How the tunnel FD is obtained — no root.** Unlike macOS, iOS never needs root.
The extension implements libbox's `PlatformInterface`, and libbox calls back into
Swift for the platform-owned pieces: the `utun` file descriptor (from the NE's
`packetFlow`), the default-interface monitor, Wi-Fi state, process lookup for
connection ownership, and so on. The OS hands the tunnel FD to the *entitled
extension* — the privilege comes from the Network Extension entitlement, not from
elevation. The core `NEPacketTunnelProvider` overrides are the familiar set:
`startTunnel`, `stopTunnel`, `setTunnelNetworkSettings` (DNS/routes/addresses),
`packetFlow` read/write for the TUN, `handleAppMessage` for app→extension
messages, and `sleep`/`wake`. The extension can be killed at any time, so state
lives in the App Group.

**UI shell note.** The desktop UI is Tauri (a WebView shell); the iOS UI is
planned as **native SwiftUI**, not Tauri. Tauri's iOS support is a
WebView-plus-Rust wrapper with no publicly documented case of hosting a VPN
Network Extension, and the tunnel work here is native Swift regardless of what
draws the UI. This is a deliberate divergence from the desktop shell, and it
means the iOS front end is new code rather than a reused web front end.

## Memory budget

**This is the single biggest risk in the port. Treat it as the schedule driver,
not a detail.**

An iOS packet-tunnel provider runs under a **hard memory cap enforced by jetsam**:
the extension process is fatally killed the moment it exceeds the limit (kill
reason `per-process-limit`), with no soft warning. The number is **~50 MB** on
current iOS (it was 15 MB through iOS 14, raised to 50 MB on iOS 15+; App Proxy
and DNS Proxy providers are still 15 MB). Two important caveats on that number:

- **It is version-dependent and not contractual.** Apple explicitly says not to
  hard-code it and to test across devices and OS versions. Treat 50 MB as the
  working figure for current iOS, confirmed unchanged on the latest release, but
  measure — don't assume.
- **sing-box already hits it.** This is not theoretical. An open sing-box issue
  ([#3976](https://github.com/SagerNet/sing-box/issues/3976)) shows the exact
  fatal jetsam log — `singboxExtension … exceeded mem limit: ActiveHard 50 MB
  (fatal)` — during a high-bandwidth WireGuard speed test. Allocation pressure
  sits in the gVisor buffer pools and the WireGuard receive path (accompanied by
  `ENOBUFS` kernel errors on the utun output); cutting buffer depths reduced
  memory but introduced throughput stalls. **The bug is open and unresolved.**

The cap covers the *whole* extension process — the Go runtime, gVisor, the QUIC
stack, and every linked library, not just heap. That is why running Go in an NE
was discouraged in the 15 MB era and only became viable at 50 MB, and why it
still demands discipline. Mitigations, in rough order of leverage:

- **Cap the Go runtime.** libbox's `Setup` already exposes a memory-limit and an
  OOM-killer knob. Set `GOMEMLIMIT` (equivalently `debug.SetMemoryLimit`) **well
  below** the jetsam ceiling — on the order of ~40 MB — because resident memory
  includes the runtime and the network stacks, not just the heap. Pair it with
  aggressive GC (`debug.SetGCPercent`) and `debug.FreeOSMemory()` so memory is
  returned to the OS promptly; Go's GC does not do that on its own.
- **Don't load geodata into RAM.** The number-one memory sink is loading routing
  databases fully into memory. Use sing-box's **binary rule-set format (`.srs`)**,
  which loads faster and uses far less memory than legacy GeoIP/Geosite, and have
  the **host app** download and cache the rule-sets into the App Group — the
  memory-constrained extension should read them, not fetch or expand them.
- **Bound the buffers.** Queue/channel depths and TCP buffer sizes are the tunable
  that #3976 turns; they trade memory against throughput, so they need real
  measurement rather than a guessed constant.
- **Load-test first.** The only way to know whether a given protocol mix fits is
  to drive real traffic and watch RSS against the ceiling. That is why the staged
  plan puts a memory spike **before** any NE work.

## Building the core: gomobile

`libbox` (and Tenebra's own core) are built for Apple with **gomobile**, but not
upstream gomobile — sing-box maintains a fork (`github.com/sagernet/gomobile`)
because upstream repeatedly broke on new Xcode releases. Pin whatever version the
target sing-box tag's `Makefile` installs (v0.1.13 at time of writing). The shape
of a bind:

```
gomobile bind -target ios,iossimulator \
  -tags "with_gvisor,with_quic,with_utls,with_wireguard,with_clash_api" \
  -o Libbox.xcframework ./experimental/libbox
```

The resulting `.xcframework` carries an `ios-arm64` device slice and an
`ios-arm64_x86_64-simulator` slice. Practical constraints:

- **Build tags are load-bearing.** A missing tag means the protocol is *silently
  absent at runtime*, not a compile error: `with_quic` (Hysteria2/TUIC),
  `with_utls` (REALITY/uTLS fingerprinting), `with_wireguard` (AmneziaWG),
  `with_gvisor` (the userspace TUN stack). Ship exactly the tags the alpha's
  protocol set needs.
- **Do _not_ use `with_naive_outbound` on iOS.** It pulls in Cronet, whose C++
  runtime collides with other libraries' at link time on iOS — a real, hard-to-
  diagnose linker failure. It is also not part of sing-box's own Apple tag set.
- **Toolchain.** Go ≥ 1.24.7 (what the pinned sing-box tag requires when linked),
  and expect the usual sing-box linker workarounds — the `//go:linkname` hacks
  (`checklinkname=0` / `badlinkname` and friends) that recent Go versions
  otherwise reject. The Objective-C headers gomobile emits may need nullability
  annotations patched for a clean Xcode build.
- **No bitcode.** Bitcode was deprecated in Xcode 14 and is gone; build without
  it.
- **macOS + Xcode only.** gomobile's Apple bind shells out to Xcode/clang for the
  Objective-C bridge (cgo is inherently on), so the Apple frameworks **cannot** be
  built on Windows or Linux. This is a hard CI constraint — the iOS artifacts come
  off a macOS runner.
- **Framework size.** `Libbox.xcframework` is tens of MB because it statically
  bundles the Go runtime, gVisor and quic-go. That counts against the app
  *download* size, not the 50 MB *runtime* cap, but it is another reason to
  include only the protocols the build actually ships.

## Entitlements and provisioning

- **Network Extension entitlement — self-serve.** The `packet-tunnel-provider`
  capability is self-serve: enable **Network Extensions** in Xcode's Signing &
  Capabilities (or on the developer portal) and check Packet Tunnel. There is no
  approval email to Apple and it is included in the standard Apple Developer
  Program — unlike the newer URL-filter and relay entitlements, which do require
  Apple's sign-off. The key is `com.apple.developer.networking.networkextension`
  with value `packet-tunnel-provider`.
- **`NETunnelProviderManager`, not Personal VPN.** Tenebra needs a *custom*
  tunnel, so it uses `NETunnelProviderManager` + `NEPacketTunnelProvider`, which
  carry any protocol. The Personal VPN path (`NEVPNManager`) is Apple's built-in
  IKEv2/IPsec only and cannot host sing-box. A packet-tunnel VPN runs on ordinary
  consumer devices — it does **not** require MDM or supervision (only DNS-proxy
  and content-filter providers do).
- **Provisioning from the app.** The VPN profile is created and saved from the
  host app via `NETunnelProviderManager.loadAllFromPreferences` /
  `saveToPreferences`, setting an `NETunnelProviderProtocol` with the extension's
  bundle identifier. The first save triggers a user authorization prompt
  (Face/Touch ID or passcode). Watch for the long-standing "reload after save"
  gotcha — you often must `loadFromPreferences` again immediately after saving
  before the config is usable.
- **On-demand rules.** Auto-reconnect is two properties on the manager —
  `isOnDemandEnabled` and `onDemandRules` (an `[NEOnDemandRule]`, first match
  wins), with rule types Connect / Disconnect / EvaluateConnection / Ignore
  matchable on interface type, SSID, DNS domain, etc. The system evaluates these
  at the kernel level when network conditions change, so the tunnel can come up
  without the app or extension already running.
- **App Group is mandatory plumbing.** The config JSON, the `command.sock`, and
  the cached rule-sets all live in the App Group container
  (`group.<bundle-id>`). Any Unix-domain socket used for app↔extension IPC **must**
  sit in the App Group to be reachable from both sandboxes.

## Distribution

- **TestFlight for the alpha.** Up to 100 **internal** testers (App Store Connect
  team members) need **no App Review** — an internal build can go out immediately.
  Up to 10,000 **external** testers are supported, but the **first external build
  is sent to Apple's beta App Review** and must pass the full guidelines,
  including the VPN rules; internal-only testing is the fast path, external is not
  a review bypass. Builds expire after 90 days.
- **App Store — Guideline 5.4 is a gating requirement.** Apple's App Review
  guidelines state that apps offering VPN services **may only be offered by
  developers enrolled as an organization** — an individual Apple Developer account
  cannot publish a VPN app. Organization enrollment requires a **D-U-N-S number**.
  Beyond that, Apple requires VPN apps to use the `NetworkExtension` APIs, to
  declare their data collection to the user before use, to commit in the privacy
  policy to not selling or disclosing user data, and to provide a VPN license in
  the review notes for territories whose local law requires one. These are
  Apple's stated requirements for the category; a VPN app that doesn't meet them
  is removed. This is a hard prerequisite to plan around, independent of the
  code.
- **License channel note.** The same GPLv3/App-Store tension noted for
  [macOS](macos.md#distribution) applies here; TestFlight and sideloading avoid
  App-Store distribution terms, while an App Store release would need the
  organization enrollment above.

## Staged plan

The ordering is deliberate: **prove the memory budget before building anything
that depends on it.**

1. **Memory spike — _without_ the Network Extension, first.** Build Tenebra's core
   and `libbox` with gomobile, run the engine **in the host app** as a local SOCKS
   proxy (routing only the app's own traffic, no NE, no 50 MB cap), and drive real
   traffic while measuring RSS. This validates the gomobile bind, the two-module
   split, and the `GenerateConfig → StartOrReloadService` handoff, and — crucially
   — produces the first honest read on whether the target protocol mix fits under
   50 MB once the NE cap applies. This is the highest-risk unknown; it goes first.
2. **NE scaffold.** Add the `NEPacketTunnelProvider` extension, the App Group,
   provisioning via `NETunnelProviderManager`, and the libbox
   `CommandServer`/`CommandClient` IPC. Get a minimal tunnel up and re-measure
   memory under the real jetsam cap.
3. **SwiftUI client.** The minimal user surface — node list, connect toggle, basic
   settings, subscription import — talking to the extension. A small fraction of a
   full client's UI.
4. **On-demand rules.** Auto-reconnect on untrusted networks, then the polish that
   makes the tunnel feel native.
5. **TestFlight.** Internal group first (no review, immediate), and only then weigh
   an external/App-Store path against the Guideline 5.4 organization requirement.

## Open questions and risks

- **The 50 MB ceiling (top risk).** Whether Tenebra's full protocol set fits under
  the cap at real throughput is genuinely unknown until step 1 measures it, and
  the underlying sing-box OOM issue (#3976) is open with no clean fix. The memory
  figure is itself version-dependent and must be measured on real devices, not
  assumed. This is the item most likely to force a scope cut (e.g. fewer
  concurrent protocols, tighter buffers, throughput ceilings).
- **gomobile / Xcode churn.** The reason sing-box forks gomobile at all is
  repeated Xcode-compatibility breakage; every Xcode bump is a potential rebuild
  hazard for the frameworks.
- **UI is new code.** iOS diverges from the Tauri desktop shell to native SwiftUI,
  so there is no front-end reuse — only the Go core carries over.
- **App Store organization requirement.** Guideline 5.4 (organization enrollment /
  D-U-N-S) is a non-code prerequisite for any App Store presence; TestFlight
  internal testing is unaffected, but a public release is not possible without it.
- **No native-shell precedent for the hard case.** Hosting a VPN Network Extension
  from a cross-platform WebView shell is undocumented in the wild, which is why the
  plan commits to native SwiftUI for iOS rather than assuming the desktop shell
  ports.
