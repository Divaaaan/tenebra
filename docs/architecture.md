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
and `golang.org/x/sys` — the named-pipe transport and service entry point on
Windows, and the OS calls the detached daemon needs on the unix side (peer
credentials on the control socket, per-socket interface binding for the ping
probe); everything else is standard library. It generates sing-box
configuration as plain JSON rather than linking sing-box as a library; sing-box
itself is the runtime. That keeps the core pure, fully unit-testable offline,
and free of the sing-box dependency tree.

### Adapters — `adapters/`

The system tunnel cannot be cross-platform; each OS exposes its own:

| OS      | Tunnel            |
|---------|-------------------|
| Windows | wintun            |
| macOS   | utun              |
| Linux   | tun (`/dev/net/tun`) |
| Android | `VpnService`      |
| iOS     | Network Extension |

### UI — `ui-desktop/`, `ui-ios/`

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

The same core can also run detached from the UI, serving the identical protocol
on a well-known endpoint: the `\\.\pipe\tenebra` named pipe from a Windows
service, or a unix domain socket from a root LaunchDaemon on macOS and a systemd
service on Linux (`tenebra-core --pipe` / `--socket` serve them from a console
for development). That is the WireGuard/Tailscale privilege model, where the
tunnel lives in a privileged service and the GUI runs unprivileged — on Linux it
is the only arrangement, since opening `/dev/net/tun` and claiming the default
route need `CAP_NET_ADMIN`. Transports, peer authentication and the endpoints'
security models are described in
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
