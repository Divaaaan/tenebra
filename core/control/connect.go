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

	// Refuse to raise a second tun over another VPN's default route. Checked here
	// — on the user-driven connect only — rather than inside startConnect, whose
	// other callers (kill-switch relaunch, reconcile) fire while our own tunnel is
	// already up or mid-teardown; blocking those would turn a recoverable blip
	// into a tunnel that cannot come back. The user can retry with
	// AllowTunConflict once they have read which interface is in the way.
	if err := d.checkTunConflict(req.AllowTunConflict); err != nil {
		return newError(req.ID, err.Error())
	}

	// Bring the DPI bypass up with the tunnel, so one press does the whole job:
	// the bypass takes YouTube and Discord on the direct path (no added latency),
	// the tunnel takes everything else that is blocked, and games stay on the ISP
	// path. Whether it actually started decides where those services are routed —
	// tunnelling them anyway would pay a round trip they no longer need, and
	// routing them direct without the bypass would leave them blocked.
	d.raiseZapretForConnect(ctx)

	// A user-driven connect opens a fresh kill-switch relaunch budget.
	d.mu.Lock()
	d.relaunches = 0
	d.mu.Unlock()

	// Hold connMu across the transition so an in-flight kill-switch relaunch or
	// connecting-window reconcile (the only connects that run off this serialized
	// command loop) cannot interleave: it re-checks the generation under the same
	// lock and yields to this connect. A user command is the one connect whose
	// success records the last-connect intent for autoconnect (remember=true).
	d.connMu.Lock()
	st, err := d.startConnect(ctx, p, req.Node, req.Auto, true, "")
	d.connMu.Unlock()
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
// command, the live re-apply of tun/kill-switch options, the kill-switch
// relaunch of a dead tunnel, and the daemon-start autoconnect — all of them are
// "start sing-box against the current options", differing only in how the
// candidate set is chosen. It returns as soon as the fallback loop is launched.
// remember marks a user-commanded connect: on success the loop records
// (profile, explicitNode) as the last-connect intent autoconnect re-issues at
// the next daemon start. Every other caller passes false — a relaunch or
// hot-swap pins the node it happens to be on, which is not the user's intent.
//
// avoid drops one node from the walk before it starts: the health failover passes
// the degraded node it is leaving so the walk moves to a different exit instead of
// reconnecting the one it just abandoned. It is ignored for an explicit-node
// connect (the user pinned that exact exit) and when empty.
func (d *Daemon) startConnect(ctx context.Context, p profile.Profile, explicitNode string, auto, remember bool, avoid string) (State, error) {
	// Build the fallback candidates. An explicit node request collapses the walk
	// to that single node: the user asked for a specific exit, so we honour it and
	// do not silently wander to another protocol behind their back. Without an
	// explicit node we hand the machine every server and let the ordering plus the
	// per-profile last-good decide the sequence.
	candidates, err := buildCandidates(p, explicitNode)
	if err != nil {
		return State{}, err
	}
	// A health failover asks to avoid the node it is leaving; drop it so the walk
	// picks a different exit. If that empties the set (a single-node profile has
	// nowhere else to go) fail here, before any teardown, so the current tunnel is
	// left running rather than torn down for a walk that would immediately exhaust.
	if explicitNode == "" && avoid != "" {
		candidates = dropCandidate(candidates, avoid)
		if len(candidates) == 0 {
			return State{}, fmt.Errorf("connect: no alternative node to fail over to")
		}
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
	mh := d.multihop
	d.mu.Unlock()

	nodes := profileNodes(p)
	nodeIDs := serverIDs(p)
	tags := serverTags(p)
	// Resolve the multihop selection (server IDs) into the builder-facing outbound
	// tags now that the profile's tag map is in hand, so every per-candidate config
	// this loop builds carries the same chain. An unresolvable pair (a node that
	// vanished, or one the builder won't render) leaves ro untouched — a single hop.
	ro = resolveMultihop(ro, mh, tags)

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
		gen:           gen,
		profileID:     p.ID,
		nodes:         nodes,
		nodeIDs:       nodeIDs,
		tags:          tags,
		ro:            ro,
		tun:           tun,
		machine:       m,
		remember:      remember,
		requestedNode: explicitNode,
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
	// Serialize with the off-command relaunch/reconcile connects (connMu), same as
	// the connect command, so the hot-swap can't race one of them.
	d.connMu.Lock()
	defer d.connMu.Unlock()
	cur := d.snapshotState()
	if cur.State != StateConnected || cur.Profile == "" || cur.Node == "" {
		return
	}
	p, ok := d.store.Get(cur.Profile)
	if !ok {
		d.emitLog(LogWarn, "re-apply: connected profile no longer stored; the change applies on the next connect")
		return
	}
	if _, err := d.startConnect(context.Background(), p, cur.Node, false, false, ""); err != nil {
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

	// The teardown runs under connMu so its generation bump is authoritative over
	// an in-flight relaunch/reconcile: that goroutine, blocked on connMu, wakes to
	// find the generation moved and yields instead of resurrecting the tunnel.
	d.connMu.Lock()
	d.teardown(StateIdle, "", "")
	d.connMu.Unlock()
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
//
// Callers hold connMu so the whole transition (this teardown plus whatever start
// follows it) is serialized against the off-command relaunch/reconcile connects.
// It waits on d.wg, which never tracks those goroutines (they run under
// relaunchWG), so the wait cannot deadlock against a connMu holder.
func (d *Daemon) teardown(newState ConnState, profileID, nodeID string) {
	d.mu.Lock()
	cancel := d.cancel
	d.cancel = nil
	// Bump generation so any in-flight loop/watcher/poller from the old connection
	// recognises it is superseded and won't emit further state.
	d.generation++
	// Drop the old walk's snapshot: it no longer describes the live state, so a
	// client attaching after this teardown must not be handed it. A new connect's
	// loop repopulates it from its own first snapshot.
	d.attempts = nil
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

	// Clear the OS system proxy AFTER the goroutines have drained — this is the
	// guard's single busiest chokepoint (every disconnect, hot-swap, connect
	// supersession, and shutdown funnels through teardown). Doing it here, not
	// before wg.Wait, closes a race: a superseded connect's recordSuccess can still
	// be arming the proxy as it unwinds, and a disarm that ran earlier would leave
	// that late arm standing. It is idempotent and a no-op unless we armed it, so
	// tun-mode teardowns and the pre-start teardown of a fresh connect pay nothing.
	d.disarmSystemProxy()

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
	// nodeIDs is the server ID of each entry in nodes, at the same index, so the
	// loop can find which node an attempt selects when it folds a transport
	// strategy into that one node before building the config.
	nodeIDs []string
	tags    map[string]string // server ID -> sing-box selector tag
	ro      routing.Options
	tun     singbox.TunOptions
	machine *fallback.Machine
	// remember marks a user-commanded connect: on success the daemon records
	// (profileID, requestedNode) as the last-connect intent for autoconnect.
	// Off-command connects (relaunch, reconcile, autoconnect) leave it false.
	remember bool
	// requestedNode is the explicit node the user asked for, "" when the
	// fallback machine chooses the exit.
	requestedNode string
}

// attemptTracker records the progress of one fallback walk and emits an
// "attempts" snapshot on every status change, giving the UI a step-by-step view
// of the anti-DPI walk. It is built from the machine's resolved plan up front, so
// the opening snapshot already lists every candidate (waiting) in the order they
// will be tried. A single runFallback goroutine owns it, so its item slice needs
// no lock; only the publish crosses into the daemon (under d.mu).
type attemptTracker struct {
	d        *Daemon
	gen      uint64
	items    []attemptItem
	byNodeID map[string]int // node ID -> index into items
}

// newAttemptTracker builds a tracker for loop's walk: one item per candidate in
// the machine's resolved order, each waiting, with the profile's last-good node
// flagged. The last-good lookup is done once here so every snapshot of this walk
// marks the same node even if last-good changes on a success mid-walk.
func (d *Daemon) newAttemptTracker(loop fallbackLoop) *attemptTracker {
	order := loop.machine.Order()
	lastGoodID := ""
	if d.lastGood != nil {
		if id, ok := d.lastGood.Get(loop.profileID); ok {
			lastGoodID = id
		}
	}
	items := make([]attemptItem, len(order))
	byNodeID := make(map[string]int, len(order))
	for i, a := range order {
		items[i] = attemptItem{
			Seq:      i + 1,
			Protocol: string(a.Node.Protocol),
			Node:     a.NodeID,
			Status:   AttemptWaiting,
			LastGood: a.NodeID == lastGoodID,
		}
		byNodeID[a.NodeID] = i
	}
	return &attemptTracker{d: d, gen: loop.gen, items: items, byNodeID: byNodeID}
}

// begin emits the opening snapshot: every candidate waiting, walk in progress.
func (t *attemptTracker) begin() { t.publish(AttemptOutcomePending) }

// trying marks a candidate in flight on its native parameters (waiting -> trying)
// and emits the snapshot.
func (t *attemptTracker) trying(a fallback.Attempt) {
	if i, ok := t.byNodeID[a.NodeID]; ok {
		t.items[i].Status = AttemptTrying
	}
	t.publish(AttemptOutcomePending)
}

// escalating keeps a candidate in flight but records that it is now being tried
// under a different transport strategy on the same node — the adaptive response
// to a handshake that looked interfered with. It names the strategy now driving
// the node and the reason for the switch, so the snapshot narrates the escalation
// rather than looking like a stuck "trying".
func (t *attemptTracker) escalating(a fallback.Attempt, strat fallback.Strategy) {
	if i, ok := t.byNodeID[a.NodeID]; ok {
		t.items[i].Status = AttemptTrying
		t.items[i].Strategy = strat.Name
		t.items[i].Reason = fallback.Censored.String()
	}
	t.publish(AttemptOutcomePending)
}

// blockedWithReason marks a candidate failed and, when reason is non-empty,
// records why the walk gave up on it (e.g. "censored" once its transport
// strategies were exhausted). The walk stays in progress; a later candidate may
// still come up.
func (t *attemptTracker) blockedWithReason(a fallback.Attempt, reason string) {
	if i, ok := t.byNodeID[a.NodeID]; ok {
		t.items[i].Status = AttemptBlocked
		if reason != "" {
			t.items[i].Reason = reason
		}
	}
	t.publish(AttemptOutcomePending)
}

// succeeded closes the walk on a candidate that came up. It records the transport
// strategy it connected under when that was a non-default variation (so the UI
// can surface a node that only worked after adaptation) and clears any interim
// interference reason now that the node is connected.
func (t *attemptTracker) succeeded(a fallback.Attempt, strat fallback.Strategy) {
	if i, ok := t.byNodeID[a.NodeID]; ok {
		t.items[i].Status = AttemptOK
		t.items[i].Reason = ""
		if !strat.IsDefault() {
			t.items[i].Strategy = strat.Name
		}
	}
	t.publish(AttemptOutcomeConnected)
}

// exhausted emits the terminal snapshot when every candidate failed: the items
// keep their last (blocked) statuses and the walk closes with "exhausted".
func (t *attemptTracker) exhausted() { t.publish(AttemptOutcomeExhausted) }

// publish snapshots the current items with outcome and hands them to the daemon
// to store and emit. The items are copied so the emitted/stored snapshot never
// aliases the tracker's mutable slice.
func (t *attemptTracker) publish(outcome string) {
	snap := attemptsEvent{
		Items:   append([]attemptItem(nil), t.items...),
		Outcome: outcome,
	}
	t.d.emitAttempts(t.gen, snap)
}

// emitAttempts stores snap as the live walk snapshot (so a mid-walk attach can be
// handed it) and pushes it to the client — but only while gen is still the
// current generation. A superseded walk must neither clobber a newer walk's
// stored snapshot nor emit a stale step over it, mirroring how the rest of the
// loop gates on isCurrent before touching shared state.
func (d *Daemon) emitAttempts(gen uint64, snap attemptsEvent) {
	d.mu.Lock()
	if d.generation != gen {
		d.mu.Unlock()
		return
	}
	stored := snap
	d.attempts = &stored
	emit := d.emit
	d.mu.Unlock()
	if emit != nil {
		emit(EventAttempts, snap)
	}
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
	// The tracker publishes the walk as "attempts" snapshots. The opening one
	// lists every candidate (waiting) in the order they will be tried — the plan
	// is already resolved — so the UI can render the whole walk before the first
	// probe. An explicit-node connect and a hot-swap re-apply run this same loop
	// with a single candidate, so they emit a one-item snapshot (waiting -> trying
	// -> ok/blocked), keeping the UI's fallback view uniform.
	tracker := d.newAttemptTracker(loop)
	tracker.begin()

	for {
		if ctx.Err() != nil || !d.isCurrent(loop.gen) {
			_ = d.runner.Stop()
			return
		}
		attempt, ok := loop.machine.Next()
		if !ok {
			// Exhausted: every candidate was tried and failed.
			if d.isCurrent(loop.gen) {
				tracker.exhausted()
				d.emitLog(LogError, "connect: all protocols failed")
				d.emitSingboxTail() // surface why sing-box could not carry any node
				d.setState(State{State: StateError, Profile: loop.profileID,
					Error: "all protocols failed", Routing: d.snapshotState().Routing})
			}
			return
		}

		// About to attempt this candidate: mark it trying before the process starts.
		tracker.trying(attempt)

		// attemptNode walks this node across its transport-strategy cascade. A
		// censored handshake escalates to the next strategy on THIS node; anything
		// else — a dead/ambiguous failure, a render/start error, or an exhausted
		// cascade — falls through here to the next node.
		switch outcome, reason := d.attemptNode(ctx, loop, attempt, tracker); outcome {
		case nodeConnected:
			return // attemptNode promoted to connected and started the lifecycle
		case nodeSuperseded:
			return // teardown owns the state; the runner is already stopped
		default: // nodeFailed
			tracker.blockedWithReason(attempt, reason)
			loop.machine.Failure(attempt)
		}
	}
}

// nodeOutcome is the result of walking one node across its transport-strategy
// cascade in attemptNode.
type nodeOutcome int

const (
	// nodeConnected: the node came up (on some strategy); the connection was
	// promoted and its lifecycle started, so the fallback loop returns.
	nodeConnected nodeOutcome = iota
	// nodeSuperseded: the generation moved or ctx was cancelled mid-attempt; the
	// runner is stopped and the loop returns without touching state.
	nodeSuperseded
	// nodeFailed: the node did not come up under any strategy (or could not be
	// rendered/started); the loop marks it blocked and advances to the next node.
	nodeFailed
)

// attemptNode tries one node across the transport-strategy cascade. It begins
// with the node's native parameters and escalates to the next strategy — a
// reshaped handshake on the SAME node — only when a failure carries the
// interference fingerprint (the entry is reachable but the handshake then
// silently stalls). A dead or ambiguous failure, a render/start error, or an
// exhausted cascade abandons the node for the next one. On success it promotes
// the connection and hands it to the lifecycle goroutines. The returned reason
// annotates a block: "censored" when the node was abandoned after its handshake
// looked interfered with, empty otherwise.
func (d *Daemon) attemptNode(ctx context.Context, loop fallbackLoop, attempt fallback.Attempt, tracker *attemptTracker) (nodeOutcome, string) {
	tag := loop.tags[attempt.NodeID]
	strategies := fallback.DefaultStrategies
	censored := false // the node showed the interference fingerprint at least once

	for si, strat := range strategies {
		if ctx.Err() != nil || !d.isCurrent(loop.gen) {
			_ = d.runner.Stop()
			return nodeSuperseded, ""
		}

		// Build this candidate's config with the strategy's handshake reshaping
		// folded into the selected node. The default (first) strategy reshapes
		// nothing, so its config is byte-for-byte the pre-adaptation one.
		nodes := applyStrategyToNodes(loop.nodes, loop.nodeIDs, attempt.NodeID, strat)
		cfgJSON, err := buildConfigJSON(nodes, tag, loop.ro, loop.tun)
		if err != nil {
			// A node we can't even render is not worth a process, and no handshake
			// reshaping fixes a render error — abandon the node outright.
			d.emitLog(LogWarn, fmt.Sprintf("connect: skip %s: %v", protoLabel(attempt.Node), err))
			return nodeFailed, ""
		}

		if err := d.runner.Start(ctx, cfgJSON); err != nil {
			if !d.isCurrent(loop.gen) {
				_ = d.runner.Stop()
				return nodeSuperseded, ""
			}
			d.emitLog(LogWarn, fmt.Sprintf("connect: start %s failed: %v", protoLabel(attempt.Node), err))
			return nodeFailed, ""
		}

		// Probe through the selector ("proxy"). up=true means traffic really flows;
		// stalled reports whether a failure was a silent stall (the through-tunnel
		// half of the interference signal) rather than a fast, hard error.
		up, stalled := d.probeUntilUp(ctx, loop.gen)
		if up {
			// Superseded mid-probe returns up=false, so reaching here is a genuine
			// success on the current generation.
			d.recordSuccess(ctx, loop, attempt, tracker, strat)
			return nodeConnected, ""
		}
		if !d.isCurrent(loop.gen) {
			_ = d.runner.Stop()
			return nodeSuperseded, ""
		}

		// Failed on the current generation. Stop this process before deciding.
		_ = d.runner.Stop()

		// Escalate to another transport strategy on THIS node only when there is a
		// further one AND the failure looks like destination interference. A dead
		// or ambiguous node gains nothing from a reshaped handshake, so we stop and
		// let the outer loop advance to the next node.
		if si < len(strategies)-1 {
			if d.classify(ctx, attempt.Node, stalled) == fallback.Censored {
				censored = true
				next := strategies[si+1]
				d.emitLog(LogWarn, fmt.Sprintf("connect: %s handshake stalled; escalating transport strategy to %s",
					protoLabel(attempt.Node), next.Name))
				tracker.escalating(attempt, next)
				continue
			}
		}
		break
	}

	d.emitLog(LogWarn, fmt.Sprintf("connect: %s blocked, trying next", protoLabel(attempt.Node)))
	reason := ""
	if censored {
		reason = fallback.Censored.String()
	}
	return nodeFailed, reason
}

// recordSuccess promotes a candidate whose probe came up: it persists last-good,
// records the last-connect intent for a user-commanded connect, closes the walk
// snapshot as succeeded (before flipping the state, so the "ok" step precedes the
// connected event on the wire), sets the connected state, hands the live
// connection to the watcher/poller, and reconciles any option toggled during the
// connecting window. strat is the strategy the node came up under, so a
// non-default one is surfaced in the snapshot.
func (d *Daemon) recordSuccess(ctx context.Context, loop fallbackLoop, attempt fallback.Attempt, tracker *attemptTracker, strat fallback.Strategy) {
	loop.machine.Success(attempt)
	if d.lastGood != nil {
		d.lastGood.Set(loop.profileID, attempt.NodeID)
	}
	if loop.remember {
		d.rememberLastConn(loop.profileID, loop.requestedNode)
	}
	tracker.succeeded(attempt, strat)
	// Capture the connected instant before publishing it, so the uptime the
	// relaunch budget reads later is measured from a fixed point.
	connectedAt := d.now()
	// In system-proxy mode, point the OS at the loopback mixed inbound before we
	// announce "connected", so the state never claims the tunnel is up while system
	// traffic still egresses direct. The probe already confirmed the inbound carries
	// traffic. The guard clears it again on any teardown (disconnect, hot-swap,
	// shutdown) and on a tunnel-process death, so the OS is never left pointing at a
	// proxy that is no longer listening. The address comes from loop.tun — the
	// snapshot this connect built its config from — so a mid-connect mode change
	// can't point the OS at the wrong port.
	if loop.tun.IsSystemProxy() {
		d.armSystemProxy(loop.tun.MixedHostPort())
	}
	d.setState(State{State: StateConnected, Profile: loop.profileID,
		Node: attempt.NodeID, Routing: d.snapshotState().Routing})
	// Hand the live connection off to the watcher/poller.
	d.startLifecycle(ctx, loop.gen, loop.profileID, attempt.NodeID, connectedAt)
	// A kill-switch/tun toggle that landed during the connecting window was
	// recorded but not baked into this config; reconcile it now.
	d.reconcileConnectingOptions(loop, attempt.NodeID)
}

// probeUntilUp waits for the clash API to come up, then probes the selector
// repeatedly within the per-candidate budget. It returns up=true only on a probe
// success that is still the current generation. stalled reports, on a failure,
// whether the last probe made no progress within its deadline — a silent stall,
// the through-tunnel corroboration of destination interference — rather than
// failing fast; it is meaningless when up is true. up is false on budget
// exhaustion, an early process exit, ctx cancellation, or supersession — the
// caller distinguishes these via isCurrent and runner state.
func (d *Daemon) probeUntilUp(ctx context.Context, gen uint64) (up bool, stalled bool) {
	done := d.runner.Done()

	budget, cancelBudget := context.WithTimeout(ctx, d.probeBudget)
	defer cancelBudget()

	// Initial warmup so the API has a chance to bind before the first probe.
	if !sleepCtx(budget, d.probeWarmup) {
		return false, false
	}

	for {
		if !d.isCurrent(gen) {
			return false, stalled
		}
		// A process exit before a successful probe is a hard failure for this
		// candidate — don't keep probing a dead sing-box.
		select {
		case <-done:
			return false, stalled
		default:
		}

		probeCtx, cancelProbe := context.WithTimeout(budget, d.probeTimeout)
		start := d.now()
		delay, err := d.runner.Probe(probeCtx, proxySelectorTag)
		elapsed := d.now().Sub(start)
		cancelProbe()
		if err == nil {
			if !d.isCurrent(gen) {
				return false, false
			}
			d.emitLog(LogInfo, fmt.Sprintf("connect: tunnel up (%dms)", delay))
			return true, false
		}
		// A probe that ran most of its deadline before failing made no progress: a
		// silent stall. A fast failure (a refused/immediately-erroring entry) sits
		// well under the threshold. The last probe's verdict is what the classifier
		// pairs with the direct TCP observation.
		stalled = elapsed >= d.probeStallThreshold()

		// Wait before retrying, but give up the moment the budget or ctx expires or
		// the process dies.
		select {
		case <-budget.Done():
			return false, stalled
		case <-done:
			return false, stalled
		case <-time.After(d.probeRetry):
		}
	}
}

// probeStallThreshold is the elapsed time past which a single failed probe counts
// as a silent stall rather than a fast failure: three-quarters of one probe's
// deadline. A stalled handshake consumes its whole deadline (or the clash API's
// own timeout) and clears this; a refused or immediately-erroring entry fails far
// under it.
func (d *Daemon) probeStallThreshold() time.Duration {
	return d.probeTimeout * 3 / 4
}

// proxySelectorTag is the sing-box selector tag the builder always emits; the
// probe tests connectivity through it, so a protocol switch (a different node
// selected as the selector default) is what the probe actually measures.
const proxySelectorTag = "proxy"

// startLifecycle launches the background goroutines for an established
// connection: a process watcher that turns an unexpected sing-box exit into an
// error state, a traffic poller that emits traffic events, and a health watchdog
// that probes the active node and fails over on sustained failure. All stop when
// ctx is cancelled or the generation moves on. It is called once, after a probe
// has already promoted the state to connected; connectedAt is that moment,
// threaded to the watcher so a later relaunch can tell a recovered tunnel from a
// crash-loop.
func (d *Daemon) startLifecycle(ctx context.Context, gen uint64, profileID, nodeID string, connectedAt time.Time) {
	done := d.runner.Done()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.watchProcess(ctx, gen, profileID, nodeID, connectedAt, done)
	}()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.pollTraffic(ctx, gen)
	}()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.healthWatch(ctx, gen, profileID, nodeID)
	}()

	// Confirm the bypass is carrying what the routing just handed it. A bypass
	// that starts but does not work leaves those services with no carrier at all,
	// because routing sent them direct precisely because it was running.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.verifyBypass(ctx, gen)
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
func (d *Daemon) watchProcess(ctx context.Context, gen uint64, profileID, nodeID string, connectedAt time.Time, done <-chan error) {
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
			// The mixed inbound died with the process, so an armed system proxy now
			// points at nothing — clear it immediately to restore direct connectivity.
			// A kill-switch relaunch below re-arms it once the tunnel is back
			// (recordSuccess); the plain error path leaves it cleared, which is the
			// honest outcome (proxy mode has no strict_route to fail closed on).
			d.disarmSystemProxy()
			if d.killSwitchRelaunch(gen, profileID, nodeID, d.now().Sub(connectedAt)) {
				return // the relaunch owns the state from here
			}
			d.setState(State{State: StateError, Profile: profileID, Node: nodeID, Error: msg, Routing: d.snapshotState().Routing})
			return
		}
	}
}

// maxRelaunches bounds kill-switch relaunches of a tunnel stuck in a crash-loop
// (connect, die within seconds, repeat) so it can't churn processes forever. The
// counter resets on an explicit connect or disconnect, and also whenever a
// relaunched tunnel stays connected past relaunchResetAfter — a drop after a
// healthy stretch is not part of a crash-loop, so it opens a fresh budget (see
// killSwitchRelaunch). Only rapid, back-to-back deaths spend the budget down.
const maxRelaunches = 5

// defaultRelaunchReset is how long a relaunched tunnel must hold the connection
// for its later death to count as a genuine recovery rather than one more turn of
// a crash-loop: a death after at least this long refunds the relaunch budget, a
// death sooner keeps spending it. It seeds the daemon's relaunchResetAfter, which
// is a field so tests can shorten it.
const defaultRelaunchReset = 30 * time.Second

// killSwitchRelaunch restarts a tunnel whose process died unexpectedly, if (and
// only if) the kill switch is armed. It reports whether it took ownership of the
// state; false means the caller should fall through to the plain error state.
// uptime is how long the dead tunnel held the connection.
//
// Why restart at all: strict_route only holds while sing-box runs — the moment
// the process dies, its filter rules and the tun route die with it, and traffic
// would fall back to the physical interface. The honest mitigation the daemon
// can offer is to put the tunnel (and its filters) back immediately, pinned to
// the node the user was on. During the gap the OS is unprotected; that window
// is why this relaunches eagerly rather than waiting for the user.
//
// The budget: a relaunch that held the tunnel up past relaunchResetAfter proved a
// recovery, not one more turn of a crash-loop, so its eventual death refunds the
// budget. Only rapid deaths (connect, die within seconds, repeat) spend it down,
// so a long healthy session survives many isolated drops while a tunnel that
// truly can't stay up still stops churning after maxRelaunches.
//
// The relaunch reuses startConnect, which tears down the old connection and waits
// for its goroutines — including the watcher that called us — so it must run on a
// fresh goroutine, not on the watcher's own stack (that would deadlock on
// wg.Wait). startConnectIfCurrent runs it there, re-checking the generation under
// connMu so a user connect/disconnect/Close that raced the death wins cleanly.
func (d *Daemon) killSwitchRelaunch(gen uint64, profileID, nodeID string, uptime time.Duration) bool {
	d.mu.Lock()
	armed := d.routing.KillSwitch
	// Refund the budget when the dead tunnel had stayed up long enough to count as
	// a recovery; a quick death leaves the counter alone so a crash-loop keeps
	// spending it.
	if armed && uptime >= d.relaunchResetAfter {
		d.relaunches = 0
	}
	spent := d.relaunches
	if armed && spent < maxRelaunches {
		d.relaunches++
	}
	d.mu.Unlock()

	if !armed {
		return false
	}
	if spent >= maxRelaunches {
		d.emitLog(LogError, fmt.Sprintf("kill switch: relaunched %d times but the tunnel keeps dying within seconds; giving up on automatic restarts", spent))
		return false
	}
	p, ok := d.store.Get(profileID)
	if !ok {
		d.emitLog(LogError, "kill switch: cannot restart, profile no longer stored")
		return false
	}

	d.emitLog(LogWarn, "kill switch: tunnel process died, restarting it on the same node")
	d.startConnectIfCurrent(gen, p, nodeID, "", nil, func(err error) {
		d.emitLog(LogError, fmt.Sprintf("kill switch: restart failed: %v", err))
		d.setState(State{State: StateError, Profile: profileID, Node: nodeID,
			Error:   "tunnel died and could not be restarted: " + err.Error(),
			Routing: d.snapshotState().Routing})
	})
	return true
}

// startConnectIfCurrent runs a connect to (p, node) on a fresh goroutine, but
// only if gen is still the live generation when the goroutine wins connMu. It is
// the single connect path that runs off the serialized command loop — the
// kill-switch relaunch and the connecting-window options reconcile both use it —
// so it must not race a user connect/disconnect/Close. connMu serializes it
// against those (they all hold it); the generation re-check inside that exclusion
// is what makes a user action authoritative: one that already ran bumped the
// generation and this yields, one that runs after this claim supersedes the
// connection it starts through the normal teardown. The goroutine is tracked in
// relaunchWG (not d.wg, which teardown waits on — that would deadlock) so Close
// can drain it and never let a connect outlive the daemon. onErr handles a start
// failure, which each caller logs differently.
//
// avoid is threaded to startConnect (the health failover drops the node it is
// leaving; other callers pass ""). beforeStart, when non-nil, runs under connMu
// once the generation is confirmed still current, right before the reconnect — the
// failover uses it to emit its health_reconnecting state atomically with the
// decision to proceed, so a racing user command that already moved the generation
// skips it cleanly. It never runs when this yields.
func (d *Daemon) startConnectIfCurrent(gen uint64, p profile.Profile, node, avoid string, beforeStart func(), onErr func(error)) {
	d.relaunchWG.Add(1)
	go func() {
		defer d.relaunchWG.Done()
		// Test seam: lets a test park this goroutine before it claims connMu so a
		// concurrent disconnect/Close is guaranteed to win the race. nil in production.
		if d.beforeReconnect != nil {
			d.beforeReconnect()
		}
		d.connMu.Lock()
		defer d.connMu.Unlock()
		if !d.isCurrent(gen) {
			return // a user action superseded us between the death and this claim
		}
		if beforeStart != nil {
			beforeStart()
		}
		if _, err := d.startConnect(context.Background(), p, node, false, false, avoid); err != nil {
			onErr(err)
		}
	}()
}

// rememberLastConn records a successful user-commanded connect — the profile
// and, only when the user pinned one, the explicit node — and persists it so
// autoconnect can re-issue the same intent at the next daemon start. An
// unchanged intent skips the disk write, mirroring how last-good avoids
// rewriting an identical value on every reconnect.
func (d *Daemon) rememberLastConn(profileID, requestedNode string) {
	d.mu.Lock()
	changed := d.lastProfile != profileID || d.lastNode != requestedNode
	d.lastProfile = profileID
	d.lastNode = requestedNode
	d.mu.Unlock()
	if changed {
		d.persistSettings()
	}
}

// AutoconnectOnStart launches a connection to the last successfully connected
// profile if the autoconnect preference is armed, reporting whether an attempt
// was launched. main calls it once per process, after the persisted stores are
// wired — it belongs to the daemon's own start (sidecar spawn, --pipe console,
// Windows service), never to a client session, so with the service the tunnel
// comes up with the machine before anyone logs in, and it can never re-fire on
// a kill-switch relaunch or an options hot-swap.
//
// The connect runs through startConnectIfCurrent: on a background goroutine, so
// control-plane readiness is never delayed (a client attaching mid-attempt sees
// connecting, like with any connect), and gated on the start generation, so a
// user command that lands first wins and the autoconnect yields. A vanished
// profile or node leaves the daemon honestly idle — logged, with no error state
// and never a different exit than the user last chose.
func (d *Daemon) AutoconnectOnStart() bool {
	d.mu.Lock()
	on := d.autoconnect
	profileID := d.lastProfile
	node := d.lastNode
	gen := d.generation
	d.mu.Unlock()

	if !on || profileID == "" {
		return false
	}
	p, ok := d.store.Get(profileID)
	if !ok {
		d.emitLog(LogWarn, "autoconnect: last profile no longer stored; staying idle")
		return false
	}
	d.emitLog(LogInfo, "autoconnect: reconnecting the last profile")
	// Raise the bypass here too, exactly as a user-driven connect does. Without
	// it, an autoconnected session is a different product from a hand-pressed one:
	// the tunnel comes up, the bypass does not, and YouTube and Discord are
	// tunnelled at full round-trip latency (or, if a previous run left the routing
	// expecting the bypass, not carried at all). Autoconnect is how the app starts
	// on most days, so "one button does the whole job" has to hold on the path
	// where no button is pressed.
	//
	// Ordered before the connect, like handleConnect: the flag it sets decides
	// where the censored services are routed in the config the connect builds.
	d.raiseZapretForConnect(context.Background())
	// A start failure (e.g. the pinned node vanished from the profile) is
	// logged and leaves the state untouched: startConnect fails before any
	// teardown, so the daemon stays idle rather than reporting an error for a
	// connection nobody asked for in this session.
	d.startConnectIfCurrent(gen, p, node, "", nil, func(err error) {
		d.emitLog(LogWarn, fmt.Sprintf("autoconnect: %v; staying idle", err))
	})
	return true
}

// reconcileConnectingOptions re-applies the kill-switch/tun options to a tunnel
// that has just come up, if they changed while it was still connecting. The
// fallback loop pins its routing/tun snapshot at the start of the connect so every
// candidate builds against a consistent view; the price is that a set_kill_switch
// or set_tun landing during the warmup+probe window is recorded and reported but
// not baked into the config that actually came up — and reapplyLive no-ops while
// the state is still "connecting". Left alone, the live tunnel would run the old
// options while the state advertises the new ones (e.g. armed in the UI but no
// strict_route on the wire). Here, right after the state reaches connected, we
// compare what this loop built against the live options and, on a divergence,
// hot-swap onto the same node exactly as a post-connect toggle would.
//
// It converges: the hot-swap's own connect re-runs this check, and once the
// options settle it finds no divergence and stops — so a burst of toggles costs at
// most one extra swap per settled value, not an endless loop. Only the live-reapply
// options (kill switch, TLS fragmentation, tun stack) are compared; routing mode
// and split are deferred-to-next-connect by design, matching what reapplyLive
// itself applies.
func (d *Daemon) reconcileConnectingOptions(loop fallbackLoop, nodeID string) {
	d.mu.Lock()
	curRo := d.routing
	curTun := d.tun
	curMh := d.multihop
	d.mu.Unlock()
	// Resolve the current multihop selection against this profile's tags, the same
	// way the loop resolved its own snapshot, so a multihop toggle landing mid-connect
	// is compared like for like and, when it differs, hot-swapped in.
	curRo = resolveMultihop(curRo, curMh, loop.tags)
	if loop.ro.KillSwitch == curRo.KillSwitch &&
		loop.ro.TLSFragment == curRo.TLSFragment &&
		loop.tun.Stack == curTun.Stack &&
		multihopResolved(loop.ro, curRo) {
		return // nothing that applies live changed during the connecting window
	}
	p, ok := d.store.Get(loop.profileID)
	if !ok {
		d.emitLog(LogWarn, "re-apply: connected profile no longer stored; the change applies on the next connect")
		return
	}
	d.emitLog(LogInfo, "re-apply: options changed while connecting; hot-swapping to apply them")
	d.startConnectIfCurrent(loop.gen, p, nodeID, "", nil, func(err error) {
		d.emitLog(LogWarn, fmt.Sprintf("re-apply: %v; the change applies on the next connect", err))
	})
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
	// The split/kill-switch/tun/autoconnect preferences are daemon-wide
	// settings, not per-connection ones, so they always track the live options
	// rather than whatever the transient State value carried. This keeps status
	// reporting them correctly across connect/disconnect transitions without
	// every call site restating them.
	applySettingsToState(&s, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
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

// singboxTailLines is how many trailing sing-box log lines emitSingboxTail
// surfaces — enough to show why a start or handshake failed without flooding the
// UI log.
const singboxTailLines = 20

// emitSingboxTail pushes the tail of the runner's sing-box log ring into the
// daemon log channel. That ring is otherwise unreachable in production — the
// control protocol exposes no "fetch logs" command — so a sing-box that failed to
// start or was rejected at config decode would leave the user only a bare "all
// protocols failed". Called on the terminal exhausted path only, never on a
// healthy connect, so it does not spam the log; a runner with no captured output
// emits nothing.
func (d *Daemon) emitSingboxTail() {
	lines := d.runner.Logs()
	if len(lines) > singboxTailLines {
		lines = lines[len(lines)-singboxTailLines:]
	}
	if len(lines) == 0 {
		return
	}
	d.emitLog(LogInfo, fmt.Sprintf("sing-box output (last %d lines):", len(lines)))
	for _, ln := range lines {
		d.emitLog(LogInfo, "  sing-box: "+ln)
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
	// Cancel any in-flight background entitlement lookups so a slow endpoint
	// doesn't hold shutdown; they only read the store and re-emit, so cutting
	// them short loses nothing but a badge update.
	if d.entCancel != nil {
		d.entCancel()
	}
	// Take the DPI bypass down with us. winws is launched detached (the strategy
	// batch uses `start`), so nothing else ever stops it: without this it outlives
	// the app that started it, keeping a kernel packet filter attached to every
	// connection on the machine with no window, tray icon or setting to explain
	// where it came from. Best-effort — a failure here must not hold up shutdown.
	d.stopZapretQuietly()
	d.connMu.Lock()
	d.teardown(StateIdle, "", "")
	d.connMu.Unlock()
	// teardown already disarmed the system proxy; clear it once more defensively so
	// process shutdown never leaves the OS pointing at a dead proxy even if some
	// path armed it after the teardown. Idempotent — a no-op when already clear.
	d.disarmSystemProxy()
	// The teardown above bumped the generation, so any kill-switch relaunch or
	// connecting-window reconcile still in flight will observe it and abort instead
	// of starting a tunnel. Wait for those goroutines to unwind (after releasing
	// connMu, which a parked one needs to make its check) so none outlives us.
	d.relaunchWG.Wait()
	// The entitlement lookups were cancelled above; wait for them to unwind so
	// none outlives us writing to the store.
	d.entWG.Wait()
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
