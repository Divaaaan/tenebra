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
// builds the fallback candidate list (collapsed to a single node when the
// request names one), and kicks off the background fallback loop. It returns as
// soon as the attempt is launched; progress arrives as state events.
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

	// Build the fallback candidates. An explicit node request collapses the walk
	// to that single node: the user asked for a specific exit, so we honour it and
	// do not silently wander to another protocol behind their back. Without an
	// explicit node we hand the machine every server and let DefaultOrder plus the
	// per-profile last-good decide the sequence.
	candidates, err := buildCandidates(p, req.Node)
	if err != nil {
		return newError(req.ID, err.Error())
	}

	m := fallback.New(p.ID, candidates, fallback.DefaultOrder, d.lastGood)

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
	resp, err := newResult(req.ID, st)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleDisconnect tears the tunnel down to idle and returns the resulting
// state. It is a no-op when nothing is connected.
func (d *Daemon) handleDisconnect(req Request) Response {
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
// state (the tunnel dropped); a clean teardown just returns. Unlike before, it no
// longer promotes connecting->connected on a timer: the fallback loop promotes to
// connected only after a successful connectivity probe, so by the time this
// goroutine runs the state is already connected.
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
			d.setState(State{State: StateError, Profile: profileID, Node: nodeID, Error: msg, Routing: d.snapshotState().Routing})
			return
		}
	}
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
	// The split config is a daemon-wide setting, not a per-connection one, so it
	// always tracks the live routing options rather than whatever the transient
	// State value carried. This keeps status reporting the split correctly across
	// connect/disconnect transitions without every call site restating it.
	applySplitToState(&s, d.routing)
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
