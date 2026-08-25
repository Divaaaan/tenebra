# Changelog

All notable changes to Tenebra are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/).

> **Early days.** Tenebra is at 0.x: the desktop clients (Windows and macOS) are
> the current focus — see the
> [project status](README.md#project-status). Expect breaking changes between
> 0.x releases.

## [Unreleased]

### Fixed
- **On a service install the bypass never installed itself and never updated.**
  `main()` hands off to the service path before it reaches the console entry
  point, and the background job that keeps the bundle present and current was
  started only by the latter — so on every ordinary Windows install it did not run
  at all. The bundle arrived solely as a side effect of connecting, and the
  twelve-hour refresh that keeps it ahead of a censor that learns never happened
  once. Nothing failed and nothing was logged: from the outside it looked like a
  bypass that was simply getting worse over time. Both entry points now start the
  daemon's background work through one named function, and the service path has a
  test that fails if it stops doing so.
- **The bypass could not be switched on from the app at all.** Hardening the
  daemon in 0.5.1 gated four control commands behind a peer holding an *enabled*
  Administrators membership. Three of them — `start_zapret`, `pick_zapret`,
  `update_zapret` — sit behind buttons in the shipped interface, and the desktop
  shell runs as the ordinary interactive user, whose token lists Administrators
  deny-only. So the switch, the "find a strategy" button and the manual bundle
  check answered "administrator rights required" for every user on every machine,
  with no elevated path in the app to offer, while the log filled with refusals
  nobody was looking at. The gate now covers only `import_zapret`, the one command
  that unpacks bytes of the caller's choosing: the other three attach or refresh a
  bundle the daemon installed itself, from a pinned release matched against a
  checksum compiled into the binary or from the copy embedded in it. Choosing to
  run already-trusted code was never the escalation — supplying the code is.
- **A machine that had never connected had no bypass to control.** Installing the
  bundle hung off connecting and nothing else, so on a fresh install every bypass
  control answered "load a bypass bundle first" — including the re-pick button,
  which is what a user reaches for precisely when video has stopped working. The
  embedded copy now goes down when the daemon starts: no network, no waiting, and
  the bundle is present before anything asks for it.
- **The strategy picker never measured video actually streaming.** One of the five
  destinations a strategy is scored on was `rr1---sn-4g5e6nls.googlevideo.com` — an
  individual CDN node's hostname, of the kind a player is handed for a single
  session. Those are allocated per region and retired, and this one had stopped
  resolving: the target answered NXDOMAIN for every strategy and for the
  no-bypass baseline alike. The ranking looked healthy because everybody lost the
  same point, but the one slot standing for video streaming — the thing a censor
  throttles while the page still loads — measured nothing, so a strategy that
  fixed the page and left the video spinning could win the run. It is
  `redirector.googlevideo.com` now, a stable name in the same SNI space, and a
  test rejects per-session node names in the probe set.
- **A manual bundle check could sit for over two minutes without answering.** The
  command inherited no deadline of its own while the HTTP client allows ninety
  seconds per request across two requests, so on a network where GitHub is
  unreachable — a large share of the networks this bypass exists for — the button
  spun until the user gave up. It is bounded at 45 seconds and reports what
  happened.

## [0.5.4] - 2026-08-25

### Fixed
- **The screen showed no bypass on a machine where the bypass was installed and
  running.** Whether one existed was a renderer-side flag, set only by a manual
  import made in the current session and reset by every restart — so on the very
  path the product is built around, the core downloading a bundle on the first
  connect and bringing the packet filter up with the tunnel, the app drew "no
  bypass" over a working one and went on asking for the archive. The core has
  reported the truth in every status snapshot since 0.5.0 (`zapret_active`,
  `zapret_strategy`, `zapret_version`); only the Settings screen read any of it.
  Both views read it now: the shell's bottom bar carries the packet filter's
  state and the strategy carrying traffic, the one-button screen names the same
  thing under the status word, and neither says anything at all until the core
  reports a bundle. A filter that is up counts as installed even when the bundle
  carries no version marker.
- **A missing core no longer looks like a working one.** When `tenebra-core`
  could not be located or would not start, the app attached to its own demo fake
  instead: invented profiles, a connect that "succeeded" a second and a half
  later on a timer, fabricated bypass strategies and a "bypass enabled" toast for
  a packet filter that did not exist. Nothing on that screen was true, and
  nothing on it said so. The fallback is now a backend that refuses every command
  with the reason the transport gave, which the app already renders as "no
  connection to the background service — retrying"; the one-button screen says it
  too, rather than showing a calm "disconnected". The demo fake is still there
  and still selected by `TENEBRA_MOCK=1` — by name, never by accident.
- **A bundle the core refused to install on trust was reported as "already
  current".** `update_zapret` answers with `blocked` when a newer bundle exists
  whose checksum this build does not carry, and the desktop bridge dropped the
  field on its way through, so the one answer that means "update Tenebra to get
  the fix" arrived as the one that means "nothing to do here".
- **The Settings switches for the bypass updater moved on click rather than on
  the core's reply.** Nothing in the response reaches the stored snapshot, so a
  refused toggle stayed where it was clicked. Every bypass control now re-reads
  the core's state after acting.
- **The bypass readout went dark the moment you pressed Connect.** The core
  replaced its whole status on every state change and rebuilt only part of it,
  dropping the four bypass fields — so the answer to `connect`, taken *after*
  that same connect had raised the packet filter, described a machine with no
  bypass on it, and nothing put it back: status re-reads that answer, and the
  state event carries a phase, not a reading. The fields now survive a state
  change like the split and DNS settings do. The clients hold their last reading
  across an answer that carries none, so an older core (the hand-installed macOS
  daemon skews behind the app) no longer blanks the readout either.
- **Turning off automatic bundle updates sprang back to on.** `false` never rode
  the wire — every flag in the status is omitted when empty, and this is the one
  whose default is on, so an omitted "off" read back as "on" while the core had
  stored the choice correctly. It is now always reported, false included.
### Removed
- **The manual bypass import, and user blocklists with it.** The first screen
  asked for a zapret archive: find the release page, pick the right asset, drag
  it in. The core has downloaded and installed one by itself since 0.5.0, so this
  was work handed to the user that the program already does — and the drop zone
  kept being offered over a bundle that was installed and running, because it
  read that session flag. User-supplied domain blocklists are gone outright: the
  app parsed them and there has never been a command that hands the result to the
  core, so the import had no effect beyond the count it printed (0.5.2 stopped it
  claiming otherwise; this removes the road to the claim). Gone with them: the
  bypass panel, its bottom-bar button and count, the zip reader and bundle
  sniffer written to feed it, and the `import_zapret` command in the desktop
  bridge. The core's own archive install is untouched — it is what the updater
  uses, and unpacking a bundle into the data directory by hand still works.
### Changed
- **Switching the bypass on and off, and re-measuring its strategies, live in
  Settings.** They were only ever reachable from the panel that has been removed,
  next to the version and the updater switch they belong beside. The status line
  there is the core's: which strategy is running, or that none is.
### Added
- **The bypass bundle ships inside the build, so a first connect no longer
  depends on reaching GitHub.** The bypass used to exist only after a download,
  and there were four ways for that download to end with nothing installed: no
  network, GitHub blocked outright — which is precisely the network this
  product is for — a published release newer than any checksum the build
  carries, or an archive that did not match the checksum it does. The third of
  those emptied every fresh install the day upstream published 1.10.2 ahead of
  its pin (see 0.5.3), and pinning faster does not fix it: the pin has to ship
  before the version it names is needed. Bundle 1.10.2 is now compiled into the
  core — the release archive byte for byte — and installed straight from those
  bytes whenever the download cannot deliver one. It is a floor, not a ceiling:
  a newer release this build pins still replaces it on the next check, a bundle
  already installed is never overwritten, and a newer one is never rolled back.
  A build whose embedded archive and pinned checksum have drifted apart fails
  its tests, which is the class of mistake the whole change exists to end. The
  cost is size, and only where the bypass runs: the Windows core binary grows
  from 11.9 MB to 13.4 MB, while the macOS and Linux ones — where the bundle is
  a Windows packet filter nothing there could start — carry neither the archive
  nor the code to unpack it, and stay the size they were. Because the bundle is
  now redistributed rather than only fetched,
  [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md#2-components-downloaded-at-runtime)
  records it as shipped, with the licenses and copyright holders it carries.
### Changed
- **"Update the bundle automatically" now governs downloads only, not whether
  the bypass exists.** Unticking it used to suppress the first-connect install
  outright, which — once a bundle ships inside the build — reads the switch as
  something it never said. Fetching a release is this application reaching the
  network on its own and is exactly what the switch is for; the compiled-in copy
  costs no request, needs no update, and is the release the client was built and
  tested against. So with updates off Tenebra still asks the release page for
  nothing, and a first connect with no bundle present still unpacks the copy it
  shipped with. Someone who unticked that box turned off updates, not their
  bypass — and a fresh install with the box unticked no longer connects with
  YouTube and Discord quietly taking the round trip through an exit node.

## [0.5.3] - 2026-08-24

### Fixed

- **The bypass installs again on a fresh machine.** Upstream published bundle
  1.10.2 while 0.5.2 was building, and the client only auto-installs a version
  whose checksum it carries — so anyone installing Tenebra after that release got
  no bypass at all, with the tunnel working but YouTube and Discord left to the
  ISP's DPI. The checksum for 1.10.2 is pinned, verified byte-for-byte against
  the archive upstream publishes. The underlying problem is that a pin has to be
  shipped before the bundle it names is useful; a bundle carried inside the build
  removes the race entirely, and that is the next change.


## [0.5.2] - 2026-08-24

### Fixed

- **The bypass panel no longer answers a domain blocklist with a rule count
  nothing acts on.** Dropping a hosts file or an archive of lists parsed it,
  listed the source with "12400 rules" beside it and put a badge on the bottom
  bar — which is precisely what a successful import looks like. Nothing was sent
  anywhere: no command exists to hand those rules to the core, so the counter was
  the entire effect of the import, and it read as "loaded" for a list that
  changed no routing and blocked nothing. The panel now takes only what it can
  actually apply — a zapret bundle, still recognised by what is inside it rather
  than by its name — and turns everything else away in the words it already had
  for a file that is not one. Its English title says what the panel is, the DPI
  bypass, instead of promising blocklists.
- **The bypass strategy pick was measuring the tunnel, so under a live
  connection it could never find a strategy.** The probe that scores each
  strategy built its HTTP client with no proxy — which refuses an HTTP proxy and
  says nothing about anything else — and left the socket to ordinary routing.
  With a tunnel up the tun owns the default route, so every request went through
  it: the baseline taken with the bypass off answered 5/5 for destinations the
  ISP blocks, each strategy answered 5/5 as well, and since a strategy is
  reported only when it beats the baseline, the run concluded that nothing
  helps. The automatic re-pick depends on that verdict — when the bypass check
  finds video is not coming through, re-measuring the bundle is what is supposed
  to find a strategy that works — so the repair was dead exactly when it was
  needed, and a hand-run pick under a connection always answered "no strategy
  pierced the block". The probe is now bound to the very interface the packet
  filter is confined to — one lookup answers both, so the measurement cannot end
  up on a different link from the filter — and the baseline and every strategy
  are measured there. A machine where that bind cannot be made falls back to a
  routed dial rather than failing every probe, and says so in the log: the pick
  reports which interface it measured on, and the "no strategy pierced the
  block" verdict now carries the baseline that produced it, so a run that
  measured the wrong path can be told apart from a network no strategy helps.
- **A probe could leave through another VPN's adapter.** The interface picker
  behind the node ping — and behind the bypass pick when nothing is pinned —
  took the lowest-numbered routable adapter and excluded only our own tun, by
  exact name. Anything else tunnel-shaped passed: another client's adapter,
  whenever it held the lower index — the ordering says nothing about what an
  adapter is — and our own tun after Windows renamed it "tenebra 2" (which it
  does when the previous one has not finished going away). A node ping through
  either reports the tunnel's fabricated ~1ms instead of the server's real
  latency. Tunnel-looking adapters are now excluded by the same recognition the
  tun-conflict guard uses, and our own tun by name prefix rather than an exact
  match.
- **Nodes addressed by name were left in the packet filter's path.** The bypass
  runs on the physical uplink, which is where our own tunnel's packets to its exit
  node go, so the exclusion list is the only thing keeping a desync off the
  handshake to our own node. That list took IP literals and dropped anything else
  on the floor — no count, no log — and subscriptions that address their nodes by
  hostname are the ordinary case. The symptom is the one the exclusion exists to
  prevent: the node's port opens and then goes quiet, which reads as a dead node
  and sends the user hunting through their subscription for a fault on their own
  machine. Names are now resolved and their addresses written, which is what the
  file can carry — winws takes `--ipset-exclude` as one ip/CIDR per line and
  answers anything else with "bad ip or subnet", so a hostname in there was never
  a lookup deferred to winws, only a discarded line. The names are asked of the
  same resolvers the tunnel will use: the machine's own, and the direct resolver
  from the routing config — the one sing-box resolves outbound server names
  through (`route.default_domain_resolver` → `dns-direct`). Asking only the
  machine's resolver leaves the hole open: where the two disagree — a name the ISP
  poisons, which is often the very reason a node hides behind one, or geo-DNS and
  round-robin handing each caller a different record — the list covers an address
  nobody dials while the filter desyncs the one that is, and the symptom returns
  with no warning at all, because the lookup "succeeded". Every answer from every
  resolver is written; excluding an address the tunnel does not use costs nothing
  but one address the filter leaves alone. What each name last answered with is
  remembered beside the list, carried across bundle updates and capped at 256
  names, so a DNS outage no longer erases an exclusion that was already working —
  the list is written from memory instead, and the nodes running on an answer
  older than an hour are named in the debug log. That memory is insurance against
  a resolver that cannot be reached, not a stand-in for one that can: an address
  confirmed within the last two minutes is written straight out, and anything
  older waits for the resolver first, so a node that has changed address is
  protected on the address it moved to rather than on the one it left. Through
  autoconnect that is the first connect of every launch, where the address
  written could otherwise be weeks old and never once confirmed — winws reads its
  lists once, at startup, so an answer that lands a moment later does nothing
  until the next start. The waiting ends per name on the first resolver that
  answers it, plus a short grace for a second resolver that is merely slower, so
  one blocked resolver no longer costs a name the whole budget — which on a line
  that swallows DoH, and the shipped default direct resolver is DoH, was every
  new name on every first connect. Measured with one resolver answering at once
  and one blocked: three names cost 2.0 s before and 0.40 s now, while a
  reconnect a moment later stays at about 0.5 ms, and a name that is simply dead
  still costs the budget once rather than every time. A resolver whose transport
  this client cannot speak (`quic://`) leaves the machine's own resolver doing
  the work as before, and now says so in the debug log instead of falling back in
  silence. Answers no node can sit behind —
  `0.0.0.0`, loopback, link-local — are refused rather than written, since a
  resolver that hijacks a dead name would otherwise have the node counted as
  covered. Names left with no address are reported and logged by name, so a node
  still in the filter's way is a warning in the log instead of silence.

## [0.5.1] - 2026-08-24

### Security

- **Any local user could run code as SYSTEM.** `import_zapret` accepted an
  arbitrary zip and unpacked it into the daemon's own directory — validating only
  that the archive held a `.bat` and a `bin/winws.exe`, never their contents —
  and `start_zapret` then ran the named `.bat` through `cmd.exe`, which in a
  service install is LocalSystem. The pipe DACL admits any interactive user by
  design, so an unprivileged process could send both and get SYSTEM with no UAC
  prompt, straight through the directory clamp that exists to stop exactly that.
  The four commands that carry executable code into the daemon's directory or run
  what is there — `import_zapret`, `update_zapret`, `pick_zapret`,
  `start_zapret` — now additionally require a peer that already holds the
  daemon's authority: its own account (the core running as an ordinary process of
  its user, where there is no boundary to cross), or a token with an *enabled*
  Administrators membership. A UAC-filtered token lists Administrators deny-only
  and does not count; the prompt is the point. Everything else — status, connect,
  routing, `stop_zapret` — stays open to the interactive user, and the daemon's
  own first-run install and auto-update are unaffected, so a bypass still arrives
  without anyone handing the daemon a file.
- **Peer authentication now fails closed.** A peer whose identity could not be
  read, or a console user the daemon could not determine, used to be *admitted*
  with a warning rather than refused — which is what made the escalation above
  reachable from an unidentified caller. Both are now refusals, still logged with
  the reason so a genuinely stuck attach is diagnosable instead of mysterious.
- **The bypass bundle is verified before it is installed, and only pinned
  versions install automatically.** The updater took the download URL out of the
  release feed as it stood — no scheme check, no host check — followed its
  redirects, unpacked the result and put it in place; the size the feed published
  was parsed and never compared to anything, and no checksum was consulted at all.
  Since the bundle installs itself on the first connect and re-checks every twelve
  hours, and a batch file out of it is then run through `cmd.exe` by a service
  account, anything able to alter that download got code execution as LocalSystem
  on a schedule. Now the archive may only come from `github.com` or the GitHub
  release-asset hosts it redirects to (`release-assets.githubusercontent.com`,
  `objects.githubusercontent.com`) over https — checked on the first URL and on
  every redirect after it — must arrive at exactly the length the release
  declares, and must hash to a SHA-256 pinned into the client for that version.
  A version the client does not pin is not installed at all: the digest GitHub
  publishes beside the asset travels the same connection as the archive, so it
  cannot stand in for a pin against a network able to forge that connection —
  which, on the networks this app is for, is the whole threat. Such a release is
  reported instead ("a newer bypass bundle is out — update Tenebra"), the working
  one is kept, and the pin ships with the next client release. The verified bytes
  are unpacked straight from memory, so nothing re-reads a file that could be
  swapped between the checksum and the install. A refusal that means tampering is
  logged as an error and named on screen; "there is a newer bundle you do not pin
  yet" is a quiet, actionable notice rather than an alarm.

### Added

- **The log now says why a connect went the way it did.** The core had 73 log
  calls and almost none of them on the live path: which exit a walk leads with
  and on what grounds, why a connect was refused before it ever started, what a
  node probe measured stage by stage, whether the DPI bypass came up and under
  which strategy, and what the tun-conflict guard actually saw in the route table
  were all decisions taken in silence. They are recorded now — decisions at
  `info`, the evidence behind them at `debug`.
- **A diagnostics report, in one action.** `collect_diagnostics` assembles the
  core's state, its build versions (core, sing-box, bypass bundle), every routing
  option in force, the stored profiles, the last fallback walk, the machine's
  interfaces and default routes, and the tail of the log into one file the user
  can attach to a bug report. Settings → Diagnostics saves it and shows where.
  It probes nothing and sends nothing, and subscription tokens and node
  credentials are masked by the same rules the app already applies to the
  diagnostics it copies from its log console.
- **A log level that filters.** The level constants existed and nothing consulted
  them. The core now drops anything below its threshold — `info` by default, so a
  shipped build never narrates itself — and `TENEBRA_LOG_LEVEL=debug` raises it
  for a support session. It is an environment variable rather than a stored
  preference on purpose: debug someone turned on in January and forgot about is
  the same disease as a log with no ceiling.
- **Changing the exit no longer drops what you were doing.** Picking another node
  on a live tunnel used to tear the whole thing down and build it again: the tun
  went away, every connection went with it, and a download or a call paid for a
  node change. The node is now switched inside the running sing-box — same
  process, same tun, same routes — and connections already open finish on the exit
  they started on while new ones take the new node. The switch is confirmed before
  it is reported, and anything that cannot be switched this way (a node the running
  config never had, a two-hop chain, a control API that refuses) falls back to the
  reconnect it always did. The UI says which of the two happened rather than
  showing "reconnecting" for both.
- **A degraded exit is left behind without dropping the tunnel.** The health
  watchdog no longer reconnects the moment the active node stops carrying traffic:
  it measures the other exits through the process that is already running — the
  same several-destinations standard `check_nodes` applies — and moves onto the
  best one live. It is damped so a bad local network cannot walk you around the
  node list: one move at most every three minutes, three in any fifteen, a node
  that just failed passed over for ten, and after that the log says why nothing is
  moving instead of churning exits.
- **The screen says something while the connect measures nodes.** That check
  opens real connections through every node before an exit is picked — several
  seconds during which no tunnel exists, so the core has no phase to report and
  the main screen sat on "Disconnected", perfectly still, as though the click had
  missed. Measuring is now a state of its own: its own status word and sub-line,
  a scanning indicator distinct from the connecting blink, a running progress
  rail, and a button that reads as busy rather than offering a hover fill it will
  not honour. It never speaks over a phase the core is actually reporting.

### Changed

- **The DPI-bypass bundle is credited to the projects it comes from.** Since
  0.5.0 the app installs the bypass itself, which means it downloads the
  [Flowseal/zapret-discord-youtube](https://github.com/Flowseal/zapret-discord-youtube)
  release onto the user's disk — [zapret](https://github.com/bol-van/zapret) by
  bol-van (MIT), the [WinDivert](https://github.com/basil00/WinDivert) packet
  driver, and the Cygwin runtime — and THIRD-PARTY-NOTICES.md mentioned none of
  them. It now has a section for software fetched at run time, with WinDivert's
  LGPLv3-or-GPLv2 choice settled on the record in favour of LGPLv3 and the
  Cygwin linking exception quoted; the generator emits that section, so
  regenerating cannot quietly drop it again. The README explains what is
  downloaded, where it lands, and which switch stops it from being downloaded at
  all. `github.com/sagernet/gomobile`, in `go.mod` since 0.4.5 and never
  listed, is in the notice too — the generator had been refusing to run over it
  since July.
- **One motion system across the app.** Timings were previously invented per
  stylesheet — 80/100/160/180/200/220/240/260ms, some `linear`, some bare `ease`
  — which is why state changes read as a set of unrelated twitches. There are now
  five durations and three curves in `tokens.css`, picked by what is moving:
  arrivals ease out, departures ease in, loops move symmetrically. Enters and
  exits share one set of keyframes instead of eight near-identical local ones.
- **Overlays, modals and panels animate out, not only in.** Settings, logs, the
  import dialog, the blocklist panel, the deep-link, tun-conflict and update
  confirms, and the crash report were all torn down in the same tick they were
  dismissed — the half of a transition a user reads as "did that crash?". Each is
  held for its exit, showing what it showed rather than flashing an empty card.
- **The progress rail no longer shifts the layout.** It used to mount with the
  work, moving everything under the status word by 4px at the exact moment the
  status changed; the track is now a permanent hairline and only the runner comes
  and goes. Both rails (connect and leak check) sweep on `transform` instead of
  animating `left`, which was relayouting on every frame.
- **The node list settles faster.** The row cascade is capped, so a sixty-node
  subscription stops dealing itself out for a second and a half; hover and
  selection marks scale in on the compositor instead of fading a painted inset
  shadow. Buttons take a press.
- **`prefers-reduced-motion` now zeroes animation delays too.** Zeroing only the
  durations left every stagger intact, so a reduced-motion cascade still dealt
  itself out one delay at a time — each row flashing into place, which is worse
  than the animation it was meant to remove.

### Fixed

- **The "another VPN owns the default route" guard can now see the VPNs it was
  written to catch, and no longer mutes itself.** Four things were wrong at once,
  and each of them alone was enough to make it report "all clear" on a machine
  with another client holding every route:
  - It looked for a literal `0.0.0.0/0` and nothing else. The usual way to take a
    machine over is not to touch the default route at all but to lay
    `0.0.0.0/1` + `128.0.0.0/1` over it — two routes covering the same address
    space, each more specific than a default route, so they win whatever its
    metric. That is what OpenVPN installs for `redirect-gateway def1` and what a
    Tailscale exit node installs. Coverage is now decided honestly, by asking
    whether an interface's routes leave any address uncovered, so the four
    `/2`s or any other partition that tiles the space is caught as surely as the
    `/1` pair — not just the one shape the check happened to know. The route table
    is also read for IPv6 now, as the guard always claimed it was: a tunnel owning
    `::/0` takes every AAAA-resolved destination on a dual-stack machine.
  - Tunnels were recognised by a short list of name fragments, which is not
    enough. Tailscale, NordLynx, Cloudflare WARP and OpenVPN's TAP adapter — the
    last of which Windows calls "Ethernet 2" — all read as ordinary network
    cards. Windows is now asked what it thinks (the interface type, and the
    driver's own description), and the name list has grown the vendors that
    describe themselves in no generic words at all.
  - An unrecognised tunnel was counted as the machine's physical uplink. Since
    tunnels write their route at metric 0, that set the bar to 0 and every
    genuine conflict at metric 1 or worse was waved through as parked at a losing
    metric — one invisible tunnel switched the guard off for all the visible
    ones. Our own tun is excluded from that calculation too, by prefix, so the
    "tenebra 2" name Windows hands back when the first is still going away is
    still recognised as ours — and by whatever interface name the user configured,
    not just the built-in one. Metrics are now the effective ones Windows compares
    (route metric plus interface metric), not the route metric alone, which is 0
    for a physical uplink behind a Hyper-V switch as readily as for a tunnel; and
    they are compared within an address family, since the stack ranks IPv4 routes
    against IPv4 and IPv6 against IPv6 — collapsing the two made a tunnel winning
    on IPv4 look parked next to a cheaper IPv6 uplink.
  - Autoconnect never consulted the guard at all — so it was off on exactly the
    path it was written for: a machine starting up with another VPN's service
    already running, with nobody watching to work out why the internet died.
- **The two presets that route traffic around the tunnel no longer ship on, and
  are finally on screen.** `set_presets` existed only in the core: nothing
  carried the presets to the app, no switch showed them, and the only way to turn
  one off was to hand-edit `settings.json` — while games-direct and real-time-UDP
  direct were both on from the first launch. Real-time UDP is ports 50000-65535,
  which on a desktop is browser calls and torrents, so a fresh install showed
  "connected" while handing the ISP address to whoever was on the other end of a
  call. Both now default off and both have a switch in Settings that says what
  they cost; unblock-services keeps its default, because it only ever moves
  traffic *into* the tunnel. Upgrading from 0.5.0 closes the same leak: 0.5.0
  wrote both presets on into `settings.json` while giving no way to see or change
  them, so a one-time settings migration clears that stored `true` — it was a
  default the user was never shown, not a choice — and both come up off, the same
  as a fresh install. A choice made from now on, through the new switches, is
  honoured and kept; unblock-services is left exactly as stored.
- **`java.exe`, `javaw.exe` and `launcher.exe` are out of the games preset.**
  Matching is by bare executable name, so those three took every JVM program on
  the machine — an IDE, a corporate client, a build tool — plus any of the many
  unrelated binaries shipped as `launcher.exe` out of the tunnel, under a switch
  the user read as "keep games direct". Minecraft's Java edition now needs a
  hand-added entry; its launcher is still covered.
- **The kill switch holds on every direct-pin path, not two of five.** Only the
  games and voice presets asked whether it was armed. Everything else that pins
  traffic to the direct outbound ignored it: the services handed to a running DPI
  bypass (YouTube, googlevideo, ytimg, all of Discord), the bundled Russian
  banking and government rule presets, and custom `rules_direct` suffixes — each
  on the routing layer and again on the DNS layer, so the names were also resolved
  by the ISP's resolver while the app showed the kill switch armed. All of them
  now yield to it. Proxy-direction rules are deliberately untouched (dropping
  those would hand a domain back to the geo split, which in smart mode can send it
  direct), and apps the user listed under split-exclude stay direct, since that is
  a choice made one executable at a time.
- **Simple mode: the connect button no longer dies on the "another VPN owns the
  default route" refusal.** The override prompt was rendered only by the full
  shell, and simple mode returns its own screen well before that point — so on a
  machine with a second VPN up the connect asked a question nothing drew, waited
  on an answer that could never arrive, and left the one button on the screen
  disabled until the app was restarted. Both views now render the same prompt,
  and the question is released if the tree goes away while it is open, so no
  future screen can strand a connect the same way. Declining also reports the
  refusal once instead of twice: cancelling is an answer, not a second failure.
- **The logs no longer grow without limit.** `service.log` was appended to
  forever, and the desktop shell's `core.log` with it. Both now roll over by
  size and keep a bounded number of generations, so a long-running service costs
  a fixed amount of disk instead of an unbounded one. The Windows service's
  guard against a planted log file is re-checked on every rotation, not only at
  start-up — a rotation is precisely the moment the path is briefly free to
  claim.
- **The daemon's own log reaches the log file.** Every line the core produced
  went only to an attached UI, so a service that failed to autoconnect at boot —
  with nobody logged in and no app running — left no record of it anywhere. Those
  lines now go to the process log as well.
- **A fresh tunnel can no longer come up on a different exit than the one it
  reports.** sing-box restores a selector from its cache file before it applies
  the config's own default, so once anything had moved that selector a later start
  could seat the tunnel somewhere the config did not name. Every start now pins the
  selector to its own default before the connectivity probe is believed.
- **Several animations that had never once run.** The stylesheets composed two
  duration+easing tokens into one shorthand (`var(--t-base) var(--t-enter)`),
  which expands to two easing functions and makes CSS drop the whole declaration.
  The blocklist scrim and panel, its imported-list rows and error line, the hint
  popover and its lines, the first-run setup strip, the node-check verdict badge
  and the blocklist-count badge were all dead on arrival, along with four hover
  transitions written the same way.

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
