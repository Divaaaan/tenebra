# Roadmap

Where Tenebra is and where it's going. This is a direction, not a schedule —
Tenebra is pre-1.0, priorities shift, and nothing here carries a date. If you
want to weigh in, open a thread in
[Discussions](https://github.com/Divaaaan/tenebra/discussions).

## Shipped

Working today on the Windows, macOS and Linux desktop clients:

- All sing-box protocols — VLESS/REALITY, Hysteria2, AmneziaWG, Shadowsocks,
  Trojan, VMess.
- Import from a subscription URL, share links, a `.txt` list, clipboard, a QR
  image, or a **Clash / Mihomo YAML** config.
- Smart / Global / Direct routing with LAN bypass, and per-app split tunnelling
  (the per-app matching is unproven on Linux).
- Three routing presets on top of that: censored services pinned into the tunnel
  (or onto the bypass when one runs), game clients direct, and real-time UDP
  direct.
- **Custom routing rules in the UI** — domain suffixes pinned to the direct path
  or into the tunnel, plus the RU banking and government presets. Editing them
  re-applies to a live tunnel rather than asking for a reconnect.
- **Multi-hop** — two of your own nodes chained entry → exit, so the exit never
  sees your entry address.
- Protocol fallback that remembers the last known-good node, and re-tries the
  same node under a reshaped handshake when the failure looks like interference
  rather than a dead server.
- **Changing the exit without dropping the tunnel.** The node is switched inside
  the running sing-box — same process, same tun — so connections already open
  finish where they started. What cannot be switched that way falls back to a
  reconnect, and the UI says which of the two happened.
- **A DPI bypass compiled into the Windows build.** Tenebra drives
  [zapret](https://github.com/bol-van/zapret) beside the tunnel, and one bundle
  release ships inside the binary: it is unpacked when the daemon starts, so a
  censored network cannot leave a fresh install with no bypass at all. A newer
  release replaces it once upstream publishes one this build pins, re-checked
  every twelve hours, and the strategy that carries traffic is chosen by
  measuring the bundle against real destinations. Windows only — see
  [DPI bypass](README.md#dpi-bypass).
- **Connection diagnostics** — an on-demand UDP / STUN check (reachability, NAT
  type, external address) and a speed test through the active tunnel.
- **A support report in one action** — state, versions, routing, the last
  fallback walk and the tail of the log, scrubbed of credentials — and a Windows
  service log that rotates by size instead of growing without a ceiling.
- **Opt-in crash reporting** — off until you turn it on, and local: a crash left
  behind by a previous run is shown so it can be pasted into an issue. Nothing
  is sent anywhere.
- DNS resolver choice plus an opt-in ad / tracker blocklist.
- Kill switch, honest leak check, system tray, notifications, `tenebra://` deep
  links, autostart, live traffic graphs, light / dark themes, English / Russian.
- Signed in-app auto-updater with **Stable** and **Beta** channels.
- **Linux as a desktop platform** — a root systemd service owns the tunnel and
  the unprivileged app attaches to it, installed by an Arch package (built from
  source in the release workflow) or a `sudo` script, with `.deb` and AppImage
  bundles beside them. Read the
  [Linux note](README.md#linux-note--the-tunnel-needs-a-root-service) first.

## In progress

- **Android, in alpha.** [`ui-android/`](ui-android/README.md) is a Kotlin /
  Compose client driving the same Go core through a `VpnService` and libbox:
  subscription import, a node list with latency badges, an AUTO exit that follows
  the lowest ping, switching the live exit over the engine's control API,
  connect-on-boot, a Quick Settings tile, and in-app logs and crash reports.
  It is installed by hand — CI builds a debug APK from `main` and from pull
  requests — and routing is *Global* only for now. There is no DPI bypass there:
  that component is a Windows packet filter.
- **Signing the tunnel off on macOS and Linux.** On Windows this stopped being
  the open question some releases ago: the service, the wintun path, the connect
  walk and the bypass are run against real servers on censored networks, and
  most of what the 0.5.x releases fix was found that way rather than in a test.
  macOS and Linux both build, install their privileged helper and start, but
  neither has had a privileged live run signed off end to end; what is still
  unmeasured on each — DNS on `systemd-resolved` hosts and per-app matching on
  Linux among them — is listed in
  [docs/porting/macos.md](docs/porting/macos.md) and
  [docs/porting/linux.md](docs/porting/linux.md). No automated test stands up a
  real tunnel on any platform: that needs privilege and a real server.

## Planned

- **A signed APK on the release.** The Android workflow already builds and signs
  a release APK on a tag; it needs the release keystore in CI secrets before a
  tag can ship one.
- **Windows installer code signing** — the in-app updater's artifacts are
  minisign-verified already, but the first download is Authenticode-unsigned and
  SmartScreen warns for it.
- **Click-to-run macOS** — a signed, notarized build with the privileged daemon
  inside the app bundle (`SMAppService`), so it installs and updates the way the
  Windows service does instead of through a `sudo` script. Needs an Apple
  Developer ID.
- **An AUR package.** The `PKGBUILD` only lives in the repository for now, so
  Arch users clone before they can build it.
- **The DPI bypass beyond Windows.** `core/zapret` drives a Windows packet
  filter; macOS and Linux compile a stub, and censored services there ride the
  tunnel through the exit node instead of going direct.
- **Full AmneziaWG obfuscation.** An AmneziaWG node imports and connects today,
  but stock sing-box carries none of the AWG obfuscation parameters and rejects
  them outright, so the tunnel runs as plain WireGuard. Real support means a
  fork that links AmneziaWG and a build that ships it.
- **iOS.** `ui-ios/` is a structural scaffold — none of its Swift has been
  compiled and no framework has been built. The plan is in
  [docs/porting/ios.md](docs/porting/ios.md).

## Exploring

Bigger ideas, not committed:

- Adaptive transport tuning per network — remembering what a given network
  tolerates instead of rediscovering it on every connect.

---

Done items move up to **Shipped**; see the [changelog](CHANGELOG.md) for what
landed in each release and [project status](README.md#project-status) for the
honest state of each layer.
