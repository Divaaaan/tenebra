# Tenebra

A cross-platform VPN client built on [sing-box](https://github.com/SagerNet/sing-box).
Desktop first (Windows), with a shared core meant to extend to macOS, Linux,
Android and iOS.

> Early development. The desktop client is the current focus; expect things to
> move around.

## Why another client

Most clients either lock you into a single protocol or are vague about what they
do with your traffic. Tenebra:

- speaks the protocols sing-box supports — VLESS/REALITY, Hysteria2, AmneziaWG,
  Shadowsocks, Trojan, VMess;
- routes Russian destinations directly and sends everything else through the
  tunnel, so latency-sensitive local traffic stays local;
- falls back between protocols when one gets throttled or blocked, and remembers
  what worked;
- ships no telemetry, no accounts and no bundled servers — you import your own
  subscription.

## Architecture

- **core/** — Go. Subscription parsing, profile storage, routing, the protocol
  fallback state machine and sing-box config generation. Shared by every
  platform.
- **adapters/** — the system tunnel per OS: wintun on Windows, utun on
  macOS/Linux, `VpnService` on Android, Network Extension on iOS.
- **ui-desktop/** — Tauri 2: a Rust shell with a React + TypeScript front end.
- **ui-android/**, **ui-ios/** — native UIs to come, on top of the same core.

On desktop the core runs sing-box as a sidecar process and talks to the UI over
a local JSON protocol. See [docs/architecture.md](docs/architecture.md).

## Building

Requirements: Go 1.24+, Node 20+, and the Rust toolchain (for the desktop UI).

Core tests:

```
go test ./...
```

Desktop app (Windows):

```
# fetch the sing-box binary and wintun.dll into src-tauri/resources
powershell -File scripts/fetch-resources.ps1

# build the core sidecar where Tauri bundles it
go build -o ui-desktop/src-tauri/binaries/tenebra-core-x86_64-pc-windows-msvc.exe ./cmd/tenebra-core

# build the bundle
cd ui-desktop
npm install
npm run tauri build
```

## License

GPLv3 — see [LICENSE](LICENSE). sing-box is GPLv3, so Tenebra is too.
