# Changelog

All notable changes to Tenebra are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/).

> **Early days.** Tenebra is at 0.2.x: the desktop client (Windows) is the current
> focus and the real tunnel path is still being validated — see the
> [project status](README.md#project-status). Expect breaking changes between
> 0.x releases.

## [Unreleased]

### Added

- **Windows service mode for the core.** Started by the service control manager,
  `tenebra-core` serves the control protocol on the `\\.\pipe\tenebra` named pipe
  (DACL: SYSTEM, Administrators, the interactive user — the Tailscale LocalAPI
  trust model) instead of stdin/stdout, logs to `%ProgramData%\Tenebra\service.log`,
  and tears the tunnel down on service stop. One client session is active at a
  time: a new connection displaces the old, and a client disconnecting leaves the
  tunnel up. `tenebra-core --pipe` serves the same transport from a console for
  development. The desktop app still spawns the stdio sidecar; moving it onto the
  service (installer, unprivileged GUI) is a separate step.

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

[Unreleased]: https://github.com/Divaaaan/tenebra/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/Divaaaan/tenebra/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/Divaaaan/tenebra/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/Divaaaan/tenebra/releases/tag/v0.1.0
