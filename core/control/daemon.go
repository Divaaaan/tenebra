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

	// cancel tears down the current connection's goroutines; nil when idle.
	cancel context.CancelFunc
	// watching guards against double-starting the lifecycle goroutines and lets
	// disconnect wait for them to drain.
	wg sync.WaitGroup
	// generation increments on every connect so a stale watcher from a previous
	// connection can tell it has been superseded and stay quiet.
	generation uint64

	// now and dial are injectable for tests; production uses the real clock and
	// dialer.
	now  func() time.Time
	dial func(ctx context.Context, network, address string) (net.Conn, error)

	// fetch retrieves a subscription body. It is injectable so the auto-refresh
	// logic can be unit-tested offline; production uses subscription.Fetch.
	fetch func(ctx context.Context, url string) ([]byte, http.Header, error)
}

// NewDaemon builds a Daemon over a profile store and runner. Routing defaults to
// smart with normalized DNS; the tun options default to the singbox defaults.
func NewDaemon(store *profile.Store, runner Runner) *Daemon {
	d := &Daemon{
		store:    store,
		runner:   runner,
		routing:  routing.Options{Mode: routing.ModeSmart}.Normalize(),
		state:    State{State: StateIdle, Routing: string(routing.ModeSmart)},
		lastGood: fallback.NewMemLastGood(),
		now:      time.Now,
		fetch:    subscription.Fetch,
	}
	var dialer net.Dialer
	d.dial = dialer.DialContext
	return d
}

// SetEmitter installs the event sink. The server calls this before serving.
func (d *Daemon) SetEmitter(emit emitFunc) {
	d.mu.Lock()
	d.emit = emit
	d.mu.Unlock()
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

func (d *Daemon) handleStatus(req Request) Response {
	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

func (d *Daemon) handleListProfiles(req Request) Response {
	out := struct {
		Profiles []profile.Profile `json:"profiles"`
	}{Profiles: d.store.List()}
	// List never returns nil for the slice the UI iterates; normalise to [].
	if out.Profiles == nil {
		out.Profiles = []profile.Profile{}
	}
	resp, err := newResult(req.ID, out)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

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

func (d *Daemon) handleRemoveProfile(req Request) Response {
	if req.Profile == "" {
		return newError(req.ID, "remove_profile: missing profile")
	}
	// If we're connected to this profile, tear the tunnel down first so we don't
	// leave an orphaned process bound to a profile that no longer exists.
	if cur := d.snapshotState(); cur.Profile == req.Profile && cur.State != StateIdle {
		d.teardown(StateIdle, "", "")
	}
	if err := d.store.Remove(req.Profile); err != nil {
		return newError(req.ID, err.Error())
	}
	return newResult0(req.ID)
}

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
	p.ExpiresAt = nil
	p.TrafficUsed = 0
	p.TrafficTotal = 0
	applyUserInfo(&p, header.Get("Subscription-Userinfo"))

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
// by the import/refresh commands.
func (d *Daemon) profileResult(id int64, p profile.Profile) Response {
	out := struct {
		Profile profile.Profile `json:"profile"`
	}{Profile: p}
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

// selectNode chooses which server to connect to for a profile. An explicit node
// ID wins if it matches; otherwise the last-good node for the profile is used if
// still present; otherwise the first server. It returns the chosen server and
// the full ordered server list (so the caller can build a config carrying every
// node with the chosen one selected).
func selectNode(p profile.Profile, explicit string, lastGood fallback.LastGood) (profile.Server, bool) {
	if len(p.Servers) == 0 {
		return profile.Server{}, false
	}
	if explicit != "" {
		for _, s := range p.Servers {
			if s.ID == explicit {
				return s, true
			}
		}
		return profile.Server{}, false
	}
	if lastGood != nil {
		if id, ok := lastGood.Get(p.ID); ok {
			for _, s := range p.Servers {
				if s.ID == id {
					return s, true
				}
			}
		}
	}
	return p.Servers[0], true
}

// nodesAndTag converts a profile's servers into model.Nodes and computes the
// sing-box selector tag the chosen server will carry. The builder derives each
// tag from the node name, uniquing collisions with a numeric suffix and skipping
// zero-protocol nodes; we mirror that walk exactly so the tag we hand Build as
// the selector default points at the chosen node and not a namesake. Getting
// this wrong would silently route through the wrong exit, so it is worth the
// small duplication rather than guessing by raw name.
func nodesAndTag(p profile.Profile, chosen profile.Server) ([]model.Node, string) {
	nodes := make([]model.Node, len(p.Servers))
	for i, s := range p.Servers {
		nodes[i] = s.Node
	}

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

	selTag := ""
	for _, s := range p.Servers {
		if s.Protocol == "" {
			continue // builder skips zero-protocol nodes; no tag assigned
		}
		tag := uniq(s.Node.Name)
		if !knownProtocol(s.Protocol) {
			// The builder allocates a tag then frees it for protocols it can't
			// render (delete(seen, tag) in buildNodes), so no tag survives and
			// the suffix counter is rolled back. Mirror that or every later tag
			// drifts and selTag points at the wrong outbound.
			delete(seen, tag)
			continue
		}
		if s.ID == chosen.ID {
			selTag = tag
		}
	}
	return nodes, selTag
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
