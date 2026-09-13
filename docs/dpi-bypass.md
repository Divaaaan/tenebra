# DPI bypass on Windows

[Overview](../README.md) · [Installation](installation.md)

Tenebra integrates [zapret](https://github.com/bol-van/zapret) on Windows. It changes packet presentation, including TLS handshake fragmentation and decoys, to try to avoid traffic-inspection rules. It runs alongside the tunnel; destinations routed directly can use it without going through the VPN exit.

A running bypass process does not prove that a blocked service is reachable. Results depend on the network, destination and selected strategy. macOS and Linux do not include this Windows packet filter; their traffic follows the configured tunnel and direct-routing rules.

## What is bundled

Tenebra uses the [Flowseal/zapret-discord-youtube](https://github.com/Flowseal/zapret-discord-youtube) bundle: `winws.exe`, the [WinDivert](https://github.com/basil00/WinDivert) driver, the Cygwin runtime, strategies and host lists.

One upstream archive is compiled into the Windows core. Its bytes are checked against the checksum pinned by the build. A missing bundle can therefore be installed without downloading it during a connection attempt. macOS and Linux binaries do not carry the archive.

Under the Windows service, the installed bundle lives in:

```text
%ProgramData%\Tenebra\data\zapret
```

## Installation and updates

- **Service startup:** installs the embedded copy if no usable bundle is present, then restores the previously requested bypass state.
- **Connect:** can install the embedded copy if needed, without fetching an archive from the network. Bypass startup has a bounded budget; a failure is logged and the connection can proceed without it.
- **Background updates:** when enabled, the service checks upstream after its startup delay (45 seconds) and then every twelve hours. Downloads must match a checksum trusted by this Tenebra build. A release newer than the trusted pins is left uninstalled, with a message to update Tenebra.

An existing usable bundle is not replaced by the embedded copy. A failed or rejected update keeps the existing bundle; connection attempts do not wait for a fresh download.

The update check contacts GitHub. These are ordinary network requests, so GitHub can observe request metadata such as the source IP. Tenebra does not attach your VPN subscription to the bundle request.

## Control downloads and bypass separately

**Settings → Censorship bypass → Update the bundle automatically** controls scheduled checks and downloads. Turn it off to stop those requests; press **Update** to request a manual update. A bundle can also be installed manually into the directory above.

The automatic-update preference does not disable the embedded copy: its installation uses bytes already shipped with Tenebra. Deleting the bundle directory is therefore not a lasting way to switch bypass off; startup or a later connect can install it again.

Use the **bypass switch** to turn the packet filter off. The requested setting is saved even when stopping the filter reports an error; check the displayed outcome and logs to confirm that the stop completed. When bypass is off or unavailable, traffic follows the applicable routing configuration, and directly routed destinations may remain blocked by the network.

All bundled components, license terms and copyright holders are listed in [Third-party notices](../THIRD-PARTY-NOTICES.md#2-components-downloaded-at-runtime).
