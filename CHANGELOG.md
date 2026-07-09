# Changelog

All notable changes to Tenebra are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/).

> **Early days.** Tenebra is at 0.1.x: the desktop client (Windows) is the current
> focus and the real tunnel path is still being validated — see the
> [project status](README.md#project-status). Expect breaking changes between
> 0.x releases.

## [Unreleased]

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

### Security

- The sing-box clash API — the loopback control surface the client polls for
  traffic counters and connectivity probes — now requires a per-run secret.
  Without one, any other local process could read the active connection list or
  switch the selected outbound over `127.0.0.1`. The secret is drawn from a
  cryptographic RNG on each run and presented as a bearer token by the client's own
  polling, so the app keeps working while other local processes are turned away.

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

[Unreleased]: https://github.com/Divaaaan/tenebra/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/Divaaaan/tenebra/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/Divaaaan/tenebra/releases/tag/v0.1.0
