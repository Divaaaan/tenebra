# Changelog

All notable changes to Tenebra are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/) once it reaches a tagged
release.

> **Pre-release.** Tenebra has not had a versioned release yet. Everything below
> lives under *Unreleased* and may change before the first tag. The desktop
> client (Windows) is the current focus and the real tunnel path is still being
> validated — see the [project status](README.md#project-status).

## [Unreleased]

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
  - A mock backend (`TENEBRA_MOCK=1`) for UI-only development.
- **Docs**: architecture, control-protocol, and development guides;
  `CONTRIBUTING.md`, `SECURITY.md` and this changelog.
- **Tests**: Go unit tests across the core and the Windows adapter, a vitest suite
  for the front end (lib helpers, API client, state hook and screens), Rust unit
  tests for the backend, and a real-binary end-to-end test that round-trips the
  control protocol against the actual `tenebra-core`.
- **CI**: Go build/vet/test (with the race detector) plus `staticcheck`, the
  front-end type-check and tests, the Rust tests, and a Windows desktop build
  producing an unsigned NSIS installer.

### Known limitations

- The real tunnel (wintun + sing-box) needs an elevated, live run to validate and
  is **not** signed off yet.
- Only the Windows adapter exists; macOS, Linux, Android and iOS are planned.
- The kill-switch and LAN bypass are core routing options; the kill-switch is not
  yet exposed in the UI.
- Installers are unsigned and there is no release pipeline.

[Unreleased]: https://github.com/Divaaaan/tenebra/commits/main
