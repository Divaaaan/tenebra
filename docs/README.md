# Tenebra documentation

Project overview and quick-start live in the top-level
[README](../README.md). This folder holds the deeper references.

- **[architecture.md](architecture.md)** — the layers (Go core, platform
  adapters, the desktop UI) and how they connect, plus the project's hard rules.
- **[control-protocol.md](control-protocol.md)** — the line-delimited JSON
  protocol the desktop UI uses to drive the core: every request, response and
  event, and the shared types.
- **[development.md](development.md)** — the full set-up, build, run and test
  walkthrough, the environment variables, coding conventions and troubleshooting.

Platform ports:

- **[porting/linux.md](porting/linux.md)** — the Linux desktop build: the root
  systemd service that owns the tunnel and its sandbox, the two install paths
  (Arch package and hand-install script), and what Linux does not support.
- **[porting/macos.md](porting/macos.md)** — the macOS desktop port: same sidecar
  model as Windows, with a privileged helper for the `utun` tunnel and a
  DMG/notarization distribution path.
- **[porting/android.md](porting/android.md)** — the Android client: the fused
  gomobile artifact, the `VpnService` that owns the tun, the CI that builds the
  APK, and release signing. The client itself lives in
  [`ui-android/`](../ui-android/README.md).
- **[porting/ios.md](porting/ios.md)** — the iOS port: the engine linked in-process
  via gomobile inside a Network Extension, the ~50 MB memory budget that dominates
  the design, and the provisioning and distribution constraints.

Also at the repository root:

- **[CONTRIBUTING.md](../CONTRIBUTING.md)** — how to get set up and propose a
  change.
- **[SECURITY.md](../SECURITY.md)** — how to report a vulnerability and the
  project's trust stance.
- **[CHANGELOG.md](../CHANGELOG.md)** — what's changed.

New to the codebase? Read [architecture.md](architecture.md) first for the mental
model, then [development.md](development.md) to get it building.
