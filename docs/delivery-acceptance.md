# Delivery acceptance after the September audit fixes

The code changes are not an installation or live-tunnel acceptance result.
Run the following acceptance only in disposable machines or the hosted release
runners; never point tests at a developer's well-known production pipe.

## Windows

The NSIS installer accepts only service-not-found (1060), already-exists (1073),
already-stopped (1062) and already-running (1056) in their respective steps.
Other failures stop the installer with a nonzero exit and repair instructions.
It waits for STOPPED before replacing binaries and for a RUNNING, authenticated
service returning the exact installed app version after startup. The installed
GUI's `--service-check` mode exits before Tauri initialization and sends only
`status`, using a 30-second overall handshake budget. It does not initialize
profiles, a sidecar, the updater, autostart or a window.

The GUI matches the kernel pipe server PID to an SCM RUNNING/OWN_PROCESS service,
its LocalSystem account and strictly quoted registered image. It retains a
process handle and rechecks SCM state/PID before trusting the stream. This uses
read-only SCM queries and PROCESS_QUERY_LIMITED_INFORMATION, not TOKEN_QUERY
or administrator elevation. Interactive pipe rights are `0x120083` on both
sides; they exclude instance creation, owner changes and DACL writes.

Acceptance matrix: first install; same-version repair; update retaining machine
profiles; intentionally slow service startup; denied registration/configuration;
failed startup; stale daemon version; silent updater failures; uninstall and
reinstall. Test both a standard account and an unelevated administrator account.
A fake pipe on a unique test name must fail the production identity check before
any profile/import payload is sent. Verify blocked overlapped reads/writes
cancel and complete without outstanding OVERLAPPED buffers. Native test names:
`overlapped_backpressure_write_is_cancelled_and_reaped` and
`overlapped_idle_read_is_cancelled_and_reaped`.

GUI release builds never silently fall back to a sidecar/profile store. Debug
builds may opt in using `TENEBRA_PIPE=off`; custom debug pipe names remain a
development facility. Repair instructions preserve profiles and require a GUI
restart after repairing the service.

Explicit uninstall (`UpdateMode <> 1`) stops the service first, then queries only
the fixed T05 provider `fcb43b44-9358-4cd7-a998-9e7f822d5248` and sublayer
`fcb43b45-9358-4cd7-a998-9e7f822d5248`. Both exact WFP NOT_FOUND results permit
legacy removal without executing an old core. Query failures are not absence.
If either object exists, the installed `tenebra-core.exe
--release-host-protection` must confirm removal and a second probe must find both
objects absent before the service registration or binaries are removed. The
core alone checks ownership (`tenebra/persistent-host-guard/v1`) and deletes its
objects. A missing, unsupported or failing core while policy exists aborts
uninstall and retains the binary for repair. Cleanup has a 20-second child-process
deadline; the containing PowerShell invocation has an NSIS 35-second timeout.
The read-only WFP probe itself uses synchronous local Windows API calls.

The embedded cleanup wrapper executes only the exact installed core. It checks
all path ancestors for reparse points and administrator/SYSTEM/TrustedInstaller
ownership plus ACLs excluding unprivileged mutation, then holds the EXE open
against writes and replacement while running the fixed cleanup command. Unsafe
custom install locations require repair into an administrator-controlled path.
No installed or temporary PowerShell script is executed; the reviewed source is
embedded as constant chunks. Regenerate its include with
`node scripts/embed-uninstall-helper.mjs`; CI checks the source and embed agree.

Ordinary update and repair in update mode preserve persistent protection. The
first upgrade uses the previous uninstaller's compiled hooks and installs these
new hooks for later removals. Rolling back to a pre-T05 core requires explicitly
disabling/releasing host protection with a T05-capable core first and confirming
its provider/sublayer are absent; an older uninstaller cannot know how to remove
new policy. VM acceptance must cover legacy with no policy, current owned policy,
missing/old core with policy, query failure, cleanup failure/timeout, unsafe EXE
locations, ordinary update/repair retention, and rollback preparation.

## Release channels

All platform jobs upload into one draft. Only `publish` may open the release,
after Windows, macOS, Linux and Arch complete. The gate requires every expected
uploaded, nonempty asset; all nine updater platform entries; the exact tag
version; same-release URLs; matching attached signatures; a public release and
public asset downloads. No new release is published from this audit workspace.

`beta.json` moves to the `update-channels` branch. GitHub Contents API updates it
with the previous blob SHA, giving one atomic Git commit rather than deleting
and replacing a public release asset. A concurrent publisher retries at most
three times and compares SemVer on each read: an older release cannot replace a
newer pointer. Network/API errors preserve the previous committed manifest.

Bootstrap is performed by the final publish job only, after the release is
public and complete. It creates `update-channels` from the release commit if
needed. Repository branch rules must permit the workflow's `contents: write`
token to create/update that branch. A denied bootstrap leaves the new release
public but the old beta pointer untouched and fails the job; repair permissions
and rerun that final job. The raw-content endpoint can cache the previous valid
manifest briefly; this affects freshness, not artifact completeness.

New clients query the atomic beta endpoint, with stable as a network-failure
fallback. Older clients continue querying the old latest-release `beta.json`:
that legacy file is populated only inside new stable drafts, and is never
clobbered on a public stable release. Thus old clients receive future stable
versions, but new prereleases require upgrading to a client with the new endpoint.
No current public refs or manifests have been changed by the audit fixes.

## Toolchain and platform boundaries

`.go-version` pins Go 1.26.8 across CI, desktop release and Android jobs; Arch
selects the same exact toolchain instead of its rolling distribution compiler.
Go's [release history](https://go.dev/doc/devel/release#go1.26.8) records this
supported 1.26 patch as released on 2026-09-01. Other jobs prohibit automatic
Go toolchain switching. Desktop release builds inspect binary build metadata
without running the binary: exact toolchain, OS/architecture, source revision
and clean source tree; retained reports include binary SHA-256. macOS validates
both slices before lipo. These reports do not replace a vulnerability scan.

- Windows Authenticode needs a signing certificate and trusted signing setup.
  Updater Minisign verification does not provide Authenticode trust.
- macOS remains an unsigned/unnotarized app with a manually installed daemon;
  app updates do not update that daemon. A Developer ID, notarization credentials,
  packaged privileged-helper lifecycle and actual macOS tunnel/update acceptance
  remain external/product prerequisites.
- Linux AppImage/deb still require the documented daemon setup; Arch installs
  the packaged systemd unit. Test package upgrade, service restart and retained
  profiles on supported distributions.
- Android requires the existing signing secrets (`ANDROID_KEYSTORE_B64` and the
  configured alias/password secrets), a signed artifact, and device acceptance
  for install/update/revoke/reconnect/Doze. No signing material is generated or
  imported by these fixes; there is no new signed APK or parity claim.
- iOS remains a scaffold: Apple team/entitlements, Network Extension provisioning,
  framework/Xcode build and real-device validation are prerequisites. These
  delivery changes do not turn the scaffold into a supported product.

API references: [Windows pipe access](https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipe-security-and-access-rights),
[CancelIoEx completion lifetime](https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-cancelioex),
[token object access checks](https://learn.microsoft.com/en-us/windows/win32/secauthz/access-rights-for-access-token-objects),
[GitHub file updates](https://docs.github.com/en/rest/repos/contents#create-or-update-file-contents).
