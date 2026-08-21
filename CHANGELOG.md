# Changelog

All notable changes to Tenebra are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/).

> **Early days.** Tenebra is at 0.x: the desktop clients (Windows and macOS) are
> the current focus — see the
> [project status](README.md#project-status). Expect breaking changes between
> 0.x releases.

## [Unreleased]

## [0.5.0] - 2026-08-21

### Added

- **The connect button measures before it picks an exit.** A node whose proxy
  handshake has stopped answering still completes a TCP dial instantly, so it
  reads as the *fastest* node and wins a latency-ranked pick while every request
  through it hangs — which is how a connect that reported success left a machine
  with no working internet. `check_nodes` now exposes every node as its own
  loopback proxy in one sing-box process (no tun, no `auto_route`, and a blocking
  final rule so a stray request cannot egress unproxied and certify a dead node),
  judges each by whether traffic survives it across several unalike destinations,
  and reports what broke: address unreachable, proxy handshake never completed,
  or tunnel up but carrying nothing. With nothing usable it says so instead of
  offering the least-bad node.
- **The app says whether video, voice and games actually work.** "Connected" was
  never an answer: exit IP, DNS verdict, throughput and node latency together
  still leave someone watching YouTube spin unable to tell whether the tunnel,
  the bypass, the routing or their own network is at fault. `check_services`
  probes the three and the screen shows what each cost — a latency, not a tick,
  because "voice works" and "voice works at 240ms" are different answers and
  moving between them is the entire reason the routing is split.
- **The DPI-bypass bundle installs itself** on the first connect when there is
  none, so the setup screen asks only for the subscription link. Dragging in a
  specific bundle still works and still wins; it is simply no longer a step
  standing between the user and the button.
- **`set_presets`** toggles the routing presets (games direct, real-time UDP
  direct, censored services unblocked) individually. Each takes an optional
  value, so naming one leaves the others alone.

### Fixed

- **Overriding the "another VPN owns the default route" refusal is reachable
  again.** The core declines to raise a tunnel while another VPN holds the
  default route, and that refusal was always meant to be overridable — but the
  question was asked through `window.confirm`, which Tauri routes to the dialog
  plugin, and the capability set deliberately grants the file dialog and nothing
  else. So the prompt never appeared: the ACL denial fell into the generic catch
  and reached the user as "could not connect", with no way through. The question
  is now a component in the app. Every ambiguous gesture — Escape, a click on the
  backdrop, the initial focus — resolves to cancel, because taking the override
  by accident can leave a machine with no network at all. Granting
  `dialog:allow-confirm` would have been worse than the bug: the brokered confirm
  is asynchronous, so `if (!window.confirm(...))` reads a pending promise as
  truthy and would have walked past the guard without ever asking.
- **The bypass no longer steals Discord's voice out of the tunnel.** The bundle
  ships a block that punches voice and STUN through the DPI on the direct path —
  right for a machine with no tunnel, wrong with one: WinDivert takes those
  packets before the tunnel's router sees them, so voice leaves from the ISP
  address no matter what the routing says, and on a network where the voice
  servers are unreachable that way the client sits on "no route" forever. The
  strategy is rewritten without that one block while a tunnel is connected, which
  is what lets voice and YouTube work at the same time rather than one or the
  other.
- **Routing choices survive a restart and apply while connected.** The routing
  mode was held in memory only: a user who chose global got smart back at the
  next launch, with the UI reporting the mode the daemon had reset to. The three
  presets were set in the constructor and had no command at all. `set_routing`
  and `set_split` now hot-swap a live tunnel like the kill switch already did,
  and skip the swap when nothing actually changed.
- **The bundle's own update checker is switched off on import too**, not only on
  the update path. A bundle dragged in by hand kept it armed, so every strategy
  start reached GitHub first — on the network this app exists for, that is the
  bypass waiting on a request the censor it has not raised yet is stalling.
- **A daemon no longer reaches the internet unless it is given the way.** The
  bundle updater was wired in the constructor, which put unit tests on the
  release feed the moment a connect could install a missing bundle. `main`
  installs it now, the same way the node-check probe runner is wired.

## [0.4.6] - 2026-07-27

### Added

- **Linux is a supported desktop platform.** Until now Linux only compiled: the
  core was built and tested in CI, but it could not host a daemon there — the
  socket transport was rejected off macOS — and no installable artifact was
  produced. It now runs the same way Windows and macOS do: one privileged core
  under systemd owns the tunnel and serves the control protocol on
  `/run/tenebra.sock`, and the unprivileged desktop app attaches to it. The
  connecting user is authenticated from the kernel's own socket credentials,
  and the profile store under `/var/lib/tenebra/data` is created root-only and
  re-checked on every start.
- **An Arch package.** `packaging/arch/PKGBUILD` builds the core, the desktop
  app and the systemd unit from source; the release workflow builds it in an
  Arch container, verifies it installs, and attaches the resulting
  `.pkg.tar.zst` to the release. sing-box is bundled with a pinned digest
  rather than declared as a dependency: there is no such package in Arch's own
  repositories, only in the AUR, and it goes into a private directory so an
  existing AUR install is untouched.
- **Debian and AppImage bundles** for everything else, plus install and
  uninstall scripts for setting the service up by hand. Only the Arch package
  installs the systemd unit for you; the Debian bundle cannot run a
  post-install script, so there the service is one manual step.

### Fixed

- **Linux: the crash and core logs no longer live in `/tmp`.** They now follow
  the XDG data directory. `/tmp` is shared and world-writable, so a symlink
  planted at the old path could have redirected an append-only writer.
- **Linux: `tenebra://` links now work in a release build.** Deep-link
  registration was compiled in only for development builds, and an AppImage has
  no installer to claim the scheme on its behalf, so a released build never
  registered as a handler at all.
- **Linux: the app no longer offers an update it cannot install.** The bundled
  updater can only replace an AppImage, so an install that came from a package
  manager now says where its updates come from instead of showing a button that
  would fail.

## [0.4.5] - 2026-07-25

### Fixed

- **One stuck app no longer takes the whole service off the air.** The core
  served its control channel from a single accept loop that, on a new
  connection, waited for the previous session's goroutine to finish — while
  writes to a client had no deadline at all, and several settings commands
  block until a live re-apply finishes. So a client that stopped reading, or a
  command still walking the fallback chain, could leave the service accepting
  nobody at all: still "Running", still holding the tunnel, but deaf, so every
  app window came up drawn and dead and restarting the app changed nothing.
  Outgoing frames are now queued and written by a dedicated writer with a
  deadline, taking over a session no longer waits on the old one, and a client
  that has genuinely stopped reading is dropped so it can reconnect and
  re-sync. Under pressure the core sheds events, never answers.
- **The window is no longer drawn but dead when the core is slow to answer.**
  The app asked the core for its state exactly once at launch, with nothing
  catching a refusal. If that one request missed — the Windows service still
  starting after an update or at logon, a session displaced on the control
  pipe — the window came up looking perfectly normal and did nothing at all
  from then on: no toggle moved, and Connect sat there enabled and silent,
  because the profile list was empty. The opening request now retries on its
  own until the core answers, the event stream is wired up regardless, and
  while there is no core the app says so instead of pretending.
- **Windows: a service that is still starting no longer costs you the
  service.** The installer starts the service moments before it launches the
  app, and at logon the two race as well. Losing that race made the app fall
  back to running its own copy of the core — unelevated, and reading a
  different profile store, so a subscription added through the service was
  invisible and connecting could not work. The first attempt now waits out a
  service that is coming up, and if the app does end up on its own core it
  says so plainly in the log rather than in passing.
- **A setting that will not apply now tells you why.** Every switch on the
  settings screen sent its command and discarded any refusal, and since the
  switch draws itself from the core's answer, a refused command looked exactly
  like a dead button. Refusals are now reported, and a core older than the app
  — the usual case on macOS, where the privileged daemon is updated by hand
  and stays behind after an app update — is named as such instead of surfacing
  as silence.
- **Connect no longer looks available with no subscription.** With no profile
  the button did nothing when pressed; it is now disabled, matching the simple
  view.
- **Settings: clicks near the top of the panel land where you aim them.** The
  bar holding the close button spanned the whole column and swallowed clicks
  across its full width, though only the ✕ at the right edge is visible, and
  jumping to a section from the rail parked that section's first control right
  underneath it. The bar now only catches clicks where it actually draws, and
  sections keep clear of it. The rail scrolls instead of silently cutting off
  its last entries in a short window.

### Added

- **Copy diagnostics from the logs screen.** A single button gathers the app
  and daemon versions, the platform, the last leak check of the session and the
  whole log buffer into one text block and puts it on the clipboard, ready to
  paste into a bug report. It stays true to the privacy stance: nothing is sent
  anywhere — you copy it and share it yourself — and subscription tokens and node
  credentials are masked out before it reaches the clipboard.

## [0.4.4] - 2026-07-18

### Added

- **Updates never interrupt a live tunnel.** Applying an update relaunches the
  app — and on Windows the installer stops the background service to swap it —
  which could drop the VPN mid-session, worst of all when auto-install fired
  moments after a connect. Auto-install now holds off until the tunnel is down;
  while it is up the banner says the update is ready and will apply once you
  disconnect. Installing by hand while connected asks first, in plain terms,
  before it cuts the connection.
- **The tray and system notifications now follow the app's language.** The tray
  menu, its tooltip and the desktop notifications were always English even with
  the interface set to Russian, because they live in the native shell rather
  than the webview. The shell now tracks the language the app is set to and
  localizes them to match, switching on the fly when the language changes.
- **Importing a subscription over plain http:// warns you.** The daemon already
  noted an unencrypted fetch in its log; the import dialog now says it inline —
  an http:// subscription (token and all) can be read in transit. It is a
  heads-up, not a wall: the import still goes through if you choose.
- **The app warns when the background service is older than it.** The daemon
  now reports its build version in every state snapshot, and the app shows a
  banner when the two builds differ — with the reinstall command ready to copy
  on macOS. Until now an out-of-date daemon made the toggles of newer settings
  (IPv4-only DNS, DPI fragmentation, auto-failover, multihop and friends) look
  simply dead: the click went through, the old daemon ignored or rejected the
  unknown command, and nothing on screen said why. This is the macOS reality
  today — the in-app updater refreshes only the .app, never the hand-installed
  LaunchDaemon — so the skew is now said out loud instead of silently eaten.

## [0.4.3] - 2026-07-13

### Fixed

- **Split tunnelling can actually be enabled now.** Picking "exclude" or
  "include" used to snap straight back to "off" whenever the app list was still
  empty — and the app editor only appears for an active mode, so the feature was
  a dead end. The chosen mode now sticks (an empty list is simply a no-op until
  the first app is added), and the app list survives toggling the mode off and
  back on.
- **The last Settings rail item highlights on the first click.** Clicking
  "Updates" (or any final short section) scrolled the pane to its bottom but the
  highlight snapped back to the previous section, so the click looked ignored
  until a second press. The rail now locks onto the last section when the pane
  is scrolled to its bottom.

## [0.4.2] - 2026-07-13

### Added

- **Failed connections explain themselves.** When every protocol has been tried
  and blocked, the log now includes the tail of sing-box's own output, so a
  config sing-box rejected or a binary that would not start is diagnosable from
  the UI instead of showing only "all protocols failed".

### Changed

- **AmneziaWG's obfuscation limit is now documented.** An AmneziaWG node imports
  and connects, but the bundled stock sing-box applies none of the AWG
  obfuscation parameters, so the tunnel runs as plain WireGuard. The README now
  states this plainly; full AmneziaWG obfuscation remains on the roadmap.

## [0.4.1] - 2026-07-12

### Added

- **System-proxy mode.** A connection mode that needs no tun driver, service, or
  elevation — for locked-down or corporate machines. Instead of a tun device,
  sing-box exposes a loopback mixed inbound (HTTP + SOCKS) and the client points
  the OS system proxy at it. Switch it in Settings → Connection mode. The OS proxy
  is cleared on every disconnect, on a tunnel-process death, and on shutdown, and a
  proxy left behind by a hard kill is reconciled at the next launch, so the machine
  is never stranded pointing at a dead local proxy.
- **Multihop (two-hop chains).** Route traffic through two of your own nodes in
  sequence (entry → exit) using sing-box's native outbound `detour`, so the exit
  never sees your entry address. Pick an entry and exit node in
  Settings → Multihop; an unresolvable pair degrades to a single hop rather than a
  broken configuration.

### Fixed

- **The speed test tolerates a blocked download endpoint.** It now tries several
  neutral endpoints in order and the first that streams data wins, so a CDN that
  challenges or refuses a datacenter IP (a VPN exit is one) no longer fails the
  whole test while the tunnel is healthy.

## [0.4.0] - 2026-07-12

### Added

- **TLS handshake fragmentation.** A *Bypass* toggle in Settings fragments the TLS
  ClientHello (sing-box's native `fragment`), splitting the handshake across
  packets so a filter keying on the SNI within a single segment cannot match it.
  The adaptive transport cascade also escalates to a fragmenting strategy on a
  censored handshake before abandoning a node. Applies to any TLS-bearing node; a
  no-op on QUIC transports.
- **Health auto-failover.** While connected, a watchdog probes the active node
  through the tunnel; after repeated failures it automatically reconnects through
  a different healthy node from the subscription, excluding the degraded one. On
  by default, with a *Reliability* toggle. The connection panel shows a distinct
  reconnecting state during recovery.
- **Network diagnostics.** A *Diagnostics* section runs an on-demand UDP/STUN
  check (UDP reachability, NAT type, external address) and an in-tunnel speed test
  (throughput over the active connection). The speed test is available only while
  connected.
- **Simple mode.** An optional one-button interface for non-technical users — a
  single connect control, the current status, and a minimal server picker, with
  everything advanced hidden. Toggled in Settings; an *Advanced view* link inside
  it returns to the full interface.
- **Tray quick-connect.** The system-tray menu can connect to any node in the
  current profile directly, without opening the window, alongside a quick
  connect/disconnect entry.
- **Adaptive transport escalation.** When a node's entry accepts a TCP connection
  but its handshake then makes no progress and draws no reset — the signature of
  destination-level interference, as distinct from a dead server — the connect
  walk now re-tries the *same* node under a different transport strategy (a
  reshaped TLS handshake: the uTLS fingerprint, then the SNI) before moving on to
  the next node. A failure classifier separates this case from a dead entry (a
  reset or an unreachable/unresolvable address, which advances straight to the
  next node) and from an ambiguous one, so a strategy escalation is spent only on
  real evidence of interference — not on ordinary packet loss or a downed host.
  The fallback `attempts` snapshot gains two optional fields: `strategy` (the
  non-default variation a candidate is being tried under or came up on) and
  `reason` (`"censored"` when a node was abandoned after its handshake looked
  interfered with), so the walk narrates the adaptation for the UI.

## [0.3.7] - 2026-07-11

### Security

- **The control channel now authenticates the connecting peer.** The pipe
  (Windows) and socket (macOS) that drive the privileged tunnel were reachable by
  any local user; earlier releases stopped credential *disclosure* but not
  *control*. The daemon now checks the peer's uid (macOS `LOCAL_PEERCRED`) or
  token SID (Windows `GetNamedPipeClientProcessId`) at accept time and admits only
  its own account and the interactive console (logged-in) user — narrowing the
  documented Tailscale-style "any local user" trust to "the logged-in user". An
  unauthorized peer is refused without disturbing the live session; if the console
  user cannot be determined the check fails open with a warning, so an
  unprivileged GUI can never be locked out of attaching.

### Security

- **Credential redaction is now complete.** Wave 1 redacted `list_profiles` and
  `status`; the `import`, `import_links` and `refresh` responses still returned
  the full profile (subscription token + node secrets) over the local control
  channel. All of them now return the same redacted view; the profiles event was
  already bodyless.
- **DNS no longer leaks under split tunnelling.** With the base mode `direct` and
  a node included into the tunnel, the app's traffic went through the proxy while
  its DNS query fell through to the direct resolver and resolved outside the
  tunnel — leaking every visited domain to the local ISP from the real IP.
  Included apps' DNS now follows the tunnel in every mode.
- **The Windows service directory and log are hardened against squatting.** The
  `%ProgramData%\Tenebra` parent is clamped with a protected SYSTEM/Administrators
  DACL before the log is opened, a pre-planted symlink/junction at the log path is
  rejected, and the data-dir owner and DACL are verified after clamping (fail
  closed), mirroring the macOS check.
- **The macOS privileged data-dir clamp is symlink-safe.** It now opens the
  directory with `O_NOFOLLOW` and operates on the descriptor, so a symlink planted
  at the path can neither redirect the clamp nor survive its verification. The
  hand-install script also verifies the binaries it installs.
- **The desktop shell is tightened.** The sidecar spawn capability is pinned to
  zero arguments and the rest of the shell surface is explicitly denied, so an
  injected script cannot repoint the core or sing-box; a `tenebra://connect` deep
  link now requires an explicit confirmation instead of auto-connecting; and an
  unresolved core/sing-box path fails closed instead of resolving a bare name from
  the working directory.
- **Bundled binaries are pinned by hash.** The fetch scripts now verify a SHA-256
  digest for the sing-box binary and the rule-sets before they are bundled into a
  signed release, so a tampered upstream artifact fails the build.
- **The node-latency probe fan-out is bounded** (a worker pool caps concurrent
  dials), and the connectivity probe now uses HTTPS.
- **IPv6 no longer bypasses the tunnel.** The tun interface carried only an IPv4
  address, so on a dual-stack host `auto_route` never claimed the IPv6 default
  route and native IPv6 traffic egressed around the VPN. The tun now also carries
  a private IPv6 (ULA) address, so IPv6 is routed into the tunnel and follows the
  same rules as IPv4; on a single-stack host the extra address is inert.
- **Stored credentials are no longer served over the local control channel.**
  `list_profiles` returned the full profile — the subscription URL with its
  embedded token plus every node's UUID/password/keys — to any local client of
  the pipe/socket, defeating the root-only permissions on the data directory. The
  response is now a redacted view carrying only what the UI renders (id, name,
  host, port, protocol); the connect path still uses the full stored profile
  internally.
- **Subscription fetch is hardened against SSRF and cleartext leaks.** Non-HTTP(S)
  schemes are rejected, plain `http://` is warned about (host only, never the
  token), and the fetch — across every redirect hop and DoH-resolved address —
  refuses loopback, link-local/metadata (169.254.169.254), and private ranges.
  An opt-out env var covers operators who genuinely self-host on a private range.
- **A malicious Clash subscription can no longer crash the daemon.** The
  hand-written YAML decoder recursed without a depth limit, so a crafted body
  could overflow the stack. Recursion is now capped and the top-level parse is
  wrapped so a decoder fault degrades to a skipped import instead of taking down
  the process.
- **Insecure nodes are surfaced in the UI.** A node whose subscription sets
  `skip-cert-verify` (disabling TLS certificate verification) now shows a warning
  badge and a per-profile summary, so the trade-off is visible rather than silent.

### Fixed

- **Node latency (ping) is measured outside the tunnel.** While connected, the
  per-node ping dialed through the tunnel and reported ~1-2 ms for every node; it
  now binds to the physical default interface so the readout reflects real
  round-trip time to each server.

## [0.3.4] - 2026-07-11

### Fixed

- **macOS release build.** The 0.3.3 release could not bundle the macOS app —
  the universal build expects the core sidecar as two per-arch binaries plus a
  lipo'd universal, and the release job shipped only the universal one. Both
  platforms now build and publish together, so the macOS app and everything it
  brought in 0.3.3 reaches users with this release. (The 0.3.3 Windows build was
  unaffected and shipped normally.)

## [0.3.3] - 2026-07-11

### Added

- **macOS desktop app.** The desktop client now builds and ships for macOS
  (universal: Apple Silicon and Intel). The tunnel follows the platform's
  privilege model instead of an elevated app: `tenebra-core --socket` serves the
  control protocol on a unix domain socket, a root LaunchDaemon owns the tunnel
  (install/uninstall scripts under `scripts/macos/`, hand-installed once with
  sudo — see `docs/porting/macos.md`), and the unprivileged app attaches to it,
  reconnecting through restarts with the same grace the Windows service client
  uses. Without the daemon the app still runs its own unprivileged sidecar:
  everything except opening the tun device works. Releases now carry the macOS
  app and in-app updates alongside the Windows installer; the build is
  unsigned, so the first launch needs System Settings -> Privacy & Security ->
  Open Anyway.
- **Clash/Mihomo YAML subscriptions.** A subscription whose body is a
  Clash/Mihomo config — servers under a top-level `proxies:` key — is now
  detected and imported alongside the existing base64 and plaintext link lists.
  Each proxy is mapped onto the same node model as the share-link parsers:
  Shadowsocks, VMess, VLESS (incl. REALITY), Trojan, Hysteria2 and WireGuard are
  supported, with their transport, TLS and obfuscation options; a proxy of an
  unsupported type is skipped rather than failing the whole import. The YAML is
  read by a small purpose-built decoder, so the core keeps its no-third-party-
  dependency footprint.
- **DNS ad and tracker blocking (opt-in).** A new Settings toggle, off by
  default, refuses DNS lookups for a bundled list of ad and tracker domains
  (answered `REFUSED`, ahead of any routing rule, in every mode). The blocklist
  is the `category-ads-all` set from the same public SagerNet source as the RU
  geodata, compiled to a local `.srs` and bundled with the app — it is loaded
  strictly from disk and never fetched at runtime, so it cannot reintroduce the
  startup stall a remote rule-set caused. The core exposes it through the new
  `set_dns` command, persists it in `settings.json`, and reports it back as
  `ad_block` in `State`.
- **Custom DNS resolvers.** Settings now has two resolver fields — the encrypted
  resolver used over the tunnel and the direct resolver for destinations kept off
  it — prefilled with the values in effect. They accept the usual schemes
  (`tls://`, `https://`, `quic://`, `h3://`, `tcp://`, `udp://`, or a bare host);
  a malformed entry is flagged inline and refused by the core, and an empty field
  falls back to the default. Like the kill switch, a change re-applies to a live
  tunnel in place (a brief reconnect on the same node) rather than waiting for the
  next connect. Both resolvers ride the same `set_dns` command and are persisted
  and reported back in `State`.

## [0.3.0] - 2026-07-09

### Added

- **Autoconnect is core-owned and survives reboots.** The "Connect on launch"
  preference moved from the desktop app into the core: the daemon persists it
  in its `settings.json` (new `set_autoconnect` command, reported back as
  `autoconnect` in `State`), records the last successful connect on its own —
  the profile, plus the node only when one was explicitly chosen — and
  reconnects to it whenever the daemon starts. With the core installed as the
  Windows service that means the tunnel comes up at **system boot**, before
  anyone logs in, and comes back after service restarts such as updates; with
  the spawned sidecar it still connects when the app opens, as before. A last
  profile or node that no longer exists leaves the core idle rather than
  guessing another exit. The app's toggle now drives the core setting, and an
  enabled legacy renderer-side flag is migrated into the core once, on the
  first launch that sees it.
- **The installer now installs the core as a Windows service.** The NSIS
  installer is per-machine (one UAC elevation per install or update; earlier
  releases installed per-user) and registers `tenebra-core` as the auto-start
  `tenebra` service: stopped before an update replaces its files, re-pointed
  and restarted after, removed on uninstall. As a service the core keeps its
  profile store in `%ProgramData%\Tenebra\data` — created with a DACL that
  admits only SYSTEM and Administrators, since profiles carry subscription
  credentials and unprivileged users reach them through the pipe protocol —
  and resolves the bundled sing-box, wintun and rule-sets from `resources\`
  next to `tenebra-core.exe`. Console and sidecar runs keep their per-user
  paths. Upgrading over a per-user 0.2.x install retires the old copy for the
  installing user (uninstall entry, autostart, `tenebra://` handler,
  shortcuts); the old `%LOCALAPPDATA%\Tenebra` files are left on disk, inert,
  rather than have the elevated installer execute or delete through a
  user-writable directory, and per-user profile stores are not migrated —
  re-import the subscription in the app.
- **Windows service mode for the core.** Started by the service control manager,
  `tenebra-core` serves the control protocol on the `\\.\pipe\tenebra` named pipe
  (DACL: SYSTEM, Administrators, the interactive user — the Tailscale LocalAPI
  trust model) instead of stdin/stdout, logs to `%ProgramData%\Tenebra\service.log`,
  and tears the tunnel down on service stop. One client session is active at a
  time: a new connection displaces the old, and a client disconnecting leaves the
  tunnel up. `tenebra-core --pipe` serves the same transport from a console for
  development. Installing the core as the service (installer work) is a separate
  step.
- **The desktop app attaches to a running service.** At startup the GUI probes
  the control pipe: if a core is listening — the installed service, or
  `tenebra-core --pipe` — it attaches to it instead of spawning its own
  sidecar, re-syncing state with a `status` request on every new session. A
  dropped session (service restart, another client taking the pipe over) is
  reported in the UI and redialed with capped backoff until the service is
  back. With no service listening the app spawns the stdio sidecar exactly as
  before; `TENEBRA_PIPE` renames the pipe or (`off`) skips the probe during
  development.
- **Update prompt on launch.** The desktop app now checks for a new signed
  release once at startup and offers it in a slim banner under the top bar —
  "Update" installs and restarts, "Later" hides it until the next launch. An
  offline or failed check stays silent. A new Settings toggle ("Install updates
  automatically") skips the banner and applies a found update right away,
  restarting into the new version; if that silent install fails, the banner is
  shown instead.

### Changed

- **A dropped service connection is no longer an instant error.** When the
  control-pipe session ends mid-run, the app now shows a "Reconnecting to the
  Tenebra service…" status while it redials, and only reports an error (state
  and notification) if the service stays away past a short grace window (8 s).
  A service restart during an update, or another client briefly taking the
  session over, comes back well inside the window and no longer flashes
  "Connection failed" while the tunnel is in fact fine.
- The installer artwork now matches the app: a branded sidebar and header on
  the installer pages, and the eclipse mark as the installer icon.
- The version badge in the top bar is derived from `package.json` at build
  time instead of a hand-bumped literal, which had been left at v0.1.1 in the
  0.2.0 release.

### Fixed

- The installer's service registration silently failed on every install: the
  `binPath` quoting collapsed into a form that `sc.exe` splits at the space in
  "Program Files", so it printed its usage text (exit 1639) instead of
  creating the service, and `nsExec` swallowed the output. The path is now
  escaped so it survives argv splitting in one piece — and the registered
  image path is quoted, closing the unquoted-service-path gap as well.

## [0.2.0] - 2026-07-09

### Added

- **Kill switch**, now armable from the UI. While it is on, the tunnel's
  `strict_route` blocks traffic that would otherwise leak, and an unexpectedly
  dead sing-box process is relaunched automatically on the same node (bounded so a
  crash-loop can't churn forever, with the budget refunded once a relaunched tunnel
  stays up). It is best-effort by design: in the brief window between the process
  dying and the relaunch, the OS routes normally — documented, not hidden.
- **Switchable TUN stack** (`system` / `gvisor` / `mixed`) in Settings, applied to
  a live tunnel without a manual reconnect.
- **Reactive tray**: the tray icon reflects the connection state (idle / connected /
  error) and the Connect / Disconnect items enable and disable to match.
- **Desktop notifications** on real connection transitions (connected, disconnected,
  error, kill switch engaged), debounced so a steady state never repeats a toast.
- **Deep links**: `tenebra://import?url=…` opens the import flow pre-filled, and
  `tenebra://connect?profile=…` connects a profile. Links are parsed in one place
  and delivered to the app whether it is already running or launched by the link.
- **Launch minimized**: with autostart enabled, a login launch starts hidden in the
  tray while auto-connect still runs.
- **DoH fallback for subscription fetch.** When a subscription host fails to fetch
  at the transport layer — the fingerprint of DNS tampering — the client retries
  once over a resolver reached by DNS-over-HTTPS, dialed to the resolver's literal
  IP so it bypasses the system resolver while keeping the original TLS SNI. No new
  dependency; the primary path is unchanged.
- **macOS and iOS porting plans** under `docs/porting/`.

### Changed

- New application icon: an eclipse-corona mark replacing the placeholder set.
- Release pipeline hardening: tagged releases are gated on the full test suite and a
  version/tag consistency check, the update-signing key is confined to the tagged
  release instead of every CI run, the version lives in one script across its four
  files, and eslint, clippy and rustfmt now run in CI.

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
- Kill-switch races: a toggle during the connecting window is now reconciled onto the
  tunnel that comes up (instead of being reported but not applied), and a relaunch
  can no longer resurrect a tunnel after an explicit disconnect or outlive shutdown.
- The Settings radio groups now move focus with the selection, so arrow keys traverse
  the full set instead of stalling after one step.
- `cargo test` no longer fails on macOS and Linux (a test hardcoded a Windows-only
  child process).

### Security

- The sing-box clash API — the loopback control surface the client polls for
  traffic counters and connectivity probes — now requires a per-run secret.
  Without one, any other local process could read the active connection list or
  switch the selected outbound over `127.0.0.1`. The secret is drawn from a
  cryptographic RNG on each run and presented as a bearer token by the client's own
  polling, so the app keeps working while other local processes are turned away.
- The update-signing key is no longer exposed to routine CI: it was injected into the
  desktop build on every push and pull request, and is now confined to the tagged
  release workflow. CI builds the installer with updater artifacts turned off.
- `SECURITY.md` now documents the update-signing key's custody, rotation and
  leak-response plan.

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

[Unreleased]: https://github.com/Divaaaan/tenebra/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/Divaaaan/tenebra/compare/v0.4.6...v0.5.0
[0.4.6]: https://github.com/Divaaaan/tenebra/compare/v0.4.5...v0.4.6
[0.4.5]: https://github.com/Divaaaan/tenebra/compare/v0.4.4...v0.4.5
[0.4.4]: https://github.com/Divaaaan/tenebra/compare/v0.4.3...v0.4.4
[0.4.3]: https://github.com/Divaaaan/tenebra/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/Divaaaan/tenebra/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/Divaaaan/tenebra/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/Divaaaan/tenebra/compare/v0.3.7...v0.4.0
[0.3.7]: https://github.com/Divaaaan/tenebra/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/Divaaaan/tenebra/compare/v0.3.4...v0.3.6
[0.3.4]: https://github.com/Divaaaan/tenebra/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/Divaaaan/tenebra/compare/v0.3.0...v0.3.3
[0.3.0]: https://github.com/Divaaaan/tenebra/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Divaaaan/tenebra/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/Divaaaan/tenebra/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/Divaaaan/tenebra/releases/tag/v0.1.0
