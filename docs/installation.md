# Installing Tenebra

[Overview](../README.md) · [Русское описание](../README.ru.md) · [All release packages](https://github.com/Divaaaan/tenebra/releases/latest)

Tenebra needs your own subscription or compatible server link. Desktop packages are available for Windows, macOS and Linux; Android and iOS are outside the 0.6.0 release.

## Windows

1. Download [Tenebra 0.6.0 for Windows x64](https://github.com/Divaaaan/tenebra/releases/download/v0.6.0/Tenebra_0.6.0_x64-setup.exe), or choose a version from [Releases](https://github.com/Divaaaan/tenebra/releases).
2. Run the installer and approve the administrator prompt to install the background service.
3. Open Tenebra from your ordinary Windows account, import your connection details and select a server.

The service owns the tunnel; the desktop interface communicates with it. The installer and in-app updater coordinate application and service updates. An offered update may restart the app and interrupt its connection.

The Windows installer is **not Authenticode-signed**, so SmartScreen may warn. Download from the project's release page. The in-app updater verifies artifacts with the project's minisign key; that signature is separate from Windows code signing.

The 0.6.0 Windows acceptance scope includes installation, both UI modes under an ordinary user at 100% and 150% scaling, IPv4, system-proxy handling and selected crash/recovery cases. The complete IPv6, BFE and reboot matrix is still open. See [host-protection acceptance](host-protection-acceptance.md) and [delivery acceptance](delivery-acceptance.md) for the acceptance requirements.

## macOS

**For advanced users.** The universal DMG is unsigned and unnotarized, and the tunnel requires a manually installed root LaunchDaemon. Opening the app alone does not set up that daemon. Native live-tunnel acceptance for 0.6.0 is not complete.

1. Download the DMG from [the latest release](https://github.com/Divaaaan/tenebra/releases/latest) and put the app in `/Applications`.
2. If Gatekeeper blocks launch, use **System Settings → Privacy & Security → Open Anyway** for the downloaded app.
3. From a checkout of the matching release source, install the daemon:

   ```sh
   sudo bash scripts/macos/install-daemon.sh --from-app /Applications/Tenebra.app --allow-unsigned
   ```

The in-app updater replaces the app, **not the root daemon**. After an update, use a matching source checkout and re-run the command above. The app warns when the daemon version falls behind. A bundled, signed and notarized helper installation path is planned; see the [macOS port notes](porting/macos.md).

## Linux

The GUI needs a privileged **systemd service** to create `/dev/net/tun` and install routes. It communicates with that service through `/run/tenebra.sock`; the GUI alone cannot connect. Native live-tunnel acceptance for 0.6.0 is not complete.

### Arch Linux

From a checkout of the release source:

```sh
cd packaging/arch
makepkg -si
sudo systemctl enable --now tenebra.service
```

The [PKGBUILD](../packaging/arch/PKGBUILD) builds and installs the core, desktop app and service. Updates are managed through `pacman`; the in-app updater cannot replace package-managed files. Prebuilt release assets are listed on [Releases](https://github.com/Divaaaan/tenebra/releases/latest).

### Other distributions

From a checkout of the release source, fetch the resources and install the service:

```sh
bash scripts/fetch-resources.sh
sudo bash scripts/linux/install-daemon.sh --dev
```

Install the GUI separately using the release `.deb` or AppImage. Re-run the service installer when upgrading the daemon; it supports rollback if an upgrade fails. To remove the service, use [`scripts/linux/uninstall-daemon.sh`](../scripts/linux/uninstall-daemon.sh).

**Linux limitations:** system-proxy mode is unsupported; use TUN mode, the default. Bundled sing-box binaries use glibc, so musl distributions need a compatible engine build. The in-app updater can replace an AppImage but cannot update a package-managed installation or its root service. See the [Linux port notes](porting/linux.md) for setup and service sandbox details.

## Connection and diagnostics

Import a subscription URL, supported share link, text file or QR image. Provider formats and server settings must be compatible with the supported protocols; a subscription from an arbitrary provider is not guaranteed to work.

If a connection fails, inspect the reported service, engine or proxy error and the app logs. Public-IP and DNS probes report limited observations, not a universal leak-protection verdict. Before posting a report, remove subscription URLs, credentials and other private data.

[Get help](https://github.com/Divaaaan/tenebra/discussions) · [Report a bug](https://github.com/Divaaaan/tenebra/issues/new/choose) · [Build from source](development.md)
