# Porting to macOS

A plan for the macOS desktop port. For the shared design this builds on, read
[architecture.md](../architecture.md); for the core ↔ UI wire format that carries
over unchanged, [control-protocol.md](../control-protocol.md). This is a plan, not
a finished adapter — nothing here ships yet.

## Overview

macOS is the cheapest port on the board. The reason is structural: Tenebra's core
does not link sing-box, it **generates** a sing-box configuration as plain JSON
and hands it to a real sing-box process at runtime (see
[architecture.md](../architecture.md#core-go--core)). That generator-vs-engine
split is already the whole desktop design, and it is platform-agnostic Go with no
OS-specific imports. The Windows adapter is a thin supervisor around a bundled
sing-box binary; macOS wants the same shape.

Two facts make the reuse almost total:

- **sing-box ships an official macOS binary.** The pinned release publishes
  `darwin-arm64` and `darwin-amd64` command-line builds, cross-compiled with
  `CGO_ENABLED=0` (pure Go — a standard sing-box build needs no cgo). The
  fetch step that today pulls the Windows `.zip` becomes a pull of the darwin
  `.tar.gz`; no Xcode, no gomobile, no source build.
- **The build tags match.** The default standalone darwin build carries
  `with_gvisor`, `with_quic`, `with_utls`, `with_wireguard`, `with_clash_api`
  and the rest — everything Tenebra's protocol set needs (VLESS/REALITY via
  uTLS, Hysteria2/TUIC via QUIC, AmneziaWG via WireGuard, plus the gVisor TUN
  stack and the Clash API we read traffic from). The tag deltas against other
  build variants don't touch anything Tenebra uses.

The single real delta from Windows is **privilege**. Windows opens the wintun
adapter from an elevated process; macOS's `utun` requires root. That one
difference drives the whole "Privilege and the tunnel" section below — everything
else is the Windows adapter with a different binary name.

## Architecture

Same sidecar model as the desktop app today: the core plus sing-box run as a
**sidecar process** beside the Tauri shell, and the UI drives them over the
sidecar's **stdin/stdout** as line-delimited JSON, exactly as documented in
[control-protocol.md](../control-protocol.md). The control protocol does not
change at all — it is transport-agnostic and already proven on Windows.

```
 Tauri UI  <-- line-delimited JSON over stdin/stdout -->  core + sing-box
 (React)                                                    |
                                                            +-- utun tunnel (root)
```

What differs is *who holds root*. On Windows the whole app is elevated and the
sidecar opens wintun directly. On macOS you do **not** run a GUI as root — the
established pattern (Tailscale, Mullvad) is an unprivileged GUI talking to a
privileged background service that owns the tunnel. So the macOS adapter splits
the Windows "elevated everything" into two:

- the **Tauri shell** stays unprivileged (it is just a WebView and a bridge);
- a **privileged helper** owns the `utun` device and the sing-box lifecycle.

sing-box itself makes this simple: run as root, its CLI `tun` inbound opens a
`utun` interface directly (sing-tun opens a `SYSPROTO_CONTROL` socket against
`com.apple.net.utun_control`) — no Network Extension, no entitlement, root is the
only requirement. So the helper's job is narrow: acquire privilege, then start
and supervise the existing sidecar. Whether the *entire* sidecar runs under the
helper or the helper only launches sing-box while `tenebra-core` stays
unprivileged is an open design choice (see [Open questions](#open-questions-and-risks)).

A native Network Extension **System Extension** (the path the official sing-box
macOS client's standalone build takes) is the other way to get an unprivileged
`utun`, but it is heavier — a Developer ID system-extension target, its own
entitlement, and a user-approval flow — and it would mean re-homing the tunnel
off the sidecar. For a port whose entire value is reusing the working desktop
sidecar, the root helper is the natural fit. The System Extension route is worth
keeping in mind if the App Extension model ever becomes attractive, but it is not
the plan here.

## Privilege and the tunnel

The helper needs root once, to open `utun`. Two mechanisms, matched to how far
along the port is:

- **Production — `SMAppService` (macOS 13+).** `SMAppService` (Ventura and later)
  replaces the deprecated `SMJobBless` for installing privileged helpers. The
  daemon `plist` and the helper binary live *inside* the `.app` bundle
  (`Contents/Library/LaunchDaemons/` and `Contents/MacOS/`) and are removed with
  it; registration is `SMAppService.daemonService(plistName:).register()`, and the
  service reports `notRegistered` / `enabled` / `requiresApproval` / `notFound`.
  First registration typically lands on `requiresApproval`: the user is sent to
  **System Settings → General → Login Items & Extensions** to enable the
  background item and authenticate. It requires that **both the app and the daemon
  are signed and match by Team ID** — an unsigned build will not register, which
  is why this is the production path and not the dev one.
- **Development — `osascript` authorization.** For local and trusted-tester
  builds, prompting for admin rights with an `AuthorizationExecuteWithPrivileges`
  /`osascript ... with administrator privileges` flow gets `utun` open without any
  signing ceremony. It is not how you'd ship to end users, but it unblocks
  bring-up on an unsigned build long before certificates are in place.

| Aspect              | Alpha / dev build            | Production build                  |
|---------------------|------------------------------|-----------------------------------|
| Privilege mechanism | `osascript` admin prompt     | `SMAppService` root daemon        |
| Signing required    | none (ad-hoc / unsigned OK)   | Developer ID, app + daemon, Team-matched |
| User approval       | admin password per install    | one-time Login-Items approval     |
| Persistence         | re-prompt as needed           | LaunchDaemon, survives reboot     |
| Who runs it         | contributor's own machine     | any user's Mac                    |

## Distribution

Ship a **DMG or PKG outside the App Store**. This is deliberate on two counts.
First, it sidesteps the well-known tension between GPLv3's "no additional
restrictions" clause and the App Store's usage terms (the VLC precedent) — a
GPLv3 app distributed directly carries no such conflict, and the official
sing-box macOS client already uses exactly this DMG/PKG channel for its
standalone build. Second, the root-helper model above wants a normal installer,
not App Store sandboxing.

For anything beyond your own machine, the binary must be signed and notarized:

- **Developer ID Application** certificate (requires the Apple Developer Program).
  `Apple Development` certificates are for local testing only and warn on other
  machines.
- **Notarization** with `notarytool` (which replaced the retired `altool`):
  `codesign` with the **hardened runtime** (`--options runtime`, mandatory for
  notarization) → `xcrun notarytool submit <artifact> --wait` (authenticating with
  an app-specific password or an App Store Connect API key `.p8`) → `xcrun stapler
  staple`.
- **Gatekeeper reality.** Recent macOS removed the old right-click → **Open**
  escape hatch for unsigned/un-notarized apps; the user must now go to **System
  Settings → Privacy & Security → Open Anyway** and authenticate. In practice this
  makes notarization effectively mandatory for a smooth install by non-technical
  users — an un-notarized Developer ID build still opens, but with steadily more
  friction on newer releases. *(The specifics of the very latest macOS release
  here come partly from secondary sources and should be re-verified on an actual
  current-OS machine.)*

For handing an early build to a trusted tester without the full signing pipeline,
an ad-hoc or unsigned `.app` runs after clearing the quarantine attribute
(`xattr -dr com.apple.quarantine Tenebra.app`). That is a dev convenience, not a
distribution strategy.

## Build and CI

GitHub's hosted **macOS runners are all Apple Silicon** now (`macos-14`,
`macos-15`, `macos-latest` and newer; `macos-13` has been retired) and are free
for public repositories. Build the bundle there with Tauri's universal target:

```
npm run tauri build -- --target universal-apple-darwin
```

Tauri produces a universal (`arm64` + `x86_64`) main binary by `lipo`-ing the two
slices; on an arm64 runner the Intel slice comes from a Rust cross-compile
(`rustup target add x86_64-apple-darwin`). The catch for a VPN client:

- **Every sidecar must also be universal.** Tauri's `externalBin` entries are
  matched by target triple (`sing-box-aarch64-apple-darwin` and
  `sing-box-x86_64-apple-darwin`), and the bundle expects each to be universal or
  per-arch. Since sing-box ships two single-arch darwin binaries, the fetch step
  has to `lipo` them into one universal `sing-box` before Tauri bundles it — the
  macOS analog of today's `fetch-resources.ps1` (see
  [development.md](../development.md#the-desktop-app)).
- **Known caveat: `externalBin` + notarization.** There is an open Tauri bug where
  a bundle carrying an `externalBin` fails notarization with *"The signature of
  the binary is invalid"* ([tauri #11992](https://github.com/tauri-apps/tauri/issues/11992),
  and the related #9422 / #12690); bundles without an external binary notarize
  cleanly. Tauri signs the sidecar, then the main binary, then the bundle, and the
  nested binary's signature is what breaks. A VPN client *is* the
  externalBin-plus-notarization case, so budget for a manual `codesign` pass over
  the embedded sing-box in CI as a likely workaround, and treat this as a risk to
  validate early rather than a solved problem.

Signing in CI follows the usual shape: import the Developer ID certificate into a
temporary keychain, set the `APPLE_*` environment variables (certificate,
signing identity, and either an Apple ID + app-specific password or an API key),
and let Tauri notarize during the build. Tauri's updater works on macOS
(`.app.tar.gz` + a signed manifest), so the release channel can match Windows.

## Staged plan

1. **Adapter scaffold.** Add `adapters/macos` mirroring `adapters/windows` — spawn
   and supervise the bundled sing-box, read traffic over its Clash API. Extend the
   resource-fetch step to pull and `lipo` the pinned darwin binary. At this stage
   the sidecar builds and speaks the protocol; no tunnel yet. Much of this is
   arch-agnostic Go that already compiles on macOS.
2. **Tunnel bring-up.** Get `utun` open. Start with the `osascript` dev path so a
   contributor can validate a real tunnel on an unsigned build, then implement the
   `SMAppService` root helper. This is the step that needs an elevated, real-server
   run to sign off — the same manual validation the Windows tunnel still needs.
3. **CI artifact.** Produce an **unsigned** universal DMG on a macOS runner, so the
   build is reproducible and reviewable before any certificates exist. Confirm the
   `lipo`'d sidecar loads and the app reaches the mock backend end to end.
4. **Production channel.** Add Developer ID signing + `notarytool` notarization,
   resolve the `externalBin` notarization caveat, and wire the updater. This is the
   gate to shipping to anyone who isn't a contributor.

## Open questions and risks

- **What exactly runs as root.** Cleanest privilege separation is the helper
  opening `utun` (or launching only sing-box) while `tenebra-core` stays
  unprivileged; simplest is the whole sidecar under the helper, mirroring the
  elevated Windows process. The trade-off (attack surface vs. plumbing) needs a
  decision before the helper is written.
- **`externalBin` + notarization (#11992).** The single most likely CI blocker for
  a signed release. Needs a proof-of-concept notarized build with the embedded
  sing-box before the production channel can be called done.
- **`SMAppService` signing floor.** The helper cannot be installed by a fully
  unsigned build, so the alpha and production privilege paths genuinely differ —
  the dev `osascript` path won't exercise the code that ships. Plan to test the
  real helper path early on a signed dev build.
- **Newest-macOS Gatekeeper behavior** is partly from secondary sources; verify the
  exact "Open Anyway" flow and any notarization hardening on a current-OS machine
  before writing install docs for end users.
- **Live tunnel validation.** As on Windows, no automated test stands up a real
  `utun` + sing-box against a live server; that path can only be signed off by a
  manual, privileged run.
