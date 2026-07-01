package control

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Divaaaan/tenebra/core/fallback"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// trafficPollInterval is how often the daemon polls cumulative byte counters and
// emits a traffic event with the computed rates.
const trafficPollInterval = time.Second

// Fallback-loop timing defaults. They are deliberately conservative: the warmup
// gives sing-box's clash API time to bind before the first probe, and the budget
// is long enough to ride out a slow-but-working handshake yet short enough that a
// blocked protocol is abandoned promptly so the next one gets its turn.
const (
	defaultProbeWarmup  = 700 * time.Millisecond
	defaultProbeRetry   = 500 * time.Millisecond
	defaultProbeTimeout = 6 * time.Second
	defaultProbeBudget  = 8 * time.Second
)

// handleConnect starts a connection to a profile. It validates the profile,
// then hands off to startConnect, which builds the fallback candidate list
// (collapsed to a single node when the request names one) and kicks off the
// background fallback loop. It returns as soon as the attempt is launched;
// progress arrives as state events.
func (d *Daemon) handleConnect(ctx context.Context, req Request) Response {
	if req.Profile == "" {
		return newError(req.ID, "connect: missing profile")
	}
	p, ok := d.store.Get(req.Profile)
	if !ok {
		return newError(req.ID, profile.ErrNotFound.Error())
	}
	if len(p.Servers) == 0 {
		return newError(req.ID, "connect: profile has no servers")
	}

	// A user-driven connect opens a fresh kill-switch relaunch budget.
	d.mu.Lock()
	d.relaunches = 0
	d.mu.Unlock()

	st, err := d.startConnect(ctx, p, req.Node, req.Auto)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	resp, err := newResult(req.ID, st)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// startConnect launches a connection attempt to profile p and returns the
// connecting state it reported. It is the shared engine behind the connect
// command, the live re-apply of tun/kill-switch options, and the kill-switch
// relaunch of a dead tunnel — all three are "start sing-box against the current
// options", differing only in how the candidate set is chosen. It returns as
// soon as the fallback loop is launched.
func (d *Daemon) startConnect(ctx context.Context, p profile.Profile, explicitNode string, auto bool) (State, error) {
	// Build the fallback candidates. An explicit node request collapses the walk
	// to that single node: the user asked for a specific exit, so we honour it and
	// do not silently wander to another protocol behind their back. Without an
	// explicit node we hand the machine every server and let the ordering plus the
	// per-profile last-good decide the sequence.
	candidates, err := buildCandidates(p, explicitNode)
	if err != nil {
		return State{}, err
	}

	// Choose the candidate ordering. The default is protocol preference (the
	// anti-DPI strategy). When the request asks for auto AND named no explicit
	// node, we measure each candidate's TCP round-trip and order them fastest
	// first instead. An explicit node already collapsed the walk to one
	// candidate, so auto is moot there — honouring the user's exact pick. Either
	// machine drives the same fallback loop, so the anti-DPI walk (advance to the
	// next candidate when the lead's connect-probe is blocked) is preserved in
	// both modes.
	var m *fallback.Machine
	if auto && explicitNode == "" {
		// pingCandidates dials only the candidate servers, concurrently and
		// briefly (see pingOne's pingDialTimeout), so probing never blocks the
		// connect path for long. Unreachable candidates fall to the back of the
		// latency order but are still tried.
		rtt := d.pingCandidates(ctx, p, candidates)
		m = fallback.NewByLatency(p.ID, candidates, rtt, d.lastGood)
	} else {
		m = fallback.New(p.ID, candidates, fallback.DefaultOrder, d.lastGood)
	}

	// Snapshot routing/tun under lock so the loop builds every per-candidate
	// config against a consistent view even if set_routing runs meanwhile.
	d.mu.Lock()
	ro := d.routing
	tun := d.tun
	d.mu.Unlock()

	nodes := profileNodes(p)
	tags := serverTags(p)

	// Tear down any existing connection (and any in-flight loop) before starting a
	// new one so we never run two sing-box processes at once.
	d.teardown(StateConnecting, p.ID, "")

	runCtx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.cancel = cancel
	d.generation++
	gen := d.generation
	d.mu.Unlock()

	// The loop runs in the background and is tracked by wg so a later
	// teardown/disconnect both cancels its context and waits for it to drain. It
	// owns Start/Probe/Stop per candidate and promotes the state to connected (or
	// error) itself.
	loop := fallbackLoop{
		gen:       gen,
		profileID: p.ID,
		nodes:     nodes,
		tags:      tags,
		ro:        ro,
		tun:       tun,
		machine:   m,
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.runFallback(runCtx, loop)
	}()

	// connect reports connecting immediately; connected/error arrive later as
	// state events from the loop.
	st := State{State: StateConnecting, Profile: p.ID, Routing: string(ro.Mode)}
	d.setState(st)
	return d.snapshotState(), nil
}

// reapplyLive pushes the current routing/tun options onto a live tunnel by
// restarting sing-box on the node it is already connected to. strict_route and
// the tun stack are startup options of the inbound — sing-box cannot change
// them on a running process — so "apply now" necessarily means a process swap.
// What it does NOT mean is a full reconnect: the candidate set is pinned to the
// current node (no fallback walk, no re-selection), the state dips through
// connecting only for the swap-and-probe, and a probe failure surfaces as an
// error state exactly like any dropped tunnel.
//
// When nothing is connected this is a no-op: the recorded options simply apply
// on the next connect. If the live profile/node has meanwhile vanished from the
// store, the running tunnel is left untouched (old options and all) and the
// change is logged as deferred — a settings toggle must never kill a working
// tunnel without bringing one back.
func (d *Daemon) reapplyLive() {
	cur := d.snapshotState()
	if cur.State != StateConnected || cur.Profile == "" || cur.Node == "" {
		return
	}
	p, ok := d.store.Get(cur.Profile)
	if !ok {
		d.emitLog(LogWarn, "re-apply: connected profile no longer stored; the change applies on the next connect")
		return
	}
	if _, err := d.startConnect(context.Background(), p, cur.Node, false); err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("re-apply: %v; the change applies on the next connect", err))
	}
}

// pingCandidates measures the TCP round-trip to each candidate's server and
// returns a nodeID -> RTT(ms) map holding only the reachable ones; an
// unreachable or unmeasurable candidate is simply omitted, which NewByLatency
// reads as "sort last". It restricts the probe to the candidate set (not every
// server in the profile) so auto only pays for nodes the walk could actually
// pick, and reuses pingOne so the dial timeout, the injectable dialer/clock, and
// the best-effort semantics match the ping command exactly.
func (d *Daemon) pingCandidates(ctx context.Context, p profile.Profile, candidates []fallback.Attempt) map[string]int64 {
	// Index the candidate IDs so we probe only those servers.
	want := make(map[string]struct{}, len(candidates))
	for _, a := range candidates {
		want[a.NodeID] = struct{}{}
	}
	servers := make([]profile.Server, 0, len(candidates))
	for _, s := range p.Servers {
		if _, ok := want[s.ID]; ok {
			servers = append(servers, s)
		}
	}

	results := d.pingServers(ctx, servers)
	rtt := make(map[string]int64, len(results))
	for _, r := range results {
		if r.Ok {
			rtt[r.Node] = r.RTTMs
		}
	}
	return rtt
}

// handleDisconnect tears the tunnel down to idle and returns the resulting
// state. It is a no-op when nothing is connected.
func (d *Daemon) handleDisconnect(req Request) Response {
	// An explicit disconnect also closes any kill-switch relaunch episode: the
	// user asked for the tunnel to be down, so nothing may bring it back.
	d.mu.Lock()
	d.relaunches = 0
	d.mu.Unlock()

	d.teardown(StateIdle, "", "")
	st := d.snapshotState()
	resp, err := newResult(req.ID, st)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// teardown stops the current connection (if any), cancels any in-flight fallback
// loop, waits for all connection goroutines to drain, and sets the state to
// newState carrying profile/node. Passing StateConnecting is how connect
// transitions an old connection out before the new one starts; StateIdle is a
// plain disconnect.
func (d *Daemon) teardown(newState ConnState, profileID, nodeID string) {
	d.mu.Lock()
	cancel := d.cancel
	d.cancel = nil
	// Bump generation so any in-flight loop/watcher/poller from the old connection
	// recognises it is superseded and won't emit further state.
	d.generation++
	d.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// Stop the process and wait for connection goroutines (the fallback loop, then
	// any watcher/poller it started) to finish before we declare the new state, so
	// events don't interleave across connections. The loop also stops the runner
	// itself on cancel; Stop is idempotent.
	_ = d.runner.Stop()
	d.wg.Wait()

	switch newState {
	case StateIdle:
		d.setState(State{State: StateIdle, Routing: d.snapshotState().Routing})
	default:
		// connecting: connect sets the live state right after this returns; set an
		// interim value so a failure in between is still coherent.
		d.setState(State{State: newState, Profile: profileID, Node: nodeID, Routing: d.snapshotState().Routing})
	}
}

// fallbackLoop bundles the immutable inputs of one connect's fallback walk so
// runFallback's signature stays readable.
type fallbackLoop struct {
	gen       uint64
	profileID string
	nodes     []model.Node
	tags      map[string]string // server ID -> sing-box selector tag
	ro        routing.Options
	tun       singbox.TunOptions
	machine   *fallback.Machine
}

// runFallback drives the fallback machine for one connect. Per candidate it
// builds a config with that node selected, starts sing-box, waits for the clash
// API to confirm real traffic flows through the selector (Probe), and on success
// promotes to connected and hands off to the long-lived watcher/poller. A probe
// failure or an early process exit logs, stops the runner, marks the candidate
// failed, and moves to the next. When the machine is exhausted it reports an
// error state. The whole loop bails the moment its generation is superseded or
// ctx is cancelled, stopping any process it started.
func (d *Daemon) runFallback(ctx context.Context, loop fallbackLoop) {
	for {
		if ctx.Err() != nil || !d.isCurrent(loop.gen) {
			_ = d.runner.Stop()
			return
		}
		attempt, ok := loop.machine.Next()
		if !ok {
			// Exhausted: every candidate was tried and failed.
			if d.isCurrent(loop.gen) {
				d.emitLog(LogError, "connect: all protocols failed")
				d.setState(State{State: StateError, Profile: loop.profileID,
					Error: "all protocols failed", Routing: d.snapshotState().Routing})
			}
			return
		}

		tag := loop.tags[attempt.NodeID]
		cfgJSON, err := buildConfigJSON(loop.nodes, tag, loop.ro, loop.tun)
		if err != nil {
			// A node we can't even render is not worth a process; skip it.
			d.emitLog(LogWarn, fmt.Sprintf("connect: skip %s: %v", protoLabel(attempt.Node), err))
			loop.machine.Failure(attempt)
			continue
		}

		if err := d.runner.Start(ctx, cfgJSON); err != nil {
			if !d.isCurrent(loop.gen) {
				_ = d.runner.Stop()
				return
			}
			d.emitLog(LogWarn, fmt.Sprintf("connect: start %s failed: %v", protoLabel(attempt.Node), err))
			loop.machine.Failure(attempt)
			continue
		}

		// Probe through the selector ("proxy"). Success means traffic really flows.
		if d.probeUntilUp(ctx, loop.gen) {
			// Superseded mid-probe: probeUntilUp already returned false in that case,
			// so reaching here means a genuine success on the current generation.
			loop.machine.Success(attempt)
			if d.lastGood != nil {
				d.lastGood.Set(loop.profileID, attempt.NodeID)
			}
			d.setState(State{State: StateConnected, Profile: loop.profileID,
				Node: attempt.NodeID, Routing: d.snapshotState().Routing})
			// Hand the live connection off to the watcher/poller and stop looping.
			d.startLifecycle(ctx, loop.gen, loop.profileID, attempt.NodeID)
			return
		}

		// Probe failed, the process exited early, or we were superseded. If
		// superseded, bail without touching state (teardown owns it).
		if !d.isCurrent(loop.gen) {
			_ = d.runner.Stop()
			return
		}
		d.emitLog(LogWarn, fmt.Sprintf("connect: %s blocked, trying next", protoLabel(attempt.Node)))
		_ = d.runner.Stop()
		loop.machine.Failure(attempt)
	}
}

// probeUntilUp waits for the clash API to come up, then probes the selector
// repeatedly within the per-candidate budget. It returns true only on a probe
// success that is still the current generation. It returns false on budget
// exhaustion, an early process exit, ctx cancellation, or supersession — the
// caller distinguishes these via isCurrent and runner state.
func (d *Daemon) probeUntilUp(ctx context.Context, gen uint64) bool {
	done := d.runner.Done()

	budget, cancelBudget := context.WithTimeout(ctx, d.probeBudget)
	defer cancelBudget()

	// Initial warmup so the API has a chance to bind before the first probe.
	if !sleepCtx(budget, d.probeWarmup) {
		return false
	}

	for {
		if !d.isCurrent(gen) {
			return false
		}
		// A process exit before a successful probe is a hard failure for this
		// candidate — don't keep probing a dead sing-box.
		select {
		case <-done:
			return false
		default:
		}

		probeCtx, cancelProbe := context.WithTimeout(budget, d.probeTimeout)
		delay, err := d.runner.Probe(probeCtx, proxySelectorTag)
		cancelProbe()
		if err == nil {
			if !d.isCurrent(gen) {
				return false
			}
			d.emitLog(LogInfo, fmt.Sprintf("connect: tunnel up (%dms)", delay))
			return true
		}

		// Wait before retrying, but give up the moment the budget or ctx expires or
		// the process dies.
		select {
		case <-budget.Done():
			return false
		case <-done:
			return false
		case <-time.After(d.probeRetry):
		}
	}
}

// proxySelectorTag is the sing-box selector tag the builder always emits; the
// probe tests connectivity through it, so a protocol switch (a different node
// selected as the selector default) is what the probe actually measures.
const proxySelectorTag = "proxy"

// startLifecycle launches the two background goroutines for an established
// connection: a process watcher that turns an unexpected sing-box exit into an
// error state, and a traffic poller that emits traffic events. Both stop when ctx
// is cancelled or the generation moves on. It is called once, after a probe has
// already promoted the state to connected.
func (d *Daemon) startLifecycle(ctx context.Context, gen uint64, profileID, nodeID string) {
	done := d.runner.Done()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.watchProcess(ctx, gen, profileID, nodeID, done)
	}()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.pollTraffic(ctx, gen)
	}()
}

// watchProcess waits for either the process to exit or the connection to be torn
// down. A process exit while this is still the live generation becomes an error
// state (the tunnel dropped) — unless the kill switch is armed, in which case
// the daemon relaunches the tunnel on the same node (see killSwitchRelaunch). A
// clean teardown just returns. Unlike before, it no longer promotes
// connecting->connected on a timer: the fallback loop promotes to connected only
// after a successful connectivity probe, so by the time this goroutine runs the
// state is already connected.
func (d *Daemon) watchProcess(ctx context.Context, gen uint64, profileID, nodeID string, done <-chan error) {
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-done:
			if !d.isCurrent(gen) {
				return // superseded; teardown already set the state
			}
			msg := "sing-box exited"
			if err != nil {
				msg = err.Error()
			}
			d.emitLog(LogError, "tunnel process exited: "+msg)
			if d.killSwitchRelaunch(gen, profileID, nodeID) {
				return // the relaunch owns the state from here
			}
			d.setState(State{State: StateError, Profile: profileID, Node: nodeID, Error: msg, Routing: d.snapshotState().Routing})
			return
		}
	}
}

// maxRelaunches bounds consecutive kill-switch relaunches so a tunnel that dies
// on every start can't churn processes forever. The counter resets on an
// explicit connect or disconnect, so a user retry always gets a fresh budget.
const maxRelaunches = 5

// killSwitchRelaunch restarts a tunnel whose process died unexpectedly, if (and
// only if) the kill switch is armed. It reports whether it took ownership of the
// state; false means the caller should fall through to the plain error state.
//
// Why restart at all: strict_route only holds while sing-box runs — the moment
// the process dies, its filter rules and the tun route die with it, and traffic
// would fall back to the physical interface. The honest mitigation the daemon
// can offer is to put the tunnel (and its filters) back immediately, pinned to
// the node the user was on. During the gap the OS is unprotected; that window
// is why this relaunches eagerly rather than waiting for the user.
//
// The relaunch reuses startConnect, which tears down the old connection and
// waits for its goroutines — including the watcher that called us — so it must
// run on a fresh goroutine, not on the watcher's own stack (that would
// deadlock on wg.Wait). The generation is re-checked on that goroutine: if a
// user connect/disconnect landed meanwhile, the relaunch yields to it.
func (d *Daemon) killSwitchRelaunch(gen uint64, profileID, nodeID string) bool {
	d.mu.Lock()
	armed := d.routing.KillSwitch
	spent := d.relaunches
	if armed && spent < maxRelaunches {
		d.relaunches++
	}
	d.mu.Unlock()

	if !armed {
		return false
	}
	if spent >= maxRelaunches {
		d.emitLog(LogError, fmt.Sprintf("kill switch: tunnel died %d times in a row; giving up on restarts", spent))
		return false
	}
	p, ok := d.store.Get(profileID)
	if !ok {
		d.emitLog(LogError, "kill switch: cannot restart, profile no longer stored")
		return false
	}

	d.emitLog(LogWarn, "kill switch: tunnel process died, restarting it on the same node")
	go func() {
		if !d.isCurrent(gen) {
			return // a user action superseded the dead connection; let it win
		}
		if _, err := d.startConnect(context.Background(), p, nodeID, false); err != nil {
			d.emitLog(LogError, fmt.Sprintf("kill switch: restart failed: %v", err))
			d.setState(State{State: StateError, Profile: profileID, Node: nodeID,
				Error: "tunnel died and could not be restarted: " + err.Error(),
				Routing: d.snapshotState().Routing})
		}
	}()
	return true
}

// pollTraffic polls cumulative counters every interval and emits a traffic event
// carrying the totals and the per-second rate derived from the previous sample.
func (d *Daemon) pollTraffic(ctx context.Context, gen uint64) {
	ticker := time.NewTicker(trafficPollInterval)
	defer ticker.Stop()

	var lastUp, lastDown int64
	var lastT time.Time
	have := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !d.isCurrent(gen) {
				return
			}
			up, down, err := d.runner.Stats()
			if err != nil {
				// API not ready yet (process still starting) — skip this tick.
				continue
			}
			now := d.now()
			ev := TrafficEvent{Up: up, Down: down}
			if have {
				dt := now.Sub(lastT).Seconds()
				if dt > 0 {
					ev.UpRate = rate(up-lastUp, dt)
					ev.DownRate = rate(down-lastDown, dt)
				}
			}
			lastUp, lastDown, lastT, have = up, down, now, true
			d.emitTraffic(ev)
		}
	}
}

// sleepCtx blocks for d or until ctx is done, whichever comes first. It returns
// true if the full duration elapsed, false if ctx was cancelled — letting a
// caller bail promptly on teardown instead of sleeping out the warmup.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// rate converts a byte delta over dt seconds into bytes/second, clamped at zero
// so a counter reset (process restart) never yields a negative rate.
func rate(delta int64, dt float64) int64 {
	if delta < 0 {
		return 0
	}
	return int64(float64(delta) / dt)
}

// isCurrent reports whether gen is still the live connection generation.
func (d *Daemon) isCurrent(gen uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.generation == gen
}

// setState replaces the connection state and emits a state event reflecting it.
func (d *Daemon) setState(s State) {
	d.mu.Lock()
	// Preserve the routing label if the new state didn't set one.
	if s.Routing == "" {
		s.Routing = d.state.Routing
	}
	// The split/kill-switch/tun preferences are daemon-wide settings, not
	// per-connection ones, so they always track the live options rather than
	// whatever the transient State value carried. This keeps status reporting
	// them correctly across connect/disconnect transitions without every call
	// site restating them.
	applySettingsToState(&s, d.routing, d.tun)
	d.state = s
	emit := d.emit
	d.mu.Unlock()

	if emit != nil {
		body := stateEventBody(s)
		emit(EventState, body)
	}
}

// stateEventBody projects a State into the state event payload (which omits the
// profile field — the protocol's state event carries state/node/error only).
func stateEventBody(s State) stateEvent {
	return stateEvent{State: s.State, Node: s.Node, Error: s.Error}
}

// stateEvent is the wire body of a state event.
type stateEvent struct {
	State ConnState `json:"state"`
	Node  string    `json:"node,omitempty"`
	Error string    `json:"error,omitempty"`
}

// emitTraffic pushes a traffic counter event to the UI, if an emitter is set.
func (d *Daemon) emitTraffic(ev TrafficEvent) {
	d.mu.Lock()
	emit := d.emit
	d.mu.Unlock()
	if emit != nil {
		emit(EventTraffic, ev)
	}
}

// emitLog pushes a diagnostic log line to the UI, if an emitter is set.
func (d *Daemon) emitLog(level, msg string) {
	d.mu.Lock()
	emit := d.emit
	d.mu.Unlock()
	if emit != nil {
		emit(EventLog, LogEvent{Level: level, Msg: msg})
	}
}

// emitProfiles signals that the stored profile set changed so the UI reloads it.
// The event carries no body — it is a hint to re-run list_profiles, not a
// snapshot — so it stays cheap and the UI keeps a single source of truth.
func (d *Daemon) emitProfiles() {
	d.mu.Lock()
	emit := d.emit
	d.mu.Unlock()
	if emit != nil {
		emit(EventProfiles, struct{}{})
	}
}

// Close tears down any active connection. The server calls it on shutdown.
func (d *Daemon) Close() error {
	d.teardown(StateIdle, "", "")
	return nil
}

// buildConfigJSON builds and marshals a sing-box config with selTag selected.
func buildConfigJSON(nodes []model.Node, selTag string, ro routing.Options, tun singbox.TunOptions) ([]byte, error) {
	cfg, err := singbox.Build(nodes, selTag, ro, tun)
	if err != nil {
		return nil, fmt.Errorf("build config: %w", err)
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return cfgJSON, nil
}

// protoLabel is a short human label for a node, used in fallback log lines so the
// UI shows "vless blocked, trying next" rather than an opaque id.
func protoLabel(n model.Node) string {
	if n.Protocol != "" {
		return string(n.Protocol)
	}
	return "node"
}
