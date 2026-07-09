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

### How the desktop app chooses a transport

At startup the GUI probes the pipe once: if a core is listening it attaches
(and opens the session with the `status` re-sync above); if nothing is
listening it spawns the core as its stdio sidecar, exactly the pre-service
behaviour. When a pipe session ends mid-run — the service restarted, or
another client displaced this one — the GUI redials with capped exponential
backoff and re-syncs when it gets back in; it never falls back to a sidecar
mid-run, since the service owns the tunnel.

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

`TENEBRA_PIPE` overrides the probe: an alternate pipe name, or `off`
to force the sidecar (useful in development, where a running service would
otherwise capture the session meant for a freshly built core).

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

The listener claims the name with `FILE_FLAG_FIRST_PIPE_INSTANCE`, so if
something else already holds it the service fails loudly at start instead of
silently sharing the name. That flag does not stop an *already-admitted*
identity from adding instances to the bound name later (on pipes,
`GENERIC_WRITE` implies `FILE_CREATE_PIPE_INSTANCE`) — which is another face
of the same trust statement: interactive users are trusted with this control
surface.

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
| `set_tun`              | `stack` (`system`/`gvisor`/`mixed`) | `State`                    |
| `set_autoconnect`      | `on` (boolean)                     | `State`                     |
| `leak_check`           | —                                  | `LeakCheck`                 |

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

`set_kill_switch` and `set_tun` go further: both are recorded and persisted the
same way, but when a tunnel is **live** the core also re-applies them in place —
see below.

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

### Tun stack (`set_tun`)

`stack` selects the tun network stack: `system` (the kernel's own TCP/IP —
fastest, the default), `gvisor` (a userspace stack — slower, but immune to tun
driver quirks), or `mixed` (TCP on system, UDP on gvisor). An unknown value is
an error and nothing is recorded.

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

## Events

| event      | fields                                                              |
|------------|--------------------------------------------------------------------|
| `state`    | `state` (`idle`/`connecting`/`connected`/`error`), `node?`, `error?`|
| `traffic`  | `up`, `down` (bytes), `up_rate`, `down_rate` (bytes/s)             |
| `log`      | `level` (`info`/`warn`/`error`), `msg`                            |
| `profiles` | none — signal that the stored profile set changed; re-run `list_profiles` |

The `profiles` event is emitted after a profile's stored data changes outside a
direct request — chiefly the background subscription auto-refresh — so the UI can
reload usage and node lists without polling. It is also emitted after a manual
`refresh_subscription` that changed anything.

```
{"event":"state","state":"connected","node":"n3"}
{"event":"traffic","up":10240,"down":51200,"up_rate":2048,"down_rate":8192}
{"event":"profiles"}
```

## Types

```ts
type State = {
  state: "idle" | "connecting" | "connected" | "error";
  node?: string;
  profile?: string;
  routing?: "smart" | "global" | "direct";
  split?: "exclude" | "include";  // omitted when off
  split_apps?: string[];          // normalized executable names; omitted when off
  kill_switch?: boolean;          // omitted when off
  tun_stack?: "system" | "gvisor" | "mixed";
  autoconnect?: boolean;          // reconnect at daemon start; omitted when off
  error?: string;
};

type Node = {
  id: string;
  name: string;
  protocol: "vless" | "hysteria2" | "amneziawg" | "shadowsocks" | "trojan" | "vmess";
  server: string;
  port: number;
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
};

type PingResult = { node: string; rttMs: number; ok: boolean };

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
