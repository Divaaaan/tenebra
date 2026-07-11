# Roadmap

Where Tenebra is and where it's going. This is a direction, not a schedule —
Tenebra is pre-1.0, priorities shift, and nothing here carries a date. If you
want to weigh in, open a thread in
[Discussions](https://github.com/Divaaaan/tenebra/discussions).

## Shipped

Working today on the Windows and macOS desktop clients:

- All sing-box protocols — VLESS/REALITY, Hysteria2, AmneziaWG, Shadowsocks,
  Trojan, VMess.
- Import from a subscription URL, share links, a `.txt` list, clipboard, a QR
  image, or a **Clash / Mihomo YAML** config.
- Smart / Global / Direct routing with LAN bypass, and per-app split tunnelling.
- Protocol fallback that remembers the last known-good node.
- DNS resolver choice plus an opt-in ad / tracker blocklist.
- Kill switch, honest leak check, system tray, notifications, `tenebra://` deep
  links, autostart, live traffic graphs, light / dark themes, English / Russian.
- Signed in-app auto-updater with **Stable** and **Beta** channels.

## In progress

- Validating the live tunnel path (wintun + sing-box, elevated) end to end —
  the gate before calling the client production-ready.
- Windows installer code signing, to soften the first-run SmartScreen prompt.

## Planned

- **Custom routing rules in the UI** — build and edit rules without hand-writing
  config.
- **Connection diagnostics** — UDP / STUN reachability and a speed test.
- **More platforms** — Linux next, then Android and iOS. The core is already
  platform-agnostic; the desktop UI and native tunnel adapters are the work.
- **Opt-in crash reporting** — privacy-first, off by default.

## Exploring

Bigger ideas, not committed:

- Multi-hop routing.
- Adaptive transport tuning per network.

---

Done items move up to **Shipped**; see the [changelog](CHANGELOG.md) for what
landed in each release and [project status](README.md#project-status) for the
honest state of each layer.
