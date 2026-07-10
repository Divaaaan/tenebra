package control

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Divaaaan/tenebra/core/fallback"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/core/subscription"
)

// Runner owns one sing-box process. It is defined here, not in an adapter
// package, so control depends on nothing OS-specific and the dependency arrow
// points adapter -> control. The daemon starts a config, polls byte counters,
// learns of an exit through Done, and stops the process.
//
// A Runner is single-use per Start/Stop cycle but reusable across cycles: after
// Stop (or a Done signal) a fresh Start must work. Implementations must be safe
// for concurrent calls to Stats/Done alongside Start/Stop, since the traffic
// poller and process watcher run in their own goroutines.
type Runner interface {
	// Start launches sing-box with the given config JSON. It returns once the
	// process has been spawned (not once the tunnel is up); a later failure is
	// reported through Done.
	Start(ctx context.Context, configJSON []byte) error
	// Stop terminates the process and waits for it to exit. It is idempotent.
	Stop() error
	// Stats returns cumulative upload/download byte counts from the clash API.
	// An error (e.g. the API not yet listening) is non-fatal to the daemon.
	Stats() (up, down int64, err error)
	// Probe runs a clash API delay test through the outbound named tag, returning
	// the measured round-trip in milliseconds. A nil error means traffic actually
	// flows through that outbound; any error (blocked protocol, dead upstream, API
	// not yet listening, ctx cancelled) means it does not. ctx bounds the call.
	Probe(ctx context.Context, tag string) (delayMs int, err error)
	// Done delivers the process's exit: it sends the exit error (nil on a clean
	// exit) once and is closed afterwards. Before any Start it must block.
	Done() <-chan error
}

// pingDialTimeout bounds each per-node TCP latency probe.
const pingDialTimeout = 3 * time.Second

// emitFunc is how the daemon pushes asynchronous events out to the writer. The
// server installs one; when nil (a bare daemon in a unit test) events are
// dropped.
type emitFunc func(name string, body any)

// Daemon holds all mutable connection state and implements every protocol
// command. It is driven by a Server (one request at a time) but its connection
// lifecycle also spawns goroutines (traffic poll, process watch) that mutate
// state, so every field touched from more than one goroutine is guarded by mu.
type Daemon struct {
	store  *profile.Store
	runner Runner

	mu      sync.Mutex
	routing routing.Options
	state   State
	tun     singbox.TunOptions

	// emit is set by the server via SetEmitter before serving; the daemon calls
	// it to publish state/traffic/log events. Guarded by mu.
	emit emitFunc

	// lastGood remembers the node that last connected per profile, feeding node
	// selection on a connect without an explicit node.
	lastGood fallback.LastGood

	// settings persists user routing preferences (the split config) so they
	// survive a restart. nil means no persistence (a bare daemon in a unit test):
	// preferences then live only for the session. Guarded by mu for writes via
	// persistSettings, but the store is itself concurrency-safe.
	settings settingsStore

	// autoconnect remembers whether the daemon reconnects the last profile when
	// it starts (see AutoconnectOnStart). Persisted with the other preferences.
	// Guarded by mu.
	autoconnect bool
	// lastProfile and lastNode record the last successful user-commanded
	// connect: the profile, and the node only when the request pinned an
	// explicit exit. They are what autoconnect re-issues at the next daemon
	// start. Off-command connects — the kill-switch relaunch, the options
	// reconcile, autoconnect itself — re-issue this intent rather than rewrite
	// it: a relaunch pins the node it was on, and recording that would silently
	// turn a "whole profile" intent into an explicit-node one. Guarded by mu.
	lastProfile string
	lastNode    string

	// cancel tears down the current connection's goroutines; nil when idle.
	cancel context.CancelFunc
	// watching guards against double-starting the lifecycle goroutines and lets
	// disconnect wait for them to drain.
	wg sync.WaitGroup
	// connMu serializes connection transitions (teardown + the start that follows)
	// so the two connects that run off the serialized command loop — the
	// kill-switch relaunch and the connecting-window options reconcile — cannot
	// interleave with a user connect/disconnect/Close. Held around startConnect and
	// teardown by every such path; never held while waiting on d.wg's goroutines
	// (they don't take it), so it introduces no deadlock. Always acquired before mu.
	connMu sync.Mutex
	// relaunchWG tracks in-flight off-command connect goroutines (relaunch /
	// reconcile) separately from d.wg, so Close can wait for them to unwind without
	// the self-deadlock that tracking them in d.wg would cause (their startConnect
	// waits on d.wg).
	relaunchWG sync.WaitGroup
	// generation increments on every connect so a stale watcher from a previous
	// connection can tell it has been superseded and stay quiet.
	generation uint64
	// relaunches counts kill-switch relaunches of a tunnel that keeps dying within
	// relaunchResetAfter of coming up (a crash-loop), so it can't churn forever.
	// Reset by an explicit connect or disconnect, and refunded when a relaunched
	// tunnel stays up past that window — so isolated drops over a long session
	// don't accumulate toward the cap. Guarded by mu.
	relaunches int
	// relaunchResetAfter is the uptime past which a relaunched tunnel's later death
	// refunds the relaunch budget rather than spending it. Injectable for tests;
	// defaults to defaultRelaunchReset.
	relaunchResetAfter time.Duration
	// beforeReconnect, when set, is called at the entry of an off-command connect
	// goroutine (relaunch / reconcile) before it claims connMu. It is a test seam
	// for driving the interleaving with a concurrent disconnect/Close; nil in
	// production.
	beforeReconnect func()

	// now and dial are injectable for tests; production uses the real clock and
	// dialer.
	now  func() time.Time
	dial func(ctx context.Context, network, address string) (net.Conn, error)

	// fallback-loop timings, injectable so tests run fast and deterministically.
	// probeWarmup is how long to wait after Start before the first probe (the
	// clash API needs a moment to listen). probeRetry is the gap between probe
	// retries within one attempt's budget. probeTimeout bounds a single probe.
	// probeBudget caps the total time spent probing one candidate before it is
	// declared failed.
	probeWarmup  time.Duration
	probeRetry   time.Duration
	probeTimeout time.Duration
	probeBudget  time.Duration

	// fetch retrieves a subscription body. It is injectable so the auto-refresh
	// logic can be unit-tested offline; production uses subscription.Fetch.
	fetch func(ctx context.Context, url string) ([]byte, http.Header, error)

	// httpGet performs a plain HTTP GET for the leak check's IP and DNS echo
	// requests. It is a separate, header-less getter from fetch so leak_check can
	// be unit-tested offline with canned responses; production uses
	// defaultHTTPGet. ipEchoes and dnsEchoURL are likewise injectable so a test
	// can point the check at fake URLs.
	httpGet    func(ctx context.Context, url string) ([]byte, error)
	ipEchoes   []ipEcho
	dnsEchoURL string
}

// NewDaemon builds a Daemon over a profile store and runner. Routing defaults to
// smart with normalized DNS; the tun options default to the singbox defaults,
// with the stack pinned explicitly so the reported state always names it.
func NewDaemon(store *profile.Store, runner Runner) *Daemon {
	d := &Daemon{
		store:   store,
		runner:  runner,
		routing: routing.Options{Mode: routing.ModeSmart}.Normalize(),
		// CacheDir pins sing-box's cache file to the writable store directory so
		// the root launchd daemon (cwd "/", read-only) doesn't abort at startup;
		// see singbox.TunOptions.CacheDir.
		tun: singbox.TunOptions{Stack: singbox.StackSystem, CacheDir: store.Dir()},
		state: State{
			State:    StateIdle,
			Routing:  string(routing.ModeSmart),
			TunStack: singbox.StackSystem,
			// Report the effective resolvers from the moment the daemon exists, so a
			// status before SetSettings still lets the UI prefill the custom-DNS
			// inputs. These match the normalized routing defaults above.
			DNSRemote: routing.DefaultDNSRemote,
			DNSDirect: routing.DefaultDNSDirect,
		},
		lastGood: fallback.NewMemLastGood(),
		now:      time.Now,
		fetch:    subscription.Fetch,

		httpGet:    defaultHTTPGet,
		ipEchoes:   defaultIPEchoes,
		dnsEchoURL: dnsEchoURL,

		probeWarmup:  defaultProbeWarmup,
		probeRetry:   defaultProbeRetry,
		probeTimeout: defaultProbeTimeout,
		probeBudget:  defaultProbeBudget,

		relaunchResetAfter: defaultRelaunchReset,
	}
	// The ping dial is pinned to the physical default interface (see
	// newPingDialer): while the tunnel is up with auto_route it would otherwise be
	// captured by the tun and every node would report the ~1-2ms latency to the
	// local tun rather than the real server. Binding the socket to the physical NIC
	// steers each probe past the tun so the RTT readout stays meaningful mid-session.
	d.dial = newPingDialer().DialContext
	return d
}

// SetEmitter installs the event sink. The server calls this before serving.
func (d *Daemon) SetEmitter(emit emitFunc) {
	d.mu.Lock()
	d.emit = emit
	d.mu.Unlock()
}

// SetLastGood swaps in a different last-good store, replacing the in-memory
// default. main wires the disk-backed store here so the last working node leads
// the fallback walk on the next launch; tests keep the in-memory one. Call it
// before serving — it is not synchronised against an in-flight connect.
func (d *Daemon) SetLastGood(lg fallback.LastGood) {
	d.lastGood = lg
}

// SetSettings installs a persistent settings store and immediately loads any
// saved preferences (the split config, the kill switch, the tun stack, the
// autoconnect preference and its last-connect target) into the live options and
// the reported state. main wires the disk-backed store here so the choices
// survive a restart; tests can install a fake or skip it. Call it before
// serving — it is not synchronised against an in-flight connect. Loading never
// starts a connection; that is AutoconnectOnStart's, and only its, job.
func (d *Daemon) SetSettings(store settingsStore) {
	d.settings = store
	ps := store.Load()

	d.mu.Lock()
	mode, apps := splitFromSettings(ps)
	d.routing.SplitMode = mode
	d.routing.SplitApps = apps
	d.routing.KillSwitch = ps.KillSwitch
	d.routing.AdBlock = ps.AdBlock
	// Empty resolvers (absent in an old file, or never customized) are left for
	// Normalize to fill with the defaults.
	d.routing.DNSRemote = ps.DNSRemote
	d.routing.DNSDirect = ps.DNSDirect
	d.routing = d.routing.Normalize()
	// A stack value from a corrupt or hand-edited file must not reach sing-box;
	// anything unknown keeps the default.
	if singbox.ValidStack(ps.TunStack) {
		d.tun.Stack = ps.TunStack
	}
	d.autoconnect = ps.Autoconnect
	d.lastProfile = ps.LastProfile
	d.lastNode = ps.LastNode
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect)
	d.mu.Unlock()
}

// SetRuleSetDir points the routing layer at a directory holding the bundled RU
// rule-set binaries (geoip-ru.srs / geosite-ru.srs) so smart-mode configs load
// them locally instead of downloading them from GitHub at startup — the latter
// blocks sing-box for ~10s (and can FATAL) when raw.githubusercontent.com is
// throttled. main passes the resources dir here only after confirming both
// files exist; an empty dir leaves the remote fallback in place. Call it before
// serving — it is not synchronised against an in-flight connect.
func (d *Daemon) SetRuleSetDir(dir string) {
	d.mu.Lock()
	d.routing.RuleSetDir = dir
	d.mu.Unlock()
}

// persistSettings saves the user preferences (split, kill switch, tun stack,
// autoconnect, the last-connect target) through the settings store, if one is
// installed. It snapshots the live values under mu itself, so the settings file
// always carries a coherent view even when a set command and the connect loop's
// last-connect recording race. Persistence is best-effort: a write failure is
// logged via the daemon's log event but never fails the originating command,
// mirroring how last-good tolerates a failed write.
func (d *Daemon) persistSettings() {
	if d.settings == nil {
		return
	}
	d.mu.Lock()
	ps := d.settingsLocked()
	d.mu.Unlock()
	if err := d.settings.Save(ps); err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("persist settings: %v", err))
	}
}

// settingsLocked projects the persisted preferences out of the daemon's live
// options. Callers must hold d.mu.
func (d *Daemon) settingsLocked() persistedSettings {
	return persistedSettings{
		Version:     settingsVersion,
		SplitMode:   string(d.routing.SplitMode),
		SplitApps:   d.routing.SplitApps,
		KillSwitch:  d.routing.KillSwitch,
		TunStack:    d.tun.Stack,
		Autoconnect: d.autoconnect,
		AdBlock:     d.routing.AdBlock,
		DNSRemote:   d.routing.DNSRemote,
		DNSDirect:   d.routing.DNSDirect,
		LastProfile: d.lastProfile,
		LastNode:    d.lastNode,
	}
}

// Handle dispatches one request to its command handler and returns the response
// to send. Unknown commands produce an error response rather than a transport
// failure. ctx bounds any network work the command performs (subscription
// fetch, ping).
func (d *Daemon) Handle(ctx context.Context, req Request) Response {
	switch req.Cmd {
	case CmdStatus:
		return d.handleStatus(req)
	case CmdListProfiles:
		return d.handleListProfiles(req)
	case CmdImportSubscription:
		return d.handleImportSubscription(ctx, req)
	case CmdImportLink:
		return d.handleImportLink(req)
	case CmdImportLinks:
		return d.handleImportLinks(req)
	case CmdRemoveProfile:
		return d.handleRemoveProfile(req)
	case CmdRefreshSub:
		return d.handleRefreshSubscription(ctx, req)
	case CmdConnect:
		return d.handleConnect(ctx, req)
	case CmdDisconnect:
		return d.handleDisconnect(req)
	case CmdPing:
		return d.handlePing(ctx, req)
	case CmdSetRouting:
		return d.handleSetRouting(req)
	case CmdSetSplit:
		return d.handleSetSplit(req)
	case CmdSetKillSwitch:
		return d.handleSetKillSwitch(req)
	case CmdSetTun:
		return d.handleSetTun(req)
	case CmdSetAutoconnect:
		return d.handleSetAutoconnect(req)
	case CmdSetDNS:
		return d.handleSetDNS(req)
	case CmdLeakCheck:
		return d.handleLeakCheck(ctx, req)
	default:
		return newError(req.ID, fmt.Sprintf("unknown command %q", req.Cmd))
	}
}

// snapshotState returns a copy of the current state under lock.
func (d *Daemon) snapshotState() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// snapshotRouting returns a copy of the live routing options under lock. The
// options are a value type, but SplitApps is a shared slice, so callers that
// might mutate it should copy first; current readers only inspect it.
func (d *Daemon) snapshotRouting() routing.Options {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.routing
}

// handleStatus answers a status command with the current connection snapshot.
func (d *Daemon) handleStatus(req Request) Response {
	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// redactedNode is the subset of a stored server the UI renders: its stable ID
// and the non-secret display fields. It deliberately drops every credential a
// model.Node carries — the VLESS/VMess UUID, the Trojan/Shadowsocks/Hysteria2
// password, the Shadowsocks method and obfs secret, the (Amnezia)WG private and
// pre-shared keys, and the REALITY keys — because the control socket is readable
// by any local user (the deliberate machine-wide-tunnel trust; see
// docs/control-protocol.md), so serialising the full node here would hand every
// secret in the store to any unprivileged process. The fields kept are exactly
// those the desktop Node type consumes (id, protocol, name, server, port).
type redactedNode struct {
	ID       string         `json:"id"`
	Protocol model.Protocol `json:"protocol"`
	Name     string         `json:"name"`
	Server   string         `json:"server"`
	Port     int            `json:"port"`
}

// redactedProfile mirrors profile.Profile minus the two secret-bearing parts:
// the subscription URL — whose path or query embeds the account token — and each
// node's credentials (see redactedNode). The URL is dropped whole rather than
// masked in place: the UI never renders it (it drives the refresh affordance off
// Source, not URL presence), and a token can sit in a path segment, a query
// parameter, or a fragment, so any partial mask risks leaking one it did not
// anticipate. Everything the UI does render — the ID, name, source, refresh
// timestamp, and the subscription quota/expiry — is preserved verbatim.
type redactedProfile struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Source       string         `json:"source"`
	Nodes        []redactedNode `json:"nodes"`
	UpdatedAt    time.Time      `json:"updatedAt"`
	ExpiresAt    *time.Time     `json:"expiresAt,omitempty"`
	TrafficUsed  int64          `json:"trafficUsed,omitempty"`
	TrafficTotal int64          `json:"trafficTotal,omitempty"`
}

// redactProfile projects one stored profile onto its wire-safe view, copying
// only the non-secret fields. The connect path never goes through here — it
// reads the full stored profile straight from the store — so redacting the
// outbound view costs the UI nothing.
func redactProfile(p profile.Profile) redactedProfile {
	nodes := make([]redactedNode, len(p.Servers))
	for i, s := range p.Servers {
		nodes[i] = redactedNode{
			ID:       s.ID,
			Protocol: s.Protocol,
			Name:     s.Name,
			Server:   s.Server,
			Port:     s.Port,
		}
	}
	return redactedProfile{
		ID:           p.ID,
		Name:         p.Name,
		Source:       p.Source,
		Nodes:        nodes,
		UpdatedAt:    p.UpdatedAt,
		ExpiresAt:    p.ExpiresAt,
		TrafficUsed:  p.TrafficUsed,
		TrafficTotal: p.TrafficTotal,
	}
}

// redactProfiles maps a stored profile list to the wire-safe view, always
// returning a non-nil slice so the UI receives [] rather than null.
func redactProfiles(ps []profile.Profile) []redactedProfile {
	out := make([]redactedProfile, len(ps))
	for i, p := range ps {
		out[i] = redactProfile(p)
	}
	return out
}

// handleListProfiles returns every stored profile in the redacted wire view —
// display fields only, no subscription token, no node credentials (see
// redactedProfile) — normalising a nil slice to [] so the UI always receives an
// array.
func (d *Daemon) handleListProfiles(req Request) Response {
	out := struct {
		Profiles []redactedProfile `json:"profiles"`
	}{Profiles: redactProfiles(d.store.List())}
	resp, err := newResult(req.ID, out)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleImportSubscription fetches a subscription URL, parses its nodes into a
// new profile, applies any Subscription-Userinfo quota, and stores it.
func (d *Daemon) handleImportSubscription(ctx context.Context, req Request) Response {
	if req.URL == "" {
		return newError(req.ID, "import_subscription: missing url")
	}
	name := req.Name
	if name == "" {
		name = req.URL
	}
	body, header, err := d.fetch(ctx, req.URL)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	nodes, _, err := subscription.ParseSubscription(body)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	if len(nodes) == 0 {
		return newError(req.ID, "import_subscription: no usable nodes in subscription")
	}

	p, err := profile.NewProfile(name, profile.SourceSubscription, req.URL, nodes)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	applyUserInfo(&p, header.Get("Subscription-Userinfo"))

	if err := d.store.Add(p); err != nil {
		return newError(req.ID, err.Error())
	}
	return d.profileResult(req.ID, p)
}

// handleImportLink parses a single share link into a one-node manual profile,
// naming it from the request, the node's own label, or a generic fallback.
func (d *Daemon) handleImportLink(req Request) Response {
	if req.Link == "" {
		return newError(req.ID, "import_link: missing link")
	}
	node, err := subscription.ParseLink(req.Link)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	name := req.Name
	if name == "" {
		// Fall back to the node's own label, then a generic one.
		if node.Name != "" {
			name = node.Name
		} else {
			name = "Imported link"
		}
	}
	p, err := profile.NewProfile(name, profile.SourceManual, "", []model.Node{node})
	if err != nil {
		return newError(req.ID, err.Error())
	}
	if err := d.store.Add(p); err != nil {
		return newError(req.ID, err.Error())
	}
	return d.profileResult(req.ID, p)
}

// handleImportLinks parses a batch of share links into a single multi-node
// manual profile. It is the convenience path for pasting several links at once
// or loading a .txt list: every valid link becomes one server in the same
// profile, unparseable lines are skipped (and counted) rather than aborting, and
// duplicates/blanks/comments are dropped by ParseLinks. The reply carries the new
// profile plus how many links were imported and skipped, so the UI can report
// "imported N, skipped M".
func (d *Daemon) handleImportLinks(req Request) Response {
	if len(req.Links) == 0 {
		return newError(req.ID, "import_links: missing links")
	}
	nodes, skipped := subscription.ParseLinks(req.Links)
	if len(nodes) == 0 {
		// Nothing parsed: surface a clear failure (and how many were skipped)
		// instead of creating an empty profile the user can't connect to.
		return newError(req.ID, fmt.Sprintf("import_links: no valid links found (%d skipped)", skipped))
	}
	name := req.Name
	if name == "" {
		name = "Imported links"
	}
	p, err := profile.NewProfile(name, profile.SourceManual, "", nodes)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	if err := d.store.Add(p); err != nil {
		return newError(req.ID, err.Error())
	}
	return d.importLinksResult(req.ID, p, len(nodes), skipped)
}

// importLinksResult wraps a batch import into the {profile, imported, skipped}
// response shape. imported is the number of servers added (equal to the profile's
// server count) and skipped the number of links that failed to parse. The profile
// is redacted (see redactProfile) before it goes on the wire: this reply travels
// the same local socket as list_profiles, so it must not carry the node
// credentials or a token-bearing URL either.
func (d *Daemon) importLinksResult(id int64, p profile.Profile, imported, skipped int) Response {
	out := struct {
		Profile  redactedProfile `json:"profile"`
		Imported int             `json:"imported"`
		Skipped  int             `json:"skipped"`
	}{Profile: redactProfile(p), Imported: imported, Skipped: skipped}
	resp, err := newResult(id, out)
	if err != nil {
		return newError(id, err.Error())
	}
	return resp
}

// handleRemoveProfile deletes a profile, first tearing down the tunnel if it is
// the one currently connected so no orphaned process outlives it.
func (d *Daemon) handleRemoveProfile(req Request) Response {
	if req.Profile == "" {
		return newError(req.ID, "remove_profile: missing profile")
	}
	// If we're connected to this profile, tear the tunnel down first so we don't
	// leave an orphaned process bound to a profile that no longer exists. Under
	// connMu so the transition is authoritative over an in-flight relaunch/reconcile.
	if cur := d.snapshotState(); cur.Profile == req.Profile && cur.State != StateIdle {
		d.connMu.Lock()
		d.teardown(StateIdle, "", "")
		d.connMu.Unlock()
	}
	if err := d.store.Remove(req.Profile); err != nil {
		return newError(req.ID, err.Error())
	}
	return newResult0(req.ID)
}

// handleRefreshSubscription re-fetches one subscription profile's nodes from its
// source URL. It rejects manual profiles, which have no URL to refresh from.
func (d *Daemon) handleRefreshSubscription(ctx context.Context, req Request) Response {
	if req.Profile == "" {
		return newError(req.ID, "refresh_subscription: missing profile")
	}
	p, ok := d.store.Get(req.Profile)
	if !ok {
		return newError(req.ID, profile.ErrNotFound.Error())
	}
	if p.Source != profile.SourceSubscription || p.URL == "" {
		return newError(req.ID, "refresh_subscription: profile is not a subscription")
	}

	updated, _, err := d.refreshProfile(ctx, p)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	// A manual refresh changed stored data (servers and/or usage); tell the UI so
	// any other view of the profile list updates without a poll.
	d.emitProfiles()
	return d.profileResult(req.ID, updated)
}

// refreshProfile fetches and re-parses one subscription profile, grafts the
// fresh content onto the existing profile so its ID and creation identity
// persist, folds in the latest usage from the Subscription-Userinfo header, and
// writes it back through the store. It returns the updated profile and whether
// the stored profile actually changed (so callers can decide whether to signal
// the UI). The profile must be a subscription with a non-empty URL; callers
// guarantee that.
func (d *Daemon) refreshProfile(ctx context.Context, p profile.Profile) (profile.Profile, bool, error) {
	body, header, err := d.fetch(ctx, p.URL)
	if err != nil {
		return profile.Profile{}, false, err
	}
	nodes, _, err := subscription.ParseSubscription(body)
	if err != nil {
		return profile.Profile{}, false, err
	}
	if len(nodes) == 0 {
		return profile.Profile{}, false, fmt.Errorf("refresh: no usable nodes in subscription %q", p.Name)
	}

	// Rebuild a profile to get fresh, stably-IDed servers, then graft the new
	// content onto the existing profile so its ID and creation identity persist.
	rebuilt, err := profile.NewProfile(p.Name, profile.SourceSubscription, p.URL, nodes)
	if err != nil {
		return profile.Profile{}, false, err
	}
	before := p
	p.Servers = rebuilt.Servers
	p.UpdatedAt = rebuilt.UpdatedAt
	// Only refresh traffic/expiry when this response actually carries a user-info
	// header. A refresh that returns the node list but no Subscription-Userinfo
	// (some panels send it only intermittently) must preserve the known quota and
	// expiry rather than silently zeroing them — the 6h background sweep would
	// otherwise wipe them. applyUserInfo no-ops on a blank header, so guarding the
	// overwrite here is what keeps the prior values.
	if ui := header.Get("Subscription-Userinfo"); ui != "" {
		p.ExpiresAt = nil
		p.TrafficUsed = 0
		p.TrafficTotal = 0
		applyUserInfo(&p, ui)
	}

	if err := d.store.Update(p); err != nil {
		return profile.Profile{}, false, err
	}
	return p, profileChanged(before, p), nil
}

// refreshAllSubscriptions re-fetches every subscription profile that has a URL,
// reusing the same refresh path as the manual command. A fetch or parse failure
// on one profile is logged and skipped — it never aborts the sweep or drops the
// profile's existing data. It returns whether any stored profile changed, so the
// caller can emit a single profiles event for the batch. With no subscription
// profiles it returns false without doing any work.
func (d *Daemon) refreshAllSubscriptions(ctx context.Context) bool {
	changedAny := false
	for _, p := range d.store.List() {
		if p.Source != profile.SourceSubscription || p.URL == "" {
			continue
		}
		_, changed, err := d.refreshProfile(ctx, p)
		if err != nil {
			d.emitLog(LogWarn, fmt.Sprintf("auto-refresh of %q failed: %v", p.Name, err))
			continue
		}
		if changed {
			changedAny = true
		}
	}
	return changedAny
}

// profileChanged reports whether a refresh produced a profile materially
// different from the one before it, ignoring the UpdatedAt timestamp (which a
// refresh always bumps). It drives whether the UI is told to reload: an
// identical subscription should not churn the list.
func profileChanged(before, after profile.Profile) bool {
	if before.TrafficUsed != after.TrafficUsed ||
		before.TrafficTotal != after.TrafficTotal {
		return true
	}
	if (before.ExpiresAt == nil) != (after.ExpiresAt == nil) {
		return true
	}
	if before.ExpiresAt != nil && !before.ExpiresAt.Equal(*after.ExpiresAt) {
		return true
	}
	if len(before.Servers) != len(after.Servers) {
		return true
	}
	for i := range before.Servers {
		if before.Servers[i].ID != after.Servers[i].ID {
			return true
		}
	}
	return false
}

// handleSetRouting validates and stores the routing mode (smart/global/direct).
// Like set_split it does not retune a live tunnel; the mode applies on the next
// connect, and the choice is reflected back so the UI stays in sync.
func (d *Daemon) handleSetRouting(req Request) Response {
	mode := routing.Mode(req.Mode)
	switch mode {
	case routing.ModeSmart, routing.ModeGlobal, routing.ModeDirect:
	default:
		return newError(req.ID, fmt.Sprintf("set_routing: unknown mode %q", req.Mode))
	}

	d.mu.Lock()
	d.routing.Mode = mode
	d.routing = d.routing.Normalize()
	d.state.Routing = string(mode)
	cur := d.state
	d.mu.Unlock()

	// A routing change does not retune a live tunnel in this iteration; the new
	// mode applies on the next connect. Report it so the UI reflects the choice.
	// (Reconfiguring sing-box live would mean a restart; deferred deliberately.)
	resp, err := newResult(req.ID, cur)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetSplit updates the per-app split configuration. Like set_routing it
// only stores the new config and reflects it in the reported state; the change
// takes effect on the next connect (live retuning would require restarting
// sing-box). The normalized config is persisted so it survives a restart.
func (d *Daemon) handleSetSplit(req Request) Response {
	mode := routing.SplitMode(req.Mode)
	switch mode {
	case routing.SplitOff, routing.SplitExclude, routing.SplitInclude:
	default:
		return newError(req.ID, fmt.Sprintf("set_split: unknown mode %q", req.Mode))
	}

	d.mu.Lock()
	d.routing.SplitMode = mode
	d.routing.SplitApps = req.Apps
	d.routing = d.routing.Normalize()
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect)
	cur := d.state
	d.mu.Unlock()

	d.persistSettings()

	resp, err := newResult(req.ID, cur)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetKillSwitch arms or disarms the kill switch. The choice is recorded,
// persisted, and — unlike set_routing/set_split — applied to a live tunnel in
// place: the daemon rebuilds the config for the node it is already on and
// hot-swaps the sing-box process (see reapplyLive), so arming doesn't wait for
// the user to reconnect. Armed means strict_route on the tun (sing-box installs
// filter rules that drop any packet trying to escape the tunnel) plus an
// automatic relaunch if the tunnel process itself dies (see watchProcess).
func (d *Daemon) handleSetKillSwitch(req Request) Response {
	d.mu.Lock()
	changed := d.routing.KillSwitch != req.On
	d.routing.KillSwitch = req.On
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetTun switches the tun network stack (system/gvisor/mixed). Recorded,
// persisted, and applied to a live tunnel in place via reapplyLive — the stack
// is a startup option of the tun inbound, so the swap restarts sing-box on the
// same node rather than walking the whole fallback order again.
func (d *Daemon) handleSetTun(req Request) Response {
	if !singbox.ValidStack(req.Stack) {
		return newError(req.ID, fmt.Sprintf("set_tun: unknown stack %q", req.Stack))
	}

	d.mu.Lock()
	changed := d.tun.Stack != req.Stack
	d.tun.Stack = req.Stack
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetAutoconnect arms or disarms connect-on-start. The choice is
// recorded, persisted with the other preferences, and reported back in the
// state. Unlike the kill switch it changes nothing about a live tunnel: the
// preference only matters when the daemon itself next starts (see
// AutoconnectOnStart), so there is no live re-apply.
func (d *Daemon) handleSetAutoconnect(req Request) Response {
	d.mu.Lock()
	d.autoconnect = req.On
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect)
	d.mu.Unlock()

	d.persistSettings()

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetDNS records the DNS preferences: the ad/tracker-block toggle and the
// two custom resolvers. Like the kill switch it applies to a live tunnel in place
// via reapplyLive — all three feed the generated dns block, so "apply now" means a
// hot-swap on the same node rather than waiting for the next connect. A resolver
// that fails validation rejects the whole command (nothing is recorded) so a typo
// can't half-apply or reach sing-box; an empty resolver is accepted and Normalize
// substitutes the default. The normalized choice is persisted so it survives a
// restart, and reflected back so the UI stays in sync.
func (d *Daemon) handleSetDNS(req Request) Response {
	if !routing.ValidDNSServer(req.DNSRemote) {
		return newError(req.ID, fmt.Sprintf("set_dns: invalid remote resolver %q", req.DNSRemote))
	}
	if !routing.ValidDNSServer(req.DNSDirect) {
		return newError(req.ID, fmt.Sprintf("set_dns: invalid direct resolver %q", req.DNSDirect))
	}

	d.mu.Lock()
	// d.routing is always kept normalized, so "before" already holds the effective
	// (default-substituted) resolvers to compare the new choice against.
	before := d.routing
	d.routing.AdBlock = req.AdBlock
	d.routing.DNSRemote = req.DNSRemote
	d.routing.DNSDirect = req.DNSDirect
	// Normalize turns an empty resolver back into the default, so the recorded and
	// reported values are always the effective ones — and so clearing an
	// already-default field to "" compares equal below and is correctly a no-op.
	d.routing = d.routing.Normalize()
	changed := dnsPrefsDiffer(before, d.routing)
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// dnsPrefsDiffer reports whether the DNS-affecting preferences (ad-block and the
// two resolvers) differ between two option snapshots. It gates the live hot-swap
// so only a real change restarts sing-box.
func dnsPrefsDiffer(a, b routing.Options) bool {
	return a.AdBlock != b.AdBlock || a.DNSRemote != b.DNSRemote || a.DNSDirect != b.DNSDirect
}

// applySettingsToState mirrors the daemon-wide preferences onto a State: the
// normalized split config, the kill switch, the tun stack, the autoconnect
// preference, and the DNS choices (ad-block plus the two resolvers). Split off
// collapses to empty fields so the wire form omits them, keeping a no-op split
// invisible to the UI; the app slice is copied so the State never aliases the
// daemon's live routing options. The resolvers are always the normalized
// (effective) values, so the UI can prefill its custom-DNS inputs.
func applySettingsToState(s *State, ro routing.Options, tun singbox.TunOptions, autoconnect bool) {
	if ro.SplitMode == routing.SplitOff || len(ro.SplitApps) == 0 {
		s.Split = ""
		s.SplitApps = nil
	} else {
		s.Split = string(ro.SplitMode)
		s.SplitApps = append([]string(nil), ro.SplitApps...)
	}
	s.KillSwitch = ro.KillSwitch
	s.TunStack = tun.Stack
	s.Autoconnect = autoconnect
	s.AdBlock = ro.AdBlock
	s.DNSRemote = ro.DNSRemote
	s.DNSDirect = ro.DNSDirect
}

// handlePing measures dial latency to every server in a profile and returns the
// per-server results.
func (d *Daemon) handlePing(ctx context.Context, req Request) Response {
	if req.Profile == "" {
		return newError(req.ID, "ping: missing profile")
	}
	p, ok := d.store.Get(req.Profile)
	if !ok {
		return newError(req.ID, profile.ErrNotFound.Error())
	}
	results := d.pingServers(ctx, p.Servers)
	out := struct {
		Results []PingResult `json:"results"`
	}{Results: results}
	resp, err := newResult(req.ID, out)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// pingServers TCP-dials each server's host:port concurrently and reports the
// dial latency. It is best-effort: an unreachable server yields ok=false with a
// zero RTT rather than failing the whole call. Results preserve input order.
func (d *Daemon) pingServers(ctx context.Context, servers []profile.Server) []PingResult {
	results := make([]PingResult, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		go func(i int, srv profile.Server) {
			defer wg.Done()
			results[i] = d.pingOne(ctx, srv)
		}(i, srv)
	}
	wg.Wait()
	return results
}

// pingOne TCP-dials a single server within pingDialTimeout and reports the
// latency. A server missing a host or port, or one that fails to dial, comes
// back with ok=false rather than an error.
func (d *Daemon) pingOne(ctx context.Context, srv profile.Server) PingResult {
	res := PingResult{Node: srv.ID}
	if srv.Server == "" || srv.Port == 0 {
		return res
	}
	dialCtx, cancel := context.WithTimeout(ctx, pingDialTimeout)
	defer cancel()
	addr := net.JoinHostPort(srv.Server, strconv.Itoa(srv.Port))
	start := d.now()
	conn, err := d.dial(dialCtx, "tcp", addr)
	if err != nil {
		return res
	}
	res.RTTMs = d.now().Sub(start).Milliseconds()
	res.Ok = true
	_ = conn.Close()
	return res
}

// profileResult wraps a profile into the {profile: Profile} response shape used
// by the import_subscription / import_link / refresh_subscription commands. The
// profile is redacted (see redactProfile) before it goes on the wire: these
// replies travel the same local socket as list_profiles, so serialising the full
// stored profile would hand its node credentials and token-bearing subscription
// URL to any unprivileged reader — exactly what redacting list_profiles closed.
func (d *Daemon) profileResult(id int64, p profile.Profile) Response {
	out := struct {
		Profile redactedProfile `json:"profile"`
	}{Profile: redactProfile(p)}
	resp, err := newResult(id, out)
	if err != nil {
		return newError(id, err.Error())
	}
	return resp
}

// newResult0 is a successful empty response (no data), for commands that return
// nothing.
func newResult0(id int64) Response {
	return Response{ID: id, Ok: true}
}

// applyUserInfo folds a parsed Subscription-Userinfo header into a profile's
// traffic/expiry fields. A blank header leaves the profile untouched.
func applyUserInfo(p *profile.Profile, header string) {
	if header == "" {
		return
	}
	info, err := subscription.ParseUserInfo(header)
	if err != nil {
		return
	}
	p.TrafficUsed = info.Upload + info.Download
	p.TrafficTotal = info.Total
	if !info.Expire.IsZero() {
		e := info.Expire.UTC()
		p.ExpiresAt = &e
	}
}

// profileNodes converts a profile's servers into the model.Node slice the
// builder consumes, preserving order.
func profileNodes(p profile.Profile) []model.Node {
	nodes := make([]model.Node, len(p.Servers))
	for i, s := range p.Servers {
		nodes[i] = s.Node
	}
	return nodes
}

// buildCandidates turns a profile's servers into fallback attempts. An explicit
// node ID collapses the set to that single server, so a user who asked for a
// specific exit gets exactly it (and an error if it is unknown). Without an
// explicit node, every renderable server becomes a candidate and the fallback
// machine decides their order.
//
// Servers the builder can't render — a zero protocol, or one not in
// knownProtocol — are dropped: a config selecting such a node would silently
// fall back to a different outbound, so probing it would measure the wrong exit.
// Better to never attempt it. If that leaves nothing, connect fails with a clear
// message rather than dialling the wrong server.
func buildCandidates(p profile.Profile, explicit string) ([]fallback.Attempt, error) {
	if explicit != "" {
		for _, s := range p.Servers {
			if s.ID == explicit {
				if !renderable(s) {
					return nil, fmt.Errorf("connect: node %s has an unsupported protocol %q", explicit, s.Protocol)
				}
				return []fallback.Attempt{{NodeID: s.ID, Node: s.Node}}, nil
			}
		}
		return nil, fmt.Errorf("connect: node not found in profile")
	}

	cands := make([]fallback.Attempt, 0, len(p.Servers))
	for _, s := range p.Servers {
		if !renderable(s) {
			continue
		}
		cands = append(cands, fallback.Attempt{NodeID: s.ID, Node: s.Node})
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("connect: profile has no usable servers")
	}
	return cands, nil
}

// serverTags computes, for every renderable server in the profile, the sing-box
// selector tag it will carry, keyed by the server's stable ID. The builder
// derives each tag from the node name, uniquing collisions with a numeric suffix
// and skipping nodes it can't render — a zero/unknown protocol, or a known one
// that fails singbox.ValidateNode; this mirrors that walk exactly so the tag the
// loop hands Build as the selector default points at the intended exit and not a
// namesake. Getting it wrong would route through the wrong server, so the
// duplication is deliberate. Skipped servers get no entry, matching the builder
// dropping them.
func serverTags(p profile.Profile) map[string]string {
	seen := map[string]int{}
	uniq := func(name string) string {
		base := sanitizeTag(name)
		if seen[base] == 0 {
			seen[base] = 1
			return base
		}
		for {
			seen[base]++
			cand := fmt.Sprintf("%s-%d", base, seen[base])
			if seen[cand] == 0 {
				seen[cand] = 1
				return cand
			}
		}
	}

	tags := make(map[string]string, len(p.Servers))
	for _, s := range p.Servers {
		if s.Protocol == "" {
			continue // builder skips zero-protocol nodes; no tag assigned
		}
		tag := uniq(s.Node.Name)
		if !singbox.ValidateNode(s.Node) {
			// The builder validates every node before emitting it and, on failure,
			// allocates then frees its tag (delete(seen, tag) in buildNodes) — a
			// known protocol carrying bad params (out-of-range port, missing
			// credentials, keyless REALITY) is dropped exactly like an unknown one.
			// Skipping this check here (only knownProtocol below) would keep a tag
			// the builder never emits, drifting the suffix counter so a later tag —
			// and thus the selector default — points at the wrong exit.
			delete(seen, tag)
			continue
		}
		if !knownProtocol(s.Protocol) {
			// The builder allocates a tag then frees it for protocols it can't
			// render (delete(seen, tag) in buildNodes), so no tag survives and the
			// suffix counter is rolled back. Mirror that or every later tag drifts.
			delete(seen, tag)
			continue
		}
		tags[s.ID] = tag
	}
	return tags
}

// renderable reports whether the builder can turn this server into a working
// outbound: it must have a known, non-zero protocol AND carry the parameters
// that protocol needs (singbox.ValidateNode). A node with a known protocol but
// invalid params — a keyless REALITY VLESS, a zero/oversized port, missing
// credentials — is dropped by the builder, so it must not become a fallback
// candidate either: probing it would build a config that silently omits it and
// measure a different exit.
func renderable(s profile.Server) bool {
	return s.Protocol != "" && knownProtocol(s.Protocol) && singbox.ValidateNode(s.Node)
}

// knownProtocol reports whether the builder can render a node of this protocol
// into an outbound or endpoint. It must track the protocols handled in
// singbox.outbound and the AmneziaWG endpoint path; anything else the builder
// drops, freeing its tag.
func knownProtocol(p model.Protocol) bool {
	switch p {
	case model.VLESS, model.Hysteria2, model.Shadowsocks,
		model.Trojan, model.VMess, model.AmneziaWG:
		return true
	default:
		return false
	}
}

// sanitizeTag mirrors the builder's tag sanitisation (control chars and quotes
// become '-', blank becomes "node"). It is duplicated here, not imported,
// because it is an internal detail of the singbox package; this copy only needs
// to agree with it for the selector-default computation in nodesAndTag.
func sanitizeTag(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "node"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteByte('-')
		case r == '"' || r == '\\':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "node"
	}
	return out
}
