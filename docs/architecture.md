# Architecture

Tenebra is a monorepo: one core, many thin platform shells. For the wire format
between the core and the UI see [control-protocol.md](control-protocol.md); to
build and run it see [development.md](development.md).

## Layers

### Core (Go) — `core/`

Everything that isn't tied to a specific OS lives here:

- **model** — the shared types. A `Node` is one proxy server, normalized across
  protocols; parsers produce them and the config generator consumes them.
- **subscription** — parse `vless://`, `hysteria2://`, `ss://`, `trojan://`,
  `vmess://` links and subscription URLs (base64 lists, plus the user-info
  header for traffic and expiry) into `model.Node`.
- **profile** — named profiles (a subscription or a manual set of nodes) and
  their on-disk storage.
- **routing** — the rule set: Russian domains and IPs go direct, private ranges
  go direct, everything else through the tunnel.
- **singbox** — turn selected nodes plus routing into a sing-box config.
- **fallback** — the state machine that, on block or timeout, walks
  REALITY → Hysteria2 → AmneziaWG and remembers the last good one.
- **control** — the JSON protocol the UI uses to drive the core.

The core's only third-party dependencies are `github.com/Microsoft/go-winio`
and `golang.org/x/sys` — Windows plumbing for the named-pipe transport and the
service entry point; everything else is standard library. It generates sing-box
configuration as plain JSON rather than linking sing-box as a library; sing-box
itself is the runtime. That keeps the core pure, fully unit-testable offline,
and free of the sing-box dependency tree.

### Adapters — `adapters/`

The system tunnel cannot be cross-platform; each OS exposes its own:

| OS      | Tunnel            |
|---------|-------------------|
| Windows | wintun            |
| macOS   | utun              |
| Linux   | utun / tun        |
| Android | `VpnService`      |
| iOS     | Network Extension |

### UI — `ui-desktop/`, `ui-android/`, `ui-ios/`

Native per platform. Desktop is Tauri 2 (Rust shell, React + TypeScript front
end). Android will be Jetpack Compose and iOS SwiftUI — all over the same core.

## How the pieces connect

### Desktop

The core and sing-box run as a **sidecar process** beside the Tauri app. The UI
sends commands (import subscription, connect, switch node) to the sidecar over
its **stdin** as line-delimited JSON and reads status and traffic events back
from its **stdout**; the sidecar's diagnostics go to **stderr**. The sidecar owns
the wintun tunnel and the sing-box lifecycle. The wire format is specified in
[control-protocol.md](control-protocol.md).

```
 Tauri UI  <-- line-delimited JSON over stdin/stdout -->  core + sing-box
 (React)                                                    |
                                                            +-- wintun tunnel
```

On Windows the same core can also run detached from the UI — as a Windows
service serving the identical protocol on the `\\.\pipe\tenebra` named pipe
(`tenebra-core --pipe` serves it from a console for development). That is the
path to the WireGuard/Tailscale privilege model, where the tunnel lives in a
SYSTEM service and the GUI runs unprivileged; the desktop shell does not use it
yet. Transports and the pipe's security model are described in
[control-protocol.md](control-protocol.md#transports).

### Mobile (later)

The core is compiled with gomobile to an `.aar` (Android) or `.xcframework`
(iOS) and embedded directly in the platform's VPN service, skipping the sidecar.

## Hard rules

- **GPLv3.** sing-box is GPLv3; shipping it makes Tenebra GPLv3 too.
- **No telemetry.** None. This is a VPN — trust is the product.
- **No infrastructure in the repo.** No hardcoded servers, subscription URLs,
  node IPs or keys. Servers come from user input only; test fixtures use
  obviously fake data.
