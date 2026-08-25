# Control protocol

The desktop UI never touches the tunnel directly. A core process owns sing-box
and the wintun tunnel; the UI drives it with **line-delimited JSON** — one JSON
object per line, UTF-8 — over one of the [transports](#transports) below. (For
where this sits in the system, see [architecture.md](architecture.md).)

Three message kinds flow over the link:

- **requests** (UI → core) carry an `id` and a `cmd`;
- **responses** (core → UI) echo the `id` with `ok`, plus `data` or `error`;
- **events** (core → UI) are unsolicited, carry no `id`, and have an `event`
  field.

## Transports

The protocol is transport-agnostic byte-stream framing; nothing about the
messages changes between transports.

- **stdio** (the default). The UI spawns `tenebra-core` as a sidecar and owns
  its stdin/stdout: requests go in on stdin, responses and events come back on
  stdout, diagnostics go to stderr. One process, one client — stdin reaching
  EOF tears the tunnel down and ends the core. Because the sidecar opens the
  tunnel itself, this mode needs the whole app to run elevated on Windows.
- **named pipe** (Windows): `\\.\pipe\tenebra`. Used when the core runs
  detached from the UI — as the Windows service, or via `tenebra-core --pipe`
  from a console for development. The tunnel then outlives any one UI process,
  and the UI does not need administrator rights. Diagnostics go to
  `%ProgramData%\Tenebra\service.log` in service mode (a service has no
  stderr), and to stderr under `--pipe`.
- **unix domain socket** (macOS, Linux): `/var/run/tenebra.sock` on macOS,
  `/run/tenebra.sock` on Linux. The same arrangement as the named pipe, for the
  same reason — the core runs detached, as a root LaunchDaemon or a systemd
  service, and an unprivileged GUI attaches to it — and `tenebra-core --socket`
  serves it from a shell for development. The path differs only because `/run`
  is the canonical spelling on Linux and the real directory on macOS is
  `/var/run`. `TENEBRA_SOCKET` overrides it on both ends: a path, or `off`/`0`
  to disable the transport.

  Two hazards a pipe gets from the OS for free are handled at bind time. A
  socket file left behind by a crash would otherwise wedge every later start
  (`bind` refuses an existing path whether or not anyone serves it), so the core
  dials it first: if something answers, another core owns the tunnel and this
  one refuses to start rather than steal it; if nothing answers the file is
  stale and is unlinked. And `bind` honours the umask, which would leave a
  root-bound socket unreachable to the GUI, so it is chmod'd — see below.

  The machine-scoped store lives at `/Library/Application Support/Tenebra/data`
  on macOS and `/var/lib/tenebra/data` on Linux, clamped to root-owned `0700`
  on every start.

### Named-pipe sessions

Exactly one client session is active at a time. A **new connection displaces
the current one**: the old stream is closed and the new client takes over.
This is deliberately last-writer-wins — the common case is the UI restarting
(upgrade, crash, user relaunch) and reconnecting while its old connection has
not been reaped yet.

Unlike stdio EOF, the end of a pipe session does **not** touch the daemon: the
tunnel, profiles and settings stay exactly as they are. Only the service
stopping tears the tunnel down. Two consequences for clients:

- on connect, the state is whatever it already was — send `status` (and
  `list_profiles`) first instead of assuming `idle`;
- events emitted while no client is connected are dropped, not queued.
- a client that stops reading its stream is dropped: the core gives one frame
  30 s to be delivered and holds at most 512 frames of backlog, shedding events
  (never responses) under pressure. Past either bound it closes the session, so
  the client reconnects and re-syncs like it does after a displacement.

### How the desktop app chooses a transport

At startup the GUI dials the pipe: if a core is listening it attaches (and
opens the session with the `status` re-sync above); if nothing is listening it
spawns the core as its stdio sidecar, exactly the pre-service behaviour.

The dial is deliberately patient — up to **5 s**, re-attempting every 50 ms —
because "nothing is listening" and "nothing is listening *yet*" arrive as the
same `ERROR_FILE_NOT_FOUND`, and the app cannot ask again later (see below).
Both ways of racing the service are ordinary: the installer runs
`sc start tenebra` and launches the app in the next breath, and an autostart
login starts the service and the GUI concurrently, while the core still has to
secure its data directory and load the store before it binds the name. A busy
pipe (`ERROR_PIPE_BUSY`, no free instance this instant) is waited out for 2 s
in the same loop. Anything else — an access denial, say — is returned at once;
retrying a standing condition would only stall the launch.

**The transport is chosen once and kept for the life of the process**, in
either direction. When a pipe session ends mid-run — the service restarted, or
another client displaced this one — the GUI redials with capped exponential
backoff and re-syncs when it gets back in; it never falls back to a sidecar
mid-run, since the service owns the tunnel. And an app that fell back to a
sidecar at startup does not promote itself onto the service later, since that
sidecar may be carrying a live tunnel this app owns and would take down.

That makes the fallback consequential rather than cosmetic: a core the app
spawned itself runs unelevated and keeps its profiles in the per-user store,
not the service's machine store under `%ProgramData%\Tenebra\data`, so the
profile list looks empty and a tun-mode connect fails for want of rights
(system-proxy mode is the only one that works without them). The GUI therefore
logs the fallback at `warn` naming both consequences, and on Windows keeps
looking for a listener for a minute afterwards — with `WaitNamedPipeW`, which
asks whether an instance is free without taking the session away from whoever
holds it. If the service does turn up late the GUI says so, since restarting
the app is then all it takes to land on the service.

While disconnected the GUI synthesizes `state` events of its own, since the
core cannot speak for a connection that is gone. The moment the session drops
it pushes `{"state":"connecting","error":"Reconnecting to the Tenebra
service…"}` — the tunnel may or may not still be up (a restarting service
tears it down, a displaced session leaves it), so neither `connected` nor
`idle` would be honest, and commands fail fast with a matching "reconnecting"
error the whole time. Only if the service stays away past a short grace
window (8 s — enough for a service restart under an update or a displaced
session to come back, so those never read as failures) does it escalate to a
synthetic `{"state":"error", ...}`. Either way the `status` re-sync replaces
the synthetic state with the real one as soon as a session is back. These
events are a client-side presentation detail, not part of the core's wire
contract.

`TENEBRA_PIPE` overrides the dial: an alternate pipe name, or `off`
to force the sidecar (useful in development, where a running service would
otherwise capture the session meant for a freshly built core). It is a
client-side override only — the core has no `TENEBRA_PIPE`, and the service
always serves the well-known name. (The unix transport is symmetric here
instead: both ends honour `TENEBRA_SOCKET`.)

The GUI dials with `SECURITY_SQOS_PRESENT | SECURITY_IDENTIFICATION`, capping
impersonation at identification: an instance-squatter admitted by the DACL
(see below) could learn who the client is, but cannot act as it.

### Pipe security

The pipe is created with the SDDL
`D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)`, admitting exactly three
identities:

- **SYSTEM** — the service itself;
- **Administrators** — elevated processes;
- **INTERACTIVE** — any locally logged-in user. This is what lets the
  unprivileged GUI drive the privileged service, and it is the same trust
  decision Tailscale's LocalAPI pipe makes on Windows.

The honest limits of that model:

- the tunnel is machine-wide, and so is control over it: *any* interactive
  local user — not just the one who started the GUI — can drive the tunnel,
  see its state and events, and take the session over. On a genuinely
  multi-user machine that is a real sharing of control, not an oversight.
- processes of the same user are not defended against each other; same-user
  malware already owns the session.
- remote (network-logon) callers never carry the INTERACTIVE SID, so reaching
  the pipe remotely requires administrator credentials — a caller that already
  administers the machine.

Driving the tunnel is where that trust stops. The commands that hand the daemon
executable code need more than admission — see
[Commands that need the daemon's own authority](#commands-that-need-the-daemons-own-authority).

The listener claims the name with `FILE_FLAG_FIRST_PIPE_INSTANCE`, so if
something else already holds it the service fails loudly at start instead of
silently sharing the name. That flag does not stop an *already-admitted*
identity from adding instances to the bound name later (on pipes,
`GENERIC_WRITE` implies `FILE_CREATE_PIPE_INSTANCE`) — which is another face
of the same trust statement: interactive users are trusted with this control
surface.

### Unix-socket security

The socket is chmod'd to `0666`: the unix-permission analog of the pipe DACL's
grant to INTERACTIVE, and necessary for the same reason — a root daemon binds
it and an unprivileged GUI has to reach it. Mode alone would therefore admit
every local user, so **reaching the socket is not being admitted to it**. Each
accepted connection is authenticated from credentials the kernel attached to
it, which the peer cannot forge or change after connecting: `LOCAL_PEERCRED` on
macOS, `SO_PEERCRED` on Linux.

The policy those credentials feed is shared with Windows, which resolves the
caller's SID instead: a peer is admitted if it is the daemon's own account
(root, so an elevated same-account helper is not locked out) or the user of the
interactive session. That is narrower than the historical "any local user" the
pipe DACL still grants, and it is where the two platforms differ in what
"interactive session" means:

- macOS reads the owner of `/dev/console`, which the window server chowns to
  whoever is logged in at the display.
- Linux has no such file. It reads logind's runtime state — `ACTIVE_UID` from
  the seat under `/run/systemd/seats` — and falls back to the sole per-user
  runtime directory under `/run/user` when no seat state is published. Both are
  session-manager state, not kernel interfaces: a host running seatd or no
  session manager publishes neither.

**The lookup fails closed.** When the interactive user cannot be determined —
no seat state, a session mid-transition, two seats disagreeing, an unparseable
value — or when the peer's own identity cannot be read at all, the peer is
refused and the daemon logs a warning naming the reason. An identity the daemon
cannot establish is not one it may act for: this channel drives a
LocalSystem/root process, so admitting an unknown caller hands the machine to
whoever asked first. A refused GUI is recoverable and diagnosable from the log;
a privilege escalation is neither.

The honest limits are the pipe's, restated: the tunnel is machine-wide, a
second user at the same seat inherits control of it, and processes of the same
user are not defended against each other.

### Commands that need the daemon's own authority

Being admitted to the channel is not being admitted to all of it. One command —
`import_zapret` — unpacks an archive of the caller's choosing into the daemon's
own directory. The bundle's strategies are `.bat` files the daemon runs through
`cmd.exe` — as LocalSystem in a service install, into a directory the service
clamps to SYSTEM+Administrators at every start (see `secureDataDir`) precisely so
an unprivileged user cannot plant something it will trust. A command that writes
there on an unprivileged caller's behalf reopens that hole from inside, and hands
any local user SYSTEM without a UAC prompt.

The boundary is *supplying* the code, not running it. `start_zapret`,
`pick_zapret` and `update_zapret` are **not** gated: they attach, measure or
refresh a bundle the daemon installed itself — from a pinned upstream release
matched against a checksum compiled into the binary, or from the copy embedded in
it. The caller supplies no bytes and picks no version. Gating them once shipped,
and it did not prompt anybody: the desktop shell runs as the ordinary interactive
user, whose token lists Administrators deny-only, so the whole bypass simply
stopped answering its own buttons.

So `import_zapret` additionally requires a peer that already holds the daemon's
authority, decided per connection alongside the admission check:

- the peer runs as the daemon's own account — there is no boundary to cross.
  This covers the core running as an ordinary process of the user who owns the
  GUI (the stdio sidecar, or `--pipe` from that user's console), where importing
  a bundle changes nothing about who can run what; or
- the peer's token carries an **enabled** Administrators membership (uid 0 on
  unix). Such a caller can replace the service outright, so letting it hand the
  daemon a bundle grants it nothing new. *Enabled* is the operative word: a
  UAC-filtered token still lists Administrators, marked deny-only, and that
  reads as non-administrative here — the prompt is the point.

Everyone else gets an error naming the missing rights, not silence. Nothing else
is gated: `status`, `connect`, `stop_zapret` and the routing settings stay open
to the interactive user, whose tunnel it is. `connect` may still auto-start an
installed bundle — running already-trusted code is not the escalation, supplying
the code is — and the daemon's own first-run install and 12-hour auto-update run
on its behalf, not a caller's, so an unprivileged user still gets a working
bypass without ever handing the daemon a file.

## Requests

| cmd                    | fields                             | returns                     |
|------------------------|------------------------------------|-----------------------------|
| `status`               | —                                  | `State`                     |
| `list_profiles`        | —                                  | `{ profiles: Profile[] }`   |
| `import_subscription`  | `url`, `name`                      | `{ profile: Profile }`      |
| `import_link`          | `link`, `name?`                    | `{ profile: Profile }`      |
| `import_links`         | `links` (string[]), `name?`        | `{ profile: Profile, imported, skipped }` |
| `remove_profile`       | `profile`                          | —                           |
| `refresh_subscription` | `profile`                          | `{ profile: Profile }`      |
| `connect`              | `profile`, `node?`, `auto?`        | `State`                     |
| `disconnect`           | —                                  | `State`                     |
| `ping`                 | `profile`                          | `{ results: PingResult[] }` |
| `set_routing`          | `mode` (`smart`/`global`/`direct`) | `State`                     |
| `set_split`            | `mode` (`off`/`exclude`/`include`), `apps?` | `State`            |
| `set_kill_switch`      | `on` (boolean)                     | `State`                     |
| `set_tls_fragment`     | `on` (boolean)                     | `State`                     |
| `set_multihop`         | `profile`, `enabled` (boolean), `entry_id`, `exit_id` | `State`  |
| `set_tun`              | `stack` (`system`/`gvisor`/`mixed`) | `State`                    |
| `set_proxy_mode`       | `proxy_mode` (`tun`/`system-proxy`), `proxy_port?` (int) | `State` |
| `set_autoconnect`      | `on` (boolean)                     | `State`                     |
| `set_auto_failover`    | `on` (boolean)                     | `State`                     |
| `set_dns`              | `ad_block` (boolean), `dns_remote`, `dns_direct`, `ipv4_only` (boolean) | `State` |
| `set_rules`            | `rules_direct` (string[]), `rules_proxy` (string[]), `preset_ru_banking` (boolean), `preset_ru_gov` (boolean) | `State` |
| `set_presets`          | `games?` (boolean), `voice?` (boolean), `services?` (boolean) | `State` |
| `set_crash_reports`    | `on` (boolean)                     | `State`                     |
| `leak_check`           | —                                  | `LeakCheck`                 |
| `run_stun_check`       | —                                  | `StunCheck`                 |
| `run_speed_test`       | —                                  | `SpeedTest` (connected only) |
| `collect_diagnostics`  | —                                  | `SupportBundle`             |

```
request:  {"id":7,"cmd":"connect","profile":"p1","node":"n3"}
response: {"id":7,"ok":true,"data":{"state":"connecting","node":"n3"}}
error:    {"id":7,"ok":false,"error":"profile not found"}
```

### Node selection (`connect`)

`connect` chooses an exit one of three ways:

- with an explicit `node`, the core uses **exactly** that server and does not
  wander to another (an explicit exit is honoured as-is). `auto` is ignored.
- without a `node` and with `auto:true`, the core **pings every candidate** (a
  short, parallel TCP dial) and walks them **fastest first** by measured
  round-trip; candidates that fail the probe sort last but are still tried.
- without a `node` and with `auto` omitted/`false` (the default), the core walks
  candidates by **protocol preference** (REALITY-flavoured VLESS → Hysteria2 →
  AmneziaWG), leading with the profile's last-good node.

The **anti-DPI fallback is preserved in every mode**: if the leading candidate's
connectivity probe is blocked, the core advances to the next candidate in the
chosen order rather than failing. In `auto` mode, RTT is authoritative — a faster
server always leads — while the per-profile last-good node only breaks an exact
RTT tie and is still recorded on a successful connect for the protocol-fallback
path.

```
request:  {"id":7,"cmd":"connect","profile":"p1","auto":true}
response: {"id":7,"ok":true,"data":{"state":"connecting","profile":"p1"}}
```

### Changing the exit on a live tunnel

A `connect` naming a `node` while the tunnel is already up on that profile does
**not** rebuild the tunnel. Every node of the profile is already an outbound in
the running sing-box, behind one selector, so the core points that selector at
the requested node over the process's own loopback clash API: the sing-box
process, the tun device and its routes stay exactly as they are.

Connections already established are **not** cut. The selector is built with
`interrupt_exist_connections: false`, so an in-flight download, call or ssh
session finishes through the exit it was dialled through and only *new*
connections take the new node. (sing-box still interrupts its own internal dials,
which is what makes DNS follow the new exit immediately.)

The switch is confirmed before it is reported: the core probes through the newly
selected exit, and a probe that does not come up puts the previous exit back and
falls through to the ordinary reconnect. The **response says which happened**:

```
request:  {"id":8,"cmd":"connect","profile":"p1","node":"n3"}
response: {"id":8,"ok":true,"data":{"state":"connected","node":"n3"}}   // steered, nothing reconnected
response: {"id":8,"ok":true,"data":{"state":"connecting","node":"n3"}}  // could not steer; rebuilding
```

A steered switch emits no `connecting` state at all — only a `connected` state
naming the new node, an `attempts` snapshot holding that one node, and a log line.
A UI must not render "reconnecting" for it, because nothing reconnected.

The core falls back to the full reconnect whenever the running process cannot be
steered to the request: nothing connected, a different profile, a node the running
config never rendered (added by a subscription refresh since the connect), a
selector that does not carry it (which is what **multihop** looks like — the chain
collapses the selector onto the exit), or a clash API that refuses the selection.
The fallback is the behaviour every node change had before, so a switch that is
impossible costs the user a reconnect, never an error.

### Batch link import (`import_links`)

`import_links` collects several share links into **one** manual profile holding
all of them as servers — the convenience path for pasting a block of links or
loading a `.txt` list. `links` is an array of strings; each element may be a
single link or a multi-line block (the core splits on newlines), so a UI can pass
the raw textarea/file body as one entry.

Parsing is forgiving so one bad line never costs the user the good ones:

- surrounding whitespace is trimmed;
- blank lines and comments (a line starting with `#` or `//`) are ignored — they
  count as neither imported nor skipped;
- exact-duplicate links collapse to a single server (first occurrence wins,
  preserving order);
- a line that looks like a link but fails to parse is **skipped** (counted), not
  fatal.

The reply carries the new profile plus `imported` (servers added) and `skipped`
(links that failed to parse) so the UI can report "imported N, skipped M". A
batch with **no** parseable links is an error rather than an empty profile.

```
request:  {"id":7,"cmd":"import_links","links":["vless://…#a\ntrojan://…#b\nbad-line"],"name":"Mine"}
response: {"id":7,"ok":true,"data":{"profile":{ /* …two servers… */ },"imported":2,"skipped":1}}
```

`set_routing` and `set_split` only record the choice; like a routing change, a
new split takes effect on the **next connect** (live retuning would require
restarting sing-box). The returned `State` reflects the stored choice.

`set_kill_switch`, `set_tls_fragment`, `set_multihop`, `set_tun` and
`set_proxy_mode` go further: all are recorded and persisted the same way, but when
a tunnel is **live** the core also re-applies them in place — see below.

`set_autoconnect` is recorded and persisted the same way but changes nothing
about a live tunnel; it takes effect when the daemon itself next starts (see
[Autoconnect](#autoconnect-set_autoconnect)).

### Kill switch (`set_kill_switch`)

`on: true` arms the kill switch; `false` (or an omitted field) disarms it. Armed
means two things:

- the tun inbound is built with **`strict_route`**: sing-box installs firewall
  rules that drop any packet trying to route around the tunnel, so a dead
  upstream node black-holes traffic instead of leaking it onto the physical
  interface. The trade-off is a rougher connect (the rules are applied
  system-wide the moment the tunnel comes up), which is why this is opt-in;
- if the **tunnel process itself dies**, the core relaunches it immediately,
  pinned to the node that was up — strict_route only holds while sing-box runs,
  so putting the process (and its filter rules) back is the only honest
  mitigation. Relaunches are budgeted (up to 5 for a tunnel caught in a
  crash-loop) so one that dies on every start can't churn forever; past the
  budget the state degrades to `error` like any dropped tunnel. The budget counts
  only rapid, back-to-back deaths: it resets on an explicit connect/disconnect,
  and a relaunched tunnel that then stays connected for a while refunds it, so
  isolated drops across a long session never accumulate toward the cap.

Be honest with users about the limits: **while the process is down — the gap
before a relaunch lands, or after the budget is spent — the OS routes normally
and traffic is not blocked.** A guarantee across that window would need an
OS-level firewall hold owned by something that outlives sing-box; the protocol
does not promise it.

While the kill switch is armed the core also emits **no rule that pins traffic to
the direct outbound**: the games and voice presets, the domains handed to a
running DPI bypass, the RU banking / government presets and any `rules_direct`
suffixes, plus the LAN bypass. The same applies on the DNS side, so none of those
names is resolved by the direct resolver either. Proxy-direction rules are
untouched — dropping those would leave the domain to the geo split, which in
`smart` mode can send it direct, i.e. the kill switch causing the leak it exists
to prevent. Apps the user listed under `set_split` in `exclude` mode do stay
direct: that is a per-application choice they made one name at a time, unlike a
preset. Note the consequence of the LAN rule: with the kill switch armed, private
destinations (a router's admin page, a NAS, a printer) go into the tunnel and stop
answering.

### TLS fragmentation (`set_tls_fragment`)

`on: true` forces TLS ClientHello fragmentation on every TLS-bearing outbound;
`false` (or an omitted field) turns it off. Armed, the core emits sing-box's
`tls.fragment` (with an explicit `fragment_fallback_delay`) on each outbound that
carries TLS, splitting the ClientHello across TCP segments so DPI keying on the
plaintext SNI in a single first packet cannot match it. It is a transport-layer
reshaping only — the protocol, credentials and destination are untouched — and is
inert for non-TLS protocols (Shadowsocks, WireGuard) and for QUIC-based ones
(Hysteria2), where there is no TCP ClientHello to split.

Like the kill switch it re-applies to a **live** tunnel in place: the core
rebuilds the config for the node it is already on and hot-swaps sing-box, so
arming does not wait for a reconnect. It is independent of the adaptive walk,
which already reaches fragmentation per-node when a node's handshake looks
censored (the last rung of the transport-strategy cascade); this toggle is the
user's unconditional override for a network that blocks the SNI outright.

### Multihop (`set_multihop`)

`enabled: true` chains the proxy through **two** of the profile's nodes: traffic
egresses via the `entry_id` node first and then the `exit_id` node, so the exit
server sees the entry's address rather than the user's. `entry_id` and `exit_id`
are stable server ids (the same identifiers `connect`'s `node` takes) within
`profile`; enabling requires both, distinct, and present in that profile, or the
command is rejected whole. `enabled: false` turns the chain off but keeps the ids
recorded so the last pick can be re-armed.

Under the hood the core resolves the two ids to sing-box outbound tags and emits
the exit outbound with `detour` set to the entry tag, pointing the route's final
at the exit; a selection that no longer resolves (a vanished node, or one the
builder cannot render) degrades to an ordinary single hop rather than a broken
config. Like the kill switch it re-applies to a **live** tunnel in place by
hot-swapping sing-box on the current node, and is persisted in `settings.json`.

### Tun stack (`set_tun`)

`stack` selects the tun network stack: `system` (the kernel's own TCP/IP —
fastest, the default), `gvisor` (a userspace stack — slower, but immune to tun
driver quirks), or `mixed` (TCP on system, UDP on gvisor). An unknown value is
an error and nothing is recorded.

### System-proxy mode (`set_proxy_mode`)

`proxy_mode` selects how the tunnel captures traffic: `tun` (the default — a tun
device with `auto_route`, which needs the tun driver) or `system-proxy`. In
`system-proxy` mode the core builds **no** tun inbound at all; instead sing-box
exposes a single loopback **mixed** inbound (HTTP + SOCKS on `127.0.0.1:<port>`)
and the client points the OS at it as the system proxy. That path needs no tun
driver, service, or elevation — the mode for locked-down/corporate machines where
a tun is not permitted. `proxy_port` optionally overrides the loopback port
(default `2080`); `0`/omitted keeps the current port. An unknown mode or an
out-of-range port is an error and nothing is recorded.

The OS proxy is set the moment the tunnel comes up (the state does not report
`connected` until the proxy is armed) and is **cleared on every teardown** — an
explicit disconnect, a switch back to `tun`, a tunnel-process death, and daemon
shutdown — so the OS is never left pointing at a mixed inbound that is no longer
listening. Because that pointer is written into the OS (not owned by sing-box, the
way `strict_route` is), a hard kill of the core could still strand it; the core
therefore also **reconciles at startup**, clearing a proxy a previous run left at
exactly its own loopback address (never a remote/corporate proxy, and never one on
a different port). The kill switch has no effect in this mode — there is no
`strict_route` on a mixed inbound — so, like the process-down window above, traffic
is not held closed if the tunnel drops; the guard's job is to restore direct
connectivity, not to fail closed.

The mode and port are **persisted** in `settings.json` and load back into the
reported `State` (`proxy_mode`, `proxy_port`) on launch.

### Live re-apply

Both options are startup parameters of the tun inbound — sing-box cannot change
them on a running process. When either command lands while `connected`, the core
**hot-swaps** the tunnel: it rebuilds the config for the node it is already on
and restarts sing-box against it. This is deliberately *not* a full reconnect —
no fallback walk, no node re-selection, no ping ranking; the candidate set is
pinned to the current node. The UI sees the ordinary `connecting` → `connected`
dip while the swap-and-probe runs (typically a second or two); a failed probe
surfaces as an `error` state exactly like any dropped tunnel.

When nothing is connected, the commands just record the choice for the next
connect. If the live profile/node has meanwhile disappeared from the store, the
running tunnel is left untouched and the change is deferred to the next connect
(a settings toggle must never tear down a working tunnel without bringing one
back); a `log` event notes the deferral.

Both preferences are **persisted** in `settings.json` alongside the split
config, and load back into the reported `State` on launch.

```
request:  {"id":9,"cmd":"set_kill_switch","on":true}
response: {"id":9,"ok":true,"data":{"state":"connecting","profile":"p1","kill_switch":true,"tun_stack":"system"}}
request:  {"id":10,"cmd":"set_tun","stack":"gvisor"}
response: {"id":10,"ok":true,"data":{"state":"idle","tun_stack":"gvisor"}}
request:  {"id":11,"cmd":"set_proxy_mode","proxy_mode":"system-proxy"}
response: {"id":11,"ok":true,"data":{"state":"idle","proxy_mode":"system-proxy","proxy_port":2080}}
```

### Autoconnect (`set_autoconnect`)

`on: true` makes the core reconnect on its own the next time the **daemon**
starts; `false` (or an omitted field) turns that off. The preference is
persisted in `settings.json` like the kill switch and reported back as
`autoconnect` in `State`. Nothing about a live tunnel changes when the command
lands — it only matters at daemon startup.

What the core reconnects to is the **last successful user connect**, which it
records on its own: the profile, plus the node only when that connect named an
explicit one. A connect that let the core choose is re-issued the same way, so
the startup connect walks the ordinary fallback order (led by the profile's
last-good node) rather than pinning whatever exit happened to be up last.
Kill-switch relaunches and live re-applies do not rewrite this record — they
pin the current node as a mechanism, not as the user's intent.

Because the trigger is the daemon's start, the behaviour follows the
transport: a spawned sidecar autoconnects when the app launches (the old
UI-driven behaviour, now core-owned), while the Windows service autoconnects
at **system boot** — before any user logs in — and after service restarts,
e.g. across an update. A client that attaches mid-attempt simply observes the
`connecting` state. The attempt never delays the control plane, a user command
that arrives first wins, and a recorded profile or node that no longer exists
leaves the core idle (with a `log` event) rather than in `error` — it never
guesses a different exit than the user last chose.

```
request:  {"id":11,"cmd":"set_autoconnect","on":true}
response: {"id":11,"ok":true,"data":{"state":"idle","tun_stack":"system","autoconnect":true}}
```

### Health failover (`set_auto_failover`)

While `connected`, the core runs a watchdog that probes the active node through
the tunnel on an interval (a clash-API delay test through the selector — the same
in-tunnel reachability check a connect uses to confirm a node came up). When the
node misses several probes in a row, the core moves the user off it **on its own**.
No user action is involved; the new node is recorded as last-good like any connect.

It moves the exit the cheap way first. Candidate exits are measured *through the
running process* — each one's own outbound is asked to fetch the same set of
unalike control destinations `check_nodes` uses, and a candidate counts only if a
strict majority of them survive it — and the best one is then selected live, the
same seamless path a user-driven node change takes. The tunnel is never taken
down, so the session survives a degraded exit.

Only when that is impossible (a config that cannot be steered, a selection the API
refuses, or a candidate that does not carry traffic once selected) does the core
fall back to reconnecting: the ordinary fallback walk with the degraded node
excluded, announced with a one-shot `health_reconnecting` state naming the node
being left, followed by the usual `connecting` → `connected` sequence. A profile
with no other usable node has nowhere to go, so the core logs it and keeps the
(possibly recoverable) tunnel up rather than churning the same node.

Automatic switching is deliberately damped, so a bad local network cannot walk the
user around the whole node list:

- three consecutive failed probes (~75s at the default interval) before anything
  moves at all;
- at most one automatic switch every **3 minutes** — one move has to be given the
  chance to prove itself;
- at most **3** automatic switches in any **15-minute** window, after which the
  core says so in the log and stops moving: three exits that all failed to fix it
  is evidence the problem is not the exit;
- a node that ran out of health probes is passed over for **10 minutes**, so two
  flapping exits cannot hand the user back and forth.

A probe that succeeds clears the run of failures, and the 15-minute window slides,
so a session that settles recovers its full budget on its own.

`on: true` (the default) arms the watchdog; `false` disarms it. The preference is
persisted in `settings.json` and reported as `auto_failover` in `State`. Unlike
the kill switch it changes nothing about a live tunnel when it lands — the
watchdog re-reads the flag on its next tick, so a mid-session toggle takes effect
without a reconnect. It composes with the kill switch, which handles a different
failure (the sing-box **process** dying) by relaunching the same node; the
watchdog handles a node that stays up but stops carrying traffic.

```
request:  {"id":14,"cmd":"set_auto_failover","on":true}
response: {"id":14,"ok":true,"data":{"state":"idle","tun_stack":"system","auto_failover":true}}
```

### Crash reports (`set_crash_reports`)

`on: true` opts in to crash reporting, `false` opts out; the choice is persisted
in `settings.json` and reported back in `State`. Like autoconnect it changes
nothing about a live tunnel, and — unlike everything else here — it governs a
purely local behaviour: **the core never sends anything anywhere.** A GUI panic
or an uncaught webview error is always written to a local file (`crash-gui.txt`,
beside `core.log`); the consent only decides whether the app offers, after the
fact, to let the user review that file and open a pre-filled GitHub issue in
their browser. There is no telemetry and no network path.

Consent is a genuine tri-state so the UI can tell "not asked yet" from "off":
`crash_reports` is omitted until the user answers, then carries their explicit
`true`/`false`, and `crash_reports_asked` becomes `true` once they have (it is
omitted while false). An omitted `on` in the request decodes to `false`
(opt-out), matching the other toggles.

```
request:  {"id":12,"cmd":"set_crash_reports","on":true}
response: {"id":12,"ok":true,"data":{"state":"idle","tun_stack":"system","crash_reports":true,"crash_reports_asked":true}}
```

### DNS (`set_dns`)

Sets the DNS preferences in one command: `ad_block` toggles ad/tracker blocking,
`ipv4_only` pins the resolution strategy to IPv4 (A records only, no AAAA — this
helps when an IPv6-capable site misbehaves through a tunnel whose exit has no
IPv6), and `dns_remote` / `dns_direct` set the two resolvers — the encrypted one
reached over the proxy for general lookups, and the direct one for destinations
kept off the tunnel. All are persisted in `settings.json` and reported back in
`State` (`ad_block`, `ipv4_only`, `dns_remote`, `dns_direct`).

Like the kill switch, `set_dns` re-applies to a **live** tunnel in place: the core
rebuilds the config for the node it is already on and hot-swaps sing-box (a brief
`connecting → connected` dip on the same node), so a change lands without waiting
for the next connect. A resend that changes nothing does not restart the tunnel.

Each resolver accepts the schemes the DNS builder parses — `tls://`, `https://`,
`quic://`, `h3://`, `tcp://`, `udp://`, or a bare host, with an optional port and,
for DoH, a path. A **malformed** resolver is rejected (`ok: false`) before
anything is recorded. An **empty** resolver is accepted and falls back to the
core's default, so the reported `dns_remote` / `dns_direct` are always the
effective values (the UI can prefill its inputs from them).

When `ad_block` is on, the core injects a DNS rule that sinkholes lookups for a
bundled ad/tracker domain list (answered `REFUSED`), ahead of any routing rule, in
every mode. The blocklist ships strictly as a local rule-set, so it is inert on a
build that lacks the bundled file rather than fetching anything at runtime.

```
request:  {"id":12,"cmd":"set_dns","ad_block":true,"dns_remote":"tls://9.9.9.9","dns_direct":"","ipv4_only":true}
response: {"id":12,"ok":true,"data":{"state":"idle","tun_stack":"system","ad_block":true,"ipv4_only":true,"dns_remote":"tls://9.9.9.9","dns_direct":"https://77.88.8.8/dns-query"}}
```

### Custom rules (`set_rules`)

Sets the custom domain-suffix routing rules and the RU direct-rule presets in one
command: `rules_direct` pins matching destinations to the direct outbound,
`rules_proxy` sends them through the proxy, and `preset_ru_banking` /
`preset_ru_gov` add bundled lists of major Russian banking / government domains as
direct rules (those services often reject connections from a foreign address, so
keeping them off the tunnel is a split-routing convenience). All are persisted in
`settings.json` and reported back in `State` (`rules_direct`, `rules_proxy`,
`preset_ru_banking`, `preset_ru_gov`).

Each element is a bare domain suffix — ASCII letters, digits, dots and hyphens,
matched by suffix so it also covers subdomains (`sberbank.ru` matches
`online.sberbank.ru`). A **malformed** element (one carrying a scheme, slash,
port, whitespace or `@`) rejects the whole command (`ok: false`) before anything
is recorded. Suffixes are normalized server-side (trimmed, lowercased,
de-duplicated, sorted), so the `State` echoed back may differ from the input
order/casing.

The rules are emitted **after** per-app split tunnelling and **before** the
smart-mode RU geo split, so a per-app rule still wins and a user rule beats the RU
geo preset. Each rule gets a mirrored DNS rule (a direct-pinned domain resolves
via the direct resolver, a proxy-pinned one via the encrypted resolver), so a
domain's lookups follow its traffic. They are inert in `direct` routing mode,
where nothing is tunnelled.

Like the kill switch, `set_rules` re-applies to a **live** tunnel in place (a
brief `connecting → connected` dip on the same node); a resend that changes
nothing does not restart the tunnel.

```
request:  {"id":13,"cmd":"set_rules","rules_direct":["bank.example"],"rules_proxy":["work.example"],"preset_ru_banking":true,"preset_ru_gov":false}
response: {"id":13,"ok":true,"data":{"state":"idle","tun_stack":"system","dns_remote":"tls://1.1.1.1","dns_direct":"https://77.88.8.8/dns-query","rules_direct":["bank.example"],"rules_proxy":["work.example"],"preset_ru_banking":true}}
```

### Routing presets (`set_presets`)

Toggles the three bundled routing presets. Each field is **optional**, and an
omitted one leaves that preset alone — the three are independent switches on the
same screen, so a command that had to restate all of them to change one would
eventually restate one wrong.

| field      | preset | default | what it does |
| ---------- | ------ | ------- | ------------ |
| `services` | unblock services | **on** | Pins the commonly-censored domains (YouTube, Discord, Meta, X, the AI APIs) to the tunnel ahead of the geo split, so `googlevideo.com` resolving to an ISP cache node does not get the video sent direct. While a DPI bypass runs, the domains the bundle actually covers move to the direct path instead. |
| `games`    | games direct | off | Pins known game clients and launchers (`process_name`) to the direct outbound: no tunnel latency on a match, and no exit-address change for anti-cheat to flag. Every name is specific to one game or launcher — a generic one like `java.exe` would take unrelated programs out of the tunnel. |
| `voice`    | real-time UDP direct | off | Sends UDP ports 50000-65535 direct. Voice and game traffic stop paying the round trip (239ms tunnelled against 9ms direct, measured), **and the peer on the other end sees the ISP address rather than the exit node's** — the same range carries browser WebRTC and torrents. |

`games` and `voice` default **off** because each takes a whole class of traffic
out of the tunnel, which is a trade the user makes rather than one the daemon
makes for them; `services` defaults on because it only ever moves traffic *into*
the tunnel. All three are persisted in `settings.json` and reported in `State`
(`preset_games_direct`, `preset_voice_direct`, `preset_unblock_services`). All
three are inert in `direct` routing mode, and the two direct-pinning ones yield to
the kill switch.

Like the kill switch, `set_presets` re-applies to a **live** tunnel in place (a
brief `connecting → connected` dip on the same node); a resend that changes
nothing does not restart the tunnel. A command naming none of the three is a
caller bug and is refused.

```
request:  {"id":14,"cmd":"set_presets","voice":true}
response: {"id":14,"ok":true,"data":{"state":"idle","tun_stack":"system","preset_voice_direct":true,"preset_unblock_services":true}}
```

### Per-app split tunnelling (`set_split`)

`apps` is a list of executable file names matched case-insensitively against the
process that owns each connection, e.g. `["chrome.exe", "steam.exe"]`. Names are
normalized server-side (trimmed, lowercased, de-duplicated, sorted), so the
`State` echoed back may differ from the input order/casing.

- `off` — no split; the base routing `mode` decides everything. An `exclude`/
  `include` with an empty (or all-blank) `apps` list collapses to `off`.
- `exclude` — the listed apps go **direct** (out of the tunnel); everything else
  follows the normal routing for the current `mode`.
- `include` — only the listed apps go through the **proxy**; everything else goes
  direct.

The split config is **persisted** in the core's config directory
(`settings.json`, written atomically) so it survives a restart and is loaded
back into the reported `State` on launch.

```
request:  {"id":8,"cmd":"set_split","mode":"exclude","apps":["Chrome.exe","steam.exe"]}
response: {"id":8,"ok":true,"data":{"state":"idle","split":"exclude","split_apps":["chrome.exe","steam.exe"]}}
```

### Leak check (`leak_check`)

The core observes the machine's current public IP from redundant third-party echo
services (the first to answer wins, so one being blocked doesn't fail the check)
and runs a best-effort DNS probe, then assembles a verdict. It takes no fields.

The result is **honest about what it could not measure** and never reports a
false pass:

- `ip_verdict` is the headline severity. `ok` only when connected **and** the
  observed IP is the configured tunnel exit; `warn` when connected but the IP is
  clearly not the exit (a probable leak); `neutral` when idle (the IP is shown
  without a pass/fail claim) or when the exit could not be compared; `error` when
  no IP could be observed at all.
- `exit_match` is the IP-vs-exit comparison, present only when connected:
  `match`, `mismatch`, or `unknown`. A literal-IP exit is compared exactly; an
  exit configured as a **hostname is not resolved** here (resolving would itself
  go through the host resolver and muddy the result), so it yields `unknown`
  rather than a guess.
- `dns.status` is `ok`/`leak` only when the resolvers could be reasoned about;
  otherwise `inconclusive` (some signal, no confident call) or `unavailable` (the
  probe could not run). A full dnsleaktest-style flow is out of scope, so
  `inconclusive`/`unavailable` are the common outcomes — **neither is a pass**,
  and clients must not present them as "safe".

A meaningful exit-match result needs a live, connected tunnel; on an idle client
the check still returns a well-formed result (`connected:false`, a `neutral` or
`error` IP verdict, and an honest DNS status).

```
request:  {"id":9,"cmd":"leak_check"}
response: {"id":9,"ok":true,"data":{
  "public_ip":"203.0.113.7","country":"NL","source":"ipify",
  "connected":true,"exit_server":"203.0.113.7","exit_match":"match",
  "ip_verdict":"ok","ip_message":"Public IP 203.0.113.7 matches the tunnel exit.",
  "dns":{"status":"inconclusive","resolvers":["1.1.1.1"],
         "message":"Observed resolver(s) shown; reported as inconclusive rather than a pass."}
}}
```

### Network diagnostics (`run_stun_check`, `run_speed_test`)

Two probes that characterise the current network path. Both take no fields.

`run_stun_check` sends a minimal STUN Binding Request (RFC 5389) over UDP to two
public STUN servers from a single socket and reports:

- `udp_ok` — whether any server answered. `false` means outbound UDP (or at least
  STUN) looks blocked from this vantage.
- `external_ip` — the reflexive public IP a server observed for us, when one
  answered.
- `nat_type` — a best-effort NAT-mapping classification aimed at peer-to-peer
  reachability rather than the full RFC 3489 cone taxonomy: `open` (the reflexive
  address is one of our own interfaces — no NAT), `endpoint-independent` (both
  servers saw the same mapping — cone-like, P2P-friendly), `endpoint-dependent`
  (the servers saw different mappings — symmetric, P2P-hostile), `unknown` (only
  one server answered, too little to classify), or `blocked` (no answer).

```
request:  {"id":11,"cmd":"run_stun_check"}
response: {"id":11,"ok":true,"data":{"udp_ok":true,"nat_type":"endpoint-independent","external_ip":"203.0.113.7"}}
```

`run_speed_test` measures **download throughput through the active tunnel**: it
streams a sample from a neutral CDN endpoint and times it. It is gated on a live
connection — issued while idle it returns an error, since a throughput reading
off the tunnel would be meaningless. The result carries the rate in megabits per
second, the bytes the sample actually read, and how long that took.

```
request:  {"id":12,"cmd":"run_speed_test"}
response: {"id":12,"ok":true,"data":{"mbps":94.3,"sample_bytes":10485760,"duration_ms":890}}
error:    {"id":12,"ok":false,"error":"speed test requires an active connection"}
```

### Support bundle (`collect_diagnostics`)

Assembles one block of text describing the machine's current state, for a user to
save and attach to a bug report. It **probes nothing and sends nothing**: it
reports what the daemon already holds plus one interface enumeration, and the
caller decides what to do with the text.

The bundle carries the core, sing-box and bypass-bundle versions, the connection
state and every routing option in force, the stored profiles (names and node
counts — never their subscription URLs), the last fallback walk with its
per-candidate outcome, the machine's interfaces and default routes, the tail of
sing-box's output, and the tail of the log.

Everything in it is run through the same secret masking the desktop app applies
to its own copied diagnostics: managed-subscription tokens, share-link userinfo
and bare UUIDs are replaced with `***`, while hosts, ports, protocols and error
text are left readable. `filename` is a timestamped name to suggest; it carries
no path, because the core's own data directory is often not readable by the
person filing the report.

```
request:  {"id":13,"cmd":"collect_diagnostics"}
response: {"id":13,"ok":true,"data":{"text":"Tenebra core diagnostics
…","filename":"tenebra-diagnostics-20260824-011500.txt"}}
```

## Events

| event      | fields                                                              |
|------------|--------------------------------------------------------------------|
| `state`    | `state` (`idle`/`connecting`/`connected`/`error`/`health_reconnecting`), `node?`, `error?`|
| `traffic`  | `up`, `down` (bytes), `up_rate`, `down_rate` (bytes/s)             |
| `log`      | `level` (`debug`/`info`/`warn`/`error`), `msg`                     |
| `profiles` | none — signal that the stored profile set changed; re-run `list_profiles` |
| `attempts` | `items` (`Attempt[]`), `outcome` (`""`/`"ok"`/`"exhausted"`) — a fallback-walk snapshot |
| `pick_progress` | `strategy`, `ok`, `targets`, `index`, `total` — one step of a `pick_zapret` run |

`log` events are filtered by the daemon's level threshold, which defaults to
`info`: a shipped build never emits `debug`. Set `TENEBRA_LOG_LEVEL=debug` in the
core's environment and restart it to raise the threshold for a support session —
the same filter governs the process log file, so debug is off on disk too until
it is asked for.

The `profiles` event is emitted after a profile's stored data changes outside a
direct request — chiefly the background subscription auto-refresh — so the UI can
reload usage and node lists without polling. It is also emitted after a manual
`refresh_subscription` that changed anything.

```
{"event":"state","state":"connected","node":"n3"}
{"event":"traffic","up":10240,"down":51200,"up_rate":2048,"down_rate":8192}
{"event":"profiles"}
```

### Fallback attempts (`attempts`)

While a `connect` walks the anti-DPI fallback order (REALITY-flavoured VLESS →
Hysteria2 → AmneziaWG, led by the profile's last-good node — see [Node
selection](#node-selection-connect)), the core narrates the walk as `attempts`
events, one **full snapshot** per change. Each `item` is a candidate in the plan:
its `seq` (1-based position in the order it will be tried), the `protocol` and
`node` it targets (the same identifiers a `state` event carries), its `status`,
and whether it is the profile's `last_good` lead.

- The **first** snapshot goes out at the start of the walk with every candidate
  `waiting` — the order is already resolved, so the whole plan is known up front.
- A candidate flips to `trying` just before its process starts, then to `ok` (its
  connectivity probe succeeded) or `blocked` (it failed and the walk moved on).
- `outcome` is `""` while the walk runs, and the terminal snapshot carries `"ok"`
  (a candidate came up) or `"exhausted"` (every candidate failed).
- Two optional annotations narrate the **adaptive transport** walk. When a
  candidate's entry is reachable but its handshake is being dropped, the core
  re-tries the **same node** under a different transport strategy (a reshaped TLS
  handshake — fingerprint, SNI) before moving on. `strategy` names the non-default
  variation a candidate is being tried under or came up on; it is omitted while
  the candidate is on its own parameters. `reason` carries the failure
  classification when a candidate was abandoned because its handshake looked
  interfered with (`"censored"`); it is omitted for an ordinary block. Both are
  absent from the common, unadapted snapshot.

Because each event is the complete picture, a client only needs the latest one.
An **explicit-node** connect and a live **re-apply** hot-swap run the same walk
with a single candidate, so they emit a one-item snapshot (`waiting` → `trying` →
`ok`/`blocked`), keeping the UI's view uniform. A client that **attaches
mid-walk** is re-synced on its `status` request: while a walk is in flight the
core re-pushes the current snapshot, so a UI that connected late still sees it.
Snapshots from a walk that a newer connect superseded are dropped, never emitted
over the connection that replaced it.

```
{"event":"attempts","items":[{"seq":1,"protocol":"vless","node":"nl-ams-01","status":"blocked","last_good":true},{"seq":2,"protocol":"hysteria2","node":"fi-hel-01","status":"trying","last_good":false}],"outcome":""}
{"event":"attempts","items":[{"seq":1,"protocol":"vless","node":"nl-ams-01","status":"blocked","last_good":true},{"seq":2,"protocol":"hysteria2","node":"fi-hel-01","status":"ok","last_good":false}],"outcome":"ok"}
```

### Bypass strategy probe (`pick_progress`)

A `pick_zapret` run measures every strategy in the bundle: each one is attached,
probed against every destination and detached, which takes minutes. The core
narrates it as `pick_progress` events so a client can show the run advancing
instead of a control that has said "measuring" since the user pressed it.

- The **first** event of a run goes out before anything is measured: `total` is
  the number of strategies the run covers, `strategy` is empty and `index` is 0.
  The run measures the plain path first — the baseline every strategy is scored
  against — and that costs as much as a strategy does.
- Then **one event per strategy**, as it lands: `strategy` names it, `ok` is how
  many of the run's destinations it carried, and `index` is its 1-based place in
  the run.
- `targets` is the run's destination count and does not move. A strategy whose
  process never came up measures nothing, and reporting its own empty target list
  would put a `0/0` on screen mid-run.
- There is **no terminal event**: a step describes a position in a run, and the
  run is a synchronous command whose answer the caller is already waiting on. A
  client stops showing progress when `pick_zapret` returns — answer, error or
  refusal — rather than waiting for the core to say the run ended.

Nothing is stored: a client that attaches mid-run picks up from the next
strategy. The same lines also go out as `log` events, which is what the log view
and the diagnostics bundle keep after the run is over.

```
{"event":"pick_progress","strategy":"","ok":0,"targets":5,"index":0,"total":23}
{"event":"pick_progress","strategy":"general (ALT2)","ok":3,"targets":5,"index":7,"total":23}
```

## Types

```ts
type State = {
  state: "idle" | "connecting" | "connected" | "error" | "health_reconnecting";
  node?: string;
  profile?: string;
  routing?: "smart" | "global" | "direct";
  daemon_version?: string;        // the daemon build's release version; omitted by daemons predating 0.4.4 — read that as "older", not "current"
  split?: "exclude" | "include";  // omitted when off
  split_apps?: string[];          // normalized executable names; omitted when off
  kill_switch?: boolean;          // omitted when off
  tls_fragment?: boolean;         // forced TLS ClientHello fragmentation; omitted when off
  multihop?: {                    // two-hop chain selection; omitted until a pair is picked
    enabled: boolean;
    entry_id?: string;            // entry server id (first hop); omitted when unset
    exit_id?: string;             // exit server id (last hop); omitted when unset
  };
  tun_stack?: "system" | "gvisor" | "mixed";
  proxy_mode?: "tun" | "system-proxy";  // connection mode; tun by default
  proxy_port?: number;            // loopback mixed-inbound port in system-proxy mode (default 2080)
  autoconnect?: boolean;          // reconnect at daemon start; omitted when off
  auto_failover?: boolean;        // health watchdog: reconnect to another node when the active one degrades; on by default, omitted when off
  ad_block?: boolean;             // DNS ad/tracker blocking; omitted when off
  ipv4_only?: boolean;            // DNS strategy pinned to IPv4-only; omitted when off
  dns_remote?: string;            // effective encrypted resolver (over the proxy)
  dns_direct?: string;            // effective direct resolver
  rules_direct?: string[];        // custom domain suffixes pinned direct; omitted when empty
  rules_proxy?: string[];         // custom domain suffixes pinned through the tunnel; omitted when empty
  preset_ru_banking?: boolean;    // RU banking direct-rule preset; omitted when off
  preset_ru_gov?: boolean;        // RU government direct-rule preset; omitted when off
  preset_games_direct?: boolean;  // game clients kept off the tunnel; off by default, omitted when off
  preset_voice_direct?: boolean;  // real-time UDP kept off the tunnel; off by default, omitted when off
  preset_unblock_services?: boolean; // censored services pinned to the tunnel; on by default, omitted when off
  crash_reports?: boolean;        // crash-report consent; omitted until asked, then the choice
  crash_reports_asked?: boolean;  // whether consent has been answered; omitted while false
  error?: string;
};

type Node = {
  id: string;
  name: string;
  protocol: "vless" | "hysteria2" | "amneziawg" | "shadowsocks" | "trojan" | "vmess";
  server: string;
  port: number;
  insecure?: boolean;    // TLS cert verification is off (skip-cert-verify); omitted when on
};

type Profile = {
  id: string;
  name: string;
  source: "subscription" | "manual";
  url?: string;          // subscription URL, kept locally and never logged
  nodes: Node[];
  updatedAt: string;     // RFC3339
  expiresAt?: string;    // from the subscription user-info header
  trafficUsed?: number;  // bytes
  trafficTotal?: number; // bytes
  managed?: boolean;     // recognised as an operator-served subscription; drives a badge, omitted when false
  tier?: "premium" | "free"; // entitlement tier resolved for a managed subscription; UX only, omitted when unknown
};

type PingResult = { node: string; rttMs: number; ok: boolean };

type Attempt = {
  seq: number;        // 1-based position in the fallback order
  protocol: "vless" | "hysteria2" | "amneziawg" | "shadowsocks" | "trojan" | "vmess";
  node: string;       // node id, as in a state event's `node`
  status: "waiting" | "trying" | "blocked" | "ok";
  last_good: boolean; // the profile's last-good lead candidate
  strategy?: string;  // non-default transport strategy tried/connected under; omitted when native
  reason?: string;    // failure classification on a block ("censored"); omitted otherwise
};

// Body of an `attempts` event: a full snapshot of the current fallback walk.
type Attempts = { items: Attempt[]; outcome: "" | "ok" | "exhausted" };

// Body of a `pick_progress` event: one step of a strategy probe run.
type PickProgress = {
  strategy: string; // the strategy just measured; empty on the run's opening event
  ok: number;       // how many of the run's destinations it carried
  targets: number;  // the run's destination count; fixed for the whole run
  index: number;    // 1-based place in the run; 0 on the opening event
  total: number;    // strategies the run covers
};

type LeakCheck = {
  public_ip?: string;   // omitted if every echo endpoint failed
  country?: string;     // best-effort ISO 3166-1 alpha-2 for public_ip
  source?: string;      // the echo endpoint that answered
  connected: boolean;   // whether a tunnel was active at check time
  exit_server?: string; // the exit address compared against; present when connected
  exit_match?: "match" | "mismatch" | "unknown"; // omitted when idle
  ip_verdict: "ok" | "warn" | "neutral" | "error";
  ip_message: string;   // human summary of the IP finding
  dns: {
    status: "ok" | "leak" | "inconclusive" | "unavailable"; // last two are NOT a pass
    resolvers?: string[]; // observed resolver IPs, if any
    message: string;      // human summary of the DNS finding
  };
};
```

This contract is the boundary between `ui-desktop` and `core/control`.
