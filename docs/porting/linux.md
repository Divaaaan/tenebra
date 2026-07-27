# Linux

How the Linux desktop build is put together, and what it does not do yet. For
the shared design this builds on, read [architecture.md](../architecture.md);
for the core ↔ UI wire format that carries over unchanged,
[control-protocol.md](../control-protocol.md). The macOS port
([macos.md](macos.md)) is the closest relative — read that first if you want the
reasoning behind the split-privilege shape, because Linux reuses it almost
verbatim.

## Overview

Linux lands close to macOS and far from Windows, for one reason: the tunnel needs
privilege the GUI must not have. Windows solves that with an elevated background
service and a per-machine install; macOS with a root `LaunchDaemon`; Linux with a
root **systemd service**. Everything above that line — the Go core, the config
generator, the control protocol, the React UI — is the same code.

Three facts make the port cheap:

- **sing-box ships official Linux binaries.** The pinned release publishes
  `linux-amd64` and `linux-arm64` command-line builds with the tags Tenebra needs
  (`with_gvisor`, `with_quic`, `with_utls`, `with_wireguard`, `with_clash_api`),
  and `CGO: disabled`. The fetch step pulls the tarball, exactly as on macOS.
- **The tun device is a plain character device.** `sing-box` opens
  `/dev/net/tun` and programs routes over netlink itself once it has
  `CAP_NET_ADMIN`. No kernel module of our own, no NetworkExtension, no
  entitlement — the whole privilege story is "run the core as root".
- **The control protocol already had a unix-socket transport.** It was written
  for the macOS daemon; Linux binds the same socket at
  [`/run/tenebra.sock`](../control-protocol.md) and the desktop shell attaches to
  it the same way.

What is genuinely different from macOS is **packaging**, not plumbing. macOS has
one filesystem layout (an `.app` bundle) and one installer story; Linux has as
many as it has distributions, so this port carries a hand-install script for any
of them and a native package for Arch, which is the distribution being targeted
first.

## Architecture

```
 Tauri UI  ──── line-delimited JSON over /run/tenebra.sock ────►  tenebra-core (root)
 (React, unprivileged)                                              │
                                                                    └─ sing-box ── /dev/net/tun
```

The daemon owns the tunnel and outlives any one UI process; the GUI is a WebView
and a bridge. When nothing answers on the socket the shell falls back to spawning
its own unprivileged sidecar — useful for development, but that sidecar cannot
open a tun device, so it cannot connect.

Running as root, the core pins two machine-scoped paths instead of the per-user
defaults it would otherwise pick out of root's home:

| What | Where | Enforced by |
|------|-------|-------------|
| Control socket | `/run/tenebra.sock`, `0666` | `ListenSocket`, narrowed per connection by the `SO_PEERCRED` peer check |
| Profile store | `/var/lib/tenebra/data`, root-owned `0700` | the core's own clamp on every start |
| sing-box + rule-sets | the install directory (see below) | `TENEBRA_SINGBOX`, then `adapters/linux.InstallDirs` |

The socket is world-writable on purpose: any local user's GUI must be able to
attach, and the authorization decision is made per connection from the peer's
credentials rather than by file mode. The store is the opposite — it holds
subscription credentials, so it is root-only and reachable only through the
protocol.

## Privilege and the tunnel

The unit is [`deploy/linux/tenebra.service`](../../deploy/linux/tenebra.service).
It runs `tenebra-core --socket` as root, restarts it forever (the
process-supervision backstop under the kill switch — nothing inside the core can
restart the core), and sandboxes what that root process can reach.

The sandbox is the part worth reading. A VPN daemon is exactly the workload most
systemd hardening presets break, so every knob was chosen against one rule:
nothing may interfere with the tun device, routing or DNS. In particular, four
common settings are deliberately **absent**, and each would break the tunnel:

| Not set | Why it would break |
|---------|--------------------|
| `PrivateDevices=` | replaces `/dev` with a minimal set that has no `/dev/net/tun` |
| `ProtectKernelTunables=` | policy routing for a tun device is configured partly through `/proc/sys` — reverse-path filtering above all — which this mounts read-only |
| `ProtectProc=`, `ProcSubset=` | per-app split tunnelling matches traffic by reading other processes' `/proc` entries |
| `ProtectSystem=strict` | would take `/run` read-only with everything else, and the control socket is bound there |

What *is* set: a capability bounding set of exactly `CAP_NET_ADMIN`,
`CAP_NET_RAW`, `CAP_NET_BIND_SERVICE`, `CAP_SYS_PTRACE`, `CAP_DAC_READ_SEARCH`
and `CAP_CHOWN`; `DevicePolicy=closed` with `/dev/net/tun` allowed back;
`ProtectSystem=full`, `ProtectHome=yes`, `PrivateTmp=yes`; the usual
`RestrictNamespaces`/`RestrictRealtime`/`RestrictSUIDSGID`/`LockPersonality`
trio; `RestrictAddressFamilies=` narrowed to the five a tunnel uses; and
`SystemCallFilter=@system-service`. Each carries a comment in the unit explaining
why it is safe here.

Two consequences are worth calling out:

- **`CAP_SYS_MODULE` is not in the bounding set**, so the daemon cannot pull in
  the `tun` module itself. On a kernel that ships `tun` as a module and has not
  loaded it, the first connect would otherwise fail with an opaque `ENODEV`. Both
  install paths therefore drop a `modules-load.d` entry (and the script also
  `modprobe`s it for the current boot).
- **`TENEBRA_CONFIG_DIR` is not set in the unit.** The core pins
  `/var/lib/tenebra/data` and clamps it to root-owned `0700` only when the
  variable is *unset*, because an operator-supplied value is honoured as an
  override everywhere else. Setting it in the unit would look tidier and quietly
  skip the clamp.

## Installing

Two supported paths. They deliberately use different prefixes so they cannot
collide:

| | Package (Arch) | Hand-install script |
|---|---|---|
| Core, sing-box, `.srs` | `/usr/lib/tenebra/` | `/usr/local/lib/tenebra/` |
| Unit | `/usr/lib/systemd/system/tenebra.service` | `/etc/systemd/system/tenebra.service` |
| GUI | `/usr/bin/tenebra` | installed separately (AppImage, `.deb`, or a local build) |
| Updates | `pacman` | re-run the script |

`/usr/local` is the half of the hierarchy a package manager never touches, and
`/etc/systemd/system` takes precedence over `/usr/lib/systemd/system` — so a
hand-install on a machine that also has the package silently wins. Both scripts
say so out loud when they see the other's unit.

### The script

```
# fetch sing-box and the rule-sets first
bash scripts/fetch-resources.sh

# build the core from this checkout and install the daemon
sudo bash scripts/linux/install-daemon.sh --dev

# or install from an already-built payload directory
sudo bash scripts/linux/install-daemon.sh --from-dir /path/to/payload
```

It is safe to re-run: it stops the service, replaces the binaries, starts them
again, and restores the previous install if any step fails. Replacing a running
executable is not optional to get right on Linux — an in-place overwrite fails
with `ETXTBSY` — which is why an upgrade necessarily drops an established tunnel.

There is no signature to check the way the macOS script leans on
`codesign`/`spctl`, so the gate it uses instead is *who could have written the
payload*: a source directory that is group- or world-writable is refused, because
everything in it is about to run as root. `--allow-unsafe-source` is the explicit
escape hatch.

Removal is [`scripts/linux/uninstall-daemon.sh`](../../scripts/linux/uninstall-daemon.sh);
it keeps `/var/lib/tenebra` unless given `--purge`.

### Arch Linux

Tauri's bundler has no pacman target — its config schema only knows `deb`, `rpm`,
`appimage`, `msi`, `nsis`, `app` and `dmg` — so the Arch package is a normal
`PKGBUILD` in [`packaging/arch/`](../../packaging/arch/PKGBUILD) that builds the
desktop binary with `--no-bundle` and installs it itself.

```
cd packaging/arch
makepkg -si
```

It builds the core with Go, the shell with Rust and npm, and pulls in the pinned
sing-box and rule-sets as checksummed sources. **sing-box is not in Arch's
official repositories** (only the AUR carries it), so depending on the system
package was not an option; the release binary is pinned by SHA-256 the same way
the Windows and macOS bundles pin it, which also keeps the engine on the exact
version Tenebra's config generator targets.

Two things differ from the script path beyond the prefix:

- **The in-app updater does nothing for a packaged install.** Tauri's updater can
  replace an AppImage, not files owned by `pacman`. Updates come from `pacman
  -Syu`, and the app should not be expected to offer one.
- **Nothing is started for you.** The scriptlet prints what to run
  (`systemctl enable --now tenebra.service`) rather than enabling the service
  behind your back, and an upgrade deliberately leaves the running daemon alone
  so a live tunnel is not dropped under you — restart it when convenient.

`.SRCINFO` is not committed and there is no AUR package: the `PKGBUILD` simply
lives in the repository for now.

## Bundled resources

[`scripts/fetch-resources.sh`](../../scripts/fetch-resources.sh) serves both Unix
targets and dispatches on `uname -s`. It is one script rather than two because
the halves that drift are the shared ones — the pinned sing-box version, the
three rule-set commits and their SHA-256 digests. Those pins already live in
three places (this script, `fetch-resources.ps1` for Windows, and the `PKGBUILD`,
which needs them in its own `source()`/`sha256sums()` arrays for makepkg to
verify); a separate Linux script would have made it four. The platform-specific
part is small: macOS fetches two darwin slices and `lipo`s them into the
universal binary Tauri's universal target requires, while Linux fetches one ELF,
because an ELF holds exactly one architecture.

```
bash scripts/fetch-resources.sh                # this host's architecture
bash scripts/fetch-resources.sh --arch arm64   # cross-fetch (Linux only)
```

`amd64` and `arm64` are pinned. sing-box also publishes `386` and `armv7` builds;
they are deliberately not pinned, because the bundle they would go into is a
webkit2gtk Tauri app and 32-bit Linux desktops are not a target. The release
archive also carries a `libcronet.so` that only sing-box's Naive outbound
`dlopen`s — a protocol Tenebra's generator never emits — so it is left out rather
than shipped as an unused ~40 MB blob.

Every download is checksum-verified and a mismatch is fatal: these binaries are
bundled verbatim into a privileged tunnel, so a swapped upstream artifact must
never reach a build.

## What is not supported

- **System-proxy mode.** Pointing a Linux desktop at a proxy means writing
  per-desktop settings (GNOME's `gsettings`, KDE's `kioslaverc`, a session's own
  environment) as the logged-in user, which a root daemon has no session bus to
  reach. Arming it logs and stays disarmed — it degrades quietly rather than
  failing. Tun mode, the default, is unaffected.
- **AmneziaWG obfuscation.** As on every other desktop platform, the bundled
  stock sing-box applies none of the AWG obfuscation parameters: an AmneziaWG
  link imports and connects, but the tunnel runs as plain WireGuard.
- **musl systems.** The upstream sing-box Linux binaries are dynamically linked
  against glibc, so Alpine and other musl distributions need a sing-box of their
  own; point `TENEBRA_SINGBOX` at it.
- **32-bit and non-amd64/arm64 architectures.** No pinned artifacts, no bundle.
- **Non-systemd inits.** The install script refuses on a machine where systemd is
  not PID 1. The daemon itself is just `tenebra-core --socket` run as root, so an
  OpenRC or runit service is a small piece of work — it is simply not written.

## Open questions and risks

- **DNS on systemd-resolved hosts.** sing-box installs DNS by routing it into the
  tunnel rather than by rewriting `resolv.conf` the way `wg-quick` does. On a host
  whose resolver is the `127.0.0.53` stub, queries stay on loopback and never
  enter the tunnel, so they can be answered by the link's own upstream. The unit
  orders itself after `systemd-resolved` and leaves `/proc/sys` writable so the
  routing side can do its job, but the interaction has not been measured against a
  live tunnel and is the most likely source of a leak on Linux.
- **The sandbox has been proven not to block startup, not to pass traffic.** The
  unit was verified with `systemd-analyze verify`, and a real `tenebra-core` was
  run under it — the socket comes up, `/dev/net/tun` opens, `/proc/sys` is
  writable, other devices are blocked. What no automated run covers is a live
  tunnel against a real server with routes installed; as on Windows and macOS,
  that can only be signed off by a manual, privileged run.
- **`MemoryDenyWriteExecute=` is left off.** The core starts fine with it, but it
  cannot be exercised against a live sing-box tunnel here, and a Go daemon with no
  JIT gains little from it. Revisit with a real tunnel to hand.
- **Per-app split tunnelling on Linux is untested.** The capabilities it needs
  (`CAP_SYS_PTRACE`, `CAP_DAC_READ_SEARCH`) are in the bounding set and `/proc` is
  deliberately left visible, but no run has confirmed sing-box actually matches a
  process by name through them.
