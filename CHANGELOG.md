# Changelog

All notable changes to Tenebra are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/).

> **Early days.** Tenebra is at 0.x: the desktop clients (Windows and macOS) are
> the current focus — see the
> [project status](README.md#project-status). Expect breaking changes between
> 0.x releases.

## [Unreleased]

### Added

- **Adaptive transport escalation.** When a node's entry accepts a TCP connection
  but its handshake then makes no progress and draws no reset — the signature of
  destination-level interference, as distinct from a dead server — the connect
  walk now re-tries the *same* node under a different transport strategy (a
  reshaped TLS handshake: the uTLS fingerprint, then the SNI) before moving on to
  the next node. A failure classifier separates this case from a dead entry (a
  reset or an unreachable/unresolvable address, which advances straight to the
  next node) and from an ambiguous one, so a strategy escalation is spent only on
  real evidence of interference — not on ordinary packet loss or a downed host.
  The fallback `attempts` snapshot gains two optional fields: `strategy` (the
  non-default variation a candidate is being tried under or came up on) and
  `reason` (`"censored"` when a node was abandoned after its handshake looked
  interfered with), so the walk narrates the adaptation for the UI.

## [0.3.7] - 2026-07-11

### Security

- **The control channel now authenticates the connecting peer.** The pipe
  (Windows) and socket (macOS) that drive the privileged tunnel were reachable by
  any local user; earlier releases stopped credential *disclosure* but not
  *control*. The daemon now checks the peer's uid (macOS `LOCAL_PEERCRED`) or
  token SID (Windows `GetNamedPipeClientProcessId`) at accept time and admits only
  its own account and the interactive console (logged-in) user — narrowing the
  documented Tailscale-style "any local user" trust to "the logged-in user". An
  unauthorized peer is refused without disturbing the live session; if the console
  user cannot be determined the check fails open with a warning, so an
  unprivileged GUI can never be locked out of attaching.

### Security

- **Credential redaction is now complete.** Wave 1 redacted `list_profiles` and
  `status`; the `import`, `import_links` and `refresh` responses still returned
  the full profile (subscription token + node secrets) over the local control
  channel. All of them now return the same redacted view; the profiles event was
  already bodyless.
- **DNS no longer leaks under split tunnelling.** With the base mode `direct` and
  a node included into the tunnel, the app's traffic went through the proxy while
  its DNS query fell through to the direct resolver and resolved outside the
  tunnel — leaking every visited domain to the local ISP from the real IP.
  Included apps' DNS now follows the tunnel in every mode.
- **The Windows service directory and log are hardened against squatting.** The
  `%ProgramData%\Tenebra` parent is clamped with a protected SYSTEM/Administrators
  DACL before the log is opened, a pre-planted symlink/junction at the log path is
  rejected, and the data-dir owner and DACL are verified after clamping (fail
  closed), mirroring the macOS check.
- **The macOS privileged data-dir clamp is symlink-safe.** It now opens the
  directory with `O_NOFOLLOW` and operates on the descriptor, so a symlink planted
  at the path can neither redirect the clamp nor survive its verification. The
  hand-install script also verifies the binaries it installs.
- **The desktop shell is tightened.** The sidecar spawn capability is pinned to
  zero arguments and the rest of the shell surface is explicitly denied, so an
  injected script cannot repoint the core or sing-box; a `tenebra://connect` deep
  link now requires an explicit confirmation instead of auto-connecting; and an
  unresolved core/sing-box path fails closed instead of resolving a bare name from
  the working directory.
- **Bundled binaries are pinned by hash.** The fetch scripts now verify a SHA-256
  digest for the sing-box binary and the rule-sets before they are bundled into a
  signed release, so a tampered upstream artifact fails the build.
- **The node-latency probe fan-out is bounded** (a worker pool caps concurrent
  dials), and the connectivity probe now uses HTTPS.
- **IPv6 no longer bypasses the tunnel.** The tun interface carried only an IPv4
  address, so on a dual-stack host `auto_route` never claimed the IPv6 default
  route and native IPv6 traffic egressed around the VPN. The tun now also carries
  a private IPv6 (ULA) address, so IPv6 is routed into the tunnel and follows the
  same rules as IPv4; on a single-stack host the extra address is inert.
- **Stored credentials are no longer served over the local control channel.**
  `list_profiles` returned the full profile — the subscription URL with its
  embedded token plus every node's UUID/password/keys — to any local client of
  the pipe/socket, defeating the root-only permissions on the data directory. The
  response is now a redacted view carrying only what the UI renders (id, name,
  host, port, protocol); the connect path still uses the full stored profile
  internally.
- **Subscription fetch is hardened against SSRF and cleartext leaks.** Non-HTTP(S)
  schemes are rejected, plain `http://` is warned about (host only, never the
  token), and the fetch — across every redirect hop and DoH-resolved address —
  refuses loopback, link-local/metadata (169.254.169.254), and private ranges.
  An opt-out env var covers operators who genuinely self-host on a private range.
- **A malicious Clash subscription can no longer crash the daemon.** The
  hand-written YAML decoder recursed without a depth limit, so a crafted body
  could overflow the stack. Recursion is now capped and the top-level parse is
  wrapped so a decoder fault degrades to a skipped import instead of taking down
  the process.
- **Insecure nodes are surfaced in the UI.** A node whose subscription sets
  `skip-cert-verify` (disabling TLS certificate verification) now shows a warning
  badge and a per-profile summary, so the trade-off is visible rather than silent.

### Fixed

- **Node latency (ping) is measured outside the tunnel.** While connected, the
  per-node ping dialed through the tunnel and reported ~1-2 ms for every node; it
  now binds to the physical default interface so the readout reflects real
  round-trip time to each server.

## [0.3.4] - 2026-07-11

### Fixed

- **macOS release build.** The 0.3.3 release could not bundle the macOS app —
  the universal build expects the core sidecar as two per-arch binaries plus a
  lipo'd universal, and the release job shipped only the universal one. Both
  platforms now build and publish together, so the macOS app and everything it
  brought in 0.3.3 reaches users with this release. (The 0.3.3 Windows build was
  unaffected and shipped normally.)

## [0.3.3] - 2026-07-11

### Added

- **macOS desktop app.** The desktop client now builds and ships for macOS
  (universal: Apple Silicon and Intel). The tunnel follows the platform's
  privilege model instead of an elevated app: `tenebra-core --socket` serves the
  control protocol on a unix domain socket, a root LaunchDaemon owns the tunnel
  (install/uninstall scripts under `scripts/macos/`, hand-installed once with
  sudo — see `docs/porting/macos.md`), and the unprivileged app attaches to it,
  reconnecting through restarts with the same grace the Windows service client
  uses. Without the daemon the app still runs its own unprivileged sidecar:
  everything except opening the tun device works. Releases now carry the macOS
  app and in-app updates alongside the Windows installer; the build is
  unsigned, so the first launch needs System Settings -> Privacy & Security ->
  Open Anyway.
- **Clash/Mihomo YAML subscriptions.** A subscription whose body is a
  Clash/Mihomo config — servers under a top-level `proxies:` key — is now
  detected and imported alongside the existing base64 and plaintext link lists.
  Each proxy is mapped onto the same node model as the share-link parsers:
  Shadowsocks, VMess, VLESS (incl. REALITY), Trojan, Hysteria2 and WireGuard are
  supported, with their transport, TLS and obfuscation options; a proxy of an
  unsupported type is skipped rather than failing the whole import. The YAML is
  read by a small purpose-built decoder, so the core keeps its no-third-party-
  dependency footprint.
- **DNS ad and tracker blocking (opt-in).** A new Settings toggle, off by
  default, refuses DNS lookups for a bundled list of ad and tracker domains
  (answered `REFUSED`, ahead of any routing rule, in every mode). The blocklist
  is the `category-ads-all` set from the same public SagerNet source as the RU
  geodata, compiled to a local `.srs` and bundled with the app — it is loaded
  strictly from disk and never fetched at runtime, so it cannot reintroduce the
  startup stall a remote rule-set caused. The core exposes it through the new
  `set_dns` command, persists it in `settings.json`, and reports it back as
  `ad_block` in `State`.
- **Custom DNS resolvers.** Settings now has two resolver fields — the encrypted
  resolver used over the tunnel and the direct resolver for destinations kept off
  it — prefilled with the values in effect. They accept the usual schemes
  (`tls://`, `https://`, `quic://`, `h3://`, `tcp://`, `udp://`, or a bare host);
  a malformed entry is flagged inline and refused by the core, and an empty field
  falls back to the default. Like the kill switch, a change re-applies to a live
  tunnel in place (a brief reconnect on the same node) rather than waiting for the
  next connect. Both resolvers ride the same `set_dns` command and are persisted
  and reported back in `State`.

## [0.3.0] - 2026-07-09

### Added

- **Autoconnect is core-owned and survives reboots.** The "Connect on launch"
  preference moved from the desktop app into the core: the daemon persists it
  in its `settings.json` (new `set_autoconnect` command, reported back as
  `autoconnect` in `State`), records the last successful connect on its own —
  the profile, plus the node only when one was explicitly chosen — and
  reconnects to it whenever the daemon starts. With the core installed as the
  Windows service that means the tunnel comes up at **system boot**, before
  anyone logs in, and comes back after service restarts such as updates; with
  the spawned sidecar it still connects when the app opens, as before. A last
  profile or node that no longer exists leaves the core idle rather than
  guessing another exit. The app's toggle now drives the core setting, and an
  enabled legacy renderer-side flag is migrated into the core once, on the
  first launch that sees it.
- **The installer now installs the core as a Windows service.** The NSIS
  installer is per-machine (one UAC elevation per install or update; earlier
  releases installed per-user) and registers `tenebra-core` as the auto-start
  `tenebra` service: stopped before an update replaces its files, re-pointed
  and restarted after, removed on uninstall. As a service the core keeps its
  profile store in `%ProgramData%\Tenebra\data` — created with a DACL that
  admits only SYSTEM and Administrators, since profiles carry subscription
  credentials and unprivileged users reach them through the pipe protocol —
  and resolves the bundled sing-box, wintun and rule-sets from `resources\`
  next to `tenebra-core.exe`. Console and sidecar runs keep their per-user
  paths. Upgrading over a per-user 0.2.x install retires the old copy for the
  installing user (uninstall entry, autostart, `tenebra://` handler,
  shortcuts); the old `%LOCALAPPDATA%\Tenebra` files are left on disk, inert,
  rather than have the elevated installer execute or delete through a
  user-writable directory, and per-user profile stores are not migrated —
  re-import the subscription in the app.
- **Windows service mode for the core.** Started by the service control manager,
  `tenebra-core` serves the control protocol on the `\\.\pipe\tenebra` named pipe
  (DACL: SYSTEM, Administrators, the interactive user — the Tailscale LocalAPI
  trust model) instead of stdin/stdout, logs to `%ProgramData%\Tenebra\service.log`,
  and tears the tunnel down on service stop. One client session is active at a
  time: a new connection displaces the old, and a client disconnecting leaves the
  tunnel up. `tenebra-core --pipe` serves the same transport from a console for
  development. Installing the core as the service (installer work) is a separate
  step.
- **The desktop app attaches to a running service.** At startup the GUI probes
  the control pipe: if a core is listening — the installed service, or
  `tenebra-core --pipe` — it attaches to it instead of spawning its own
  sidecar, re-syncing state with a `status` request on every new session. A
  dropped session (service restart, another client taking the pipe over) is
  reported in the UI and redialed with capped backoff until the service is
  back. With no service listening the app spawns the stdio sidecar exactly as
  before; `TENEBRA_PIPE` renames the pipe or (`off`) skips the probe during
  development.
- **Update prompt on launch.** The desktop app now checks for a new signed
  release once at startup and offers it in a slim banner under the top bar —
  "Update" installs and restarts, "Later" hides it until the next launch. An
  offline or failed check stays silent. A new Settings toggle ("Install updates
  automatically") skips the banner and applies a found update right away,
  restarting into the new version; if that silent install fails, the banner is
  shown instead.

### Changed

- **A dropped service connection is no longer an instant error.** When the
  control-pipe session ends mid-run, the app now shows a "Reconnecting to the
  Tenebra service…" status while it redials, and only reports an error (state
  and notification) if the service stays away past a short grace window (8 s).
  A service restart during an update, or another client briefly taking the
  session over, comes back well inside the window and no longer flashes
  "Connection failed" while the tunnel is in fact fine.
- The installer artwork now matches the app: a branded sidebar and header on
  the installer pages, and the eclipse mark as the installer icon.
- The version badge in the top bar is derived from `package.json` at build
  time instead of a hand-bumped literal, which had been left at v0.1.1 in the
  0.2.0 release.

### Fixed

- The installer's service registration silently failed on every install: the
  `binPath` quoting collapsed into a form that `sc.exe` splits at the space in
  "Program Files", so it printed its usage text (exit 1639) instead of
  creating the service, and `nsExec` swallowed the output. The path is now
  escaped so it survives argv splitting in one piece — and the registered
  image path is quoted, closing the unquoted-service-path gap as well.

## [0.2.0] - 2026-07-09

### Added

- **Kill switch**, now armable from the UI. While it is on, the tunnel's
  `strict_route` blocks traffic that would otherwise leak, and an unexpectedly
  dead sing-box process is relaunched automatically on the same node (bounded so a
  crash-loop can't churn forever, with the budget refunded once a relaunched tunnel
  stays up). It is best-effort by design: in the brief window between the process
  dying and the relaunch, the OS routes normally — documented, not hidden.
- **Switchable TUN stack** (`system` / `gvisor` / `mixed`) in Settings, applied to
  a live tunnel without a manual reconnect.
- **Reactive tray**: the tray icon reflects the connection state (idle / connected /
  error) and the Connect / Disconnect items enable and disable to match.
- **Desktop notifications** on real connection transitions (connected, disconnected,
  error, kill switch engaged), debounced so a steady state never repeats a toast.
- **Deep links**: `tenebra://import?url=…` opens the import flow pre-filled, and
  `tenebra://connect?profile=…` connects a profile. Links are parsed in one place
  and delivered to the app whether it is already running or launched by the link.
- **Launch minimized**: with autostart enabled, a login launch starts hidden in the
  tray while auto-connect still runs.
- **DoH fallback for subscription fetch.** When a subscription host fails to fetch
  at the transport layer — the fingerprint of DNS tampering — the client retries
  once over a resolver reached by DNS-over-HTTPS, dialed to the resolver's literal
  IP so it bypasses the system resolver while keeping the original TLS SNI. No new
  dependency; the primary path is unchanged.
- **macOS and iOS porting plans** under `docs/porting/`.

### Changed

- New application icon: an eclipse-corona mark replacing the placeholder set.
- Release pipeline hardening: tagged releases are gated on the full test suite and a
  version/tag consistency check, the update-signing key is confined to the tagged
  release instead of every CI run, the version lives in one script across its four
  files, and eslint, clippy and rustfmt now run in CI.

### Fixed

- Node selection could drift onto the wrong server when a profile held a node with
  a known protocol but invalid parameters (a REALITY entry with no public key, a
  VLESS entry missing its UUID, a bad port). The config generator drops such nodes,
  but the selector-tag and fallback-candidate walks did not, so a tag could land on
  a node the tunnel never built — routing through a different exit than the one the
  UI showed. All three walks now drop the same nodes.
- Shadowsocks nodes that require a transport plugin (v2ray-plugin, obfs, shadow-tls)
  were imported and then built into a plain outbound with the plugin dropped, so the
  tunnel looked connected while its handshake silently mismatched. Such a node is now
  recognised as unsupported and skipped like any other node the config generator
  can't render, rather than connecting without the plugin.
- Kill-switch races: a toggle during the connecting window is now reconciled onto the
  tunnel that comes up (instead of being reported but not applied), and a relaunch
  can no longer resurrect a tunnel after an explicit disconnect or outlive shutdown.
- The Settings radio groups now move focus with the selection, so arrow keys traverse
  the full set instead of stalling after one step.
- `cargo test` no longer fails on macOS and Linux (a test hardcoded a Windows-only
  child process).

### Security

- The sing-box clash API — the loopback control surface the client polls for
  traffic counters and connectivity probes — now requires a per-run secret.
  Without one, any other local process could read the active connection list or
  switch the selected outbound over `127.0.0.1`. The secret is drawn from a
  cryptographic RNG on each run and presented as a bearer token by the client's own
  polling, so the app keeps working while other local processes are turned away.
- The update-signing key is no longer exposed to routine CI: it was injected into the
  desktop build on every push and pull request, and is now confined to the tagged
  release workflow. CI builds the installer with updater artifacts turned off.
- `SECURITY.md` now documents the update-signing key's custody, rotation and
  leak-response plan.

## [0.1.1] - 2026-07-01

### Fixed

- Subscription import failing on some networks. Cloudflare and some panels ask for
  a TLS renegotiation mid-handshake, which Go refuses by default and turned into a
  silent failure where `curl` and browsers succeeded; one client-initiated
  renegotiation is now allowed. Import failures are also classified into a plain,
  localized reason instead of a generic message, and the fetch cause (host only) is
  logged to `core.log`.

## [0.1.0] - 2026-07-01

Initial tagged release.

### Added

- **Go core (standard-library only, fully unit-tested).**
  - Subscription and share-link parsing for VLESS (incl. REALITY), Hysteria2,
    AmneziaWG, Shadowsocks, Trojan and VMess into one normalized node model.
  - Subscription bodies as base64 or plaintext link lists, with the
    `Subscription-Userinfo` header read for traffic used / total and expiry.
  - Named profiles (subscription or manual) with an atomic on-disk store and
    stable per-server IDs.
  - Routing modes — *smart* (RU and LAN direct, the rest tunnelled), *global*,
    *direct* — generating sing-box `route`/`dns` blocks; RU geodata is fetched
    from the public sing-geoip / sing-geosite rule-sets at runtime.
  - Per-app split tunnelling (*off* / *exclude* / *include*) matched by
    executable name and persisted across restarts.
  - A from-scratch sing-box config generator that emits plain JSON and does not
    depend on sing-box.
  - A pure protocol-fallback state machine (REALITY → Hysteria2 → AmneziaWG) that
    remembers the last good node per profile across launches, with an optional
    latency ordering that walks nodes fastest-first by measured ping while
    keeping the anti-DPI fallback.
  - An "auto-select fastest node" mode for `connect` (`auto` flag): without an
    explicit node the core pings every candidate and tries the lowest-RTT one
    first, falling through to the next on a block.
  - An honest leak check: public IP from redundant echo services plus a
    best-effort DNS probe, with a verdict that never reports a false pass.
  - The line-delimited JSON control protocol and the daemon that drives it, with
    a 6-hour background subscription auto-refresh.
  - Batch link import (`import_links`): several share links (a pasted block or a
    `.txt` list) collapse into one profile, skipping blank/comment/duplicate and
    unparseable lines and reporting how many were imported and skipped.
- **Windows adapter** that spawns and supervises the sing-box process and reads
  traffic counters from its clash API.
- **`tenebra-core` sidecar** speaking the control protocol over stdin/stdout.
- **Desktop app (Tauri 2 — Rust shell + React/TypeScript).**
  - Home, Profiles, Settings and Logs screens.
  - Import via subscription URL, a single link, several links at once (pasted
    block or `.txt` list, gathered into one profile), clipboard, or QR code
    (image file or pasted image), with an imported/skipped summary for batches.
  - Connect/disconnect with automatic or manual node selection, per-node ping,
    and "select fastest".
  - Live traffic graphs, routing and split-tunnel controls, and a leak-check
    panel.
  - System tray (quick connect/disconnect/show/quit), launch-at-login,
    single-instance, light/dark themes, and English / Russian UI.
  - In-app auto-updater (Settings → Updates) that verifies each update's minisign
    signature against the bundled public key before installing.
  - A mock backend (`TENEBRA_MOCK=1`) for UI-only development.
- **Docs**: architecture, control-protocol, and development guides;
  `CONTRIBUTING.md`, `SECURITY.md` and this changelog.
- **Tests**: Go unit tests across the core and the Windows adapter, a vitest suite
  for the front end (lib helpers, API client, state hook and screens), Rust unit
  tests for the backend, and a real-binary end-to-end test that round-trips the
  control protocol against the actual `tenebra-core`.
- **CI/Release**: Go build/vet/test (with the race detector) plus `staticcheck`,
  the front-end type-check and tests, the Rust tests, and a Windows desktop build.
  A tagged `release` workflow builds the NSIS installer, signs the updater
  artifacts, and publishes a GitHub release with the updater manifest.

### Known limitations

- The real tunnel (wintun + sing-box) needs an elevated, live run to validate and
  is **not** signed off yet.
- Only the Windows adapter exists; macOS, Linux, Android and iOS are planned.
- The kill-switch and LAN bypass are core routing options; the kill-switch is not
  yet exposed in the UI.
- The installer is not Authenticode code-signed, so Windows SmartScreen warns on
  first run. Updates delivered in-app are minisign-verified against the bundled
  key; only the initial download is unsigned.

[Unreleased]: https://github.com/Divaaaan/tenebra/compare/v0.3.7...HEAD
[0.3.7]: https://github.com/Divaaaan/tenebra/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/Divaaaan/tenebra/compare/v0.3.4...v0.3.6
[0.3.4]: https://github.com/Divaaaan/tenebra/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/Divaaaan/tenebra/compare/v0.3.0...v0.3.3
[0.3.0]: https://github.com/Divaaaan/tenebra/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Divaaaan/tenebra/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/Divaaaan/tenebra/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/Divaaaan/tenebra/releases/tag/v0.1.0
