package control

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tenebra-vpn/tenebra/core/profile"
	"github.com/tenebra-vpn/tenebra/core/singbox"
)

// trafficPollInterval is how often the daemon polls cumulative byte counters and
// emits a traffic event with the computed rates.
const trafficPollInterval = time.Second

func (d *Daemon) handleConnect(ctx context.Context, req Request) Response {
	if req.Profile == "" {
		return newError(req.ID, "connect: missing profile")
	}
	p, ok := d.store.Get(req.Profile)
	if !ok {
		return newError(req.ID, profile.ErrNotFound.Error())
	}

	chosen, ok := selectNode(p, req.Node, d.lastGood)
	if !ok {
		if req.Node != "" {
			return newError(req.ID, "connect: node not found in profile")
		}
		return newError(req.ID, "connect: profile has no servers")
	}

	// Build the config under the current routing options, with every node
	// present and the chosen one as the selector default.
	d.mu.Lock()
	ro := d.routing
	tun := d.tun
	d.mu.Unlock()

	nodes, selTag := nodesAndTag(p, chosen)
	cfg, err := singbox.Build(nodes, selTag, ro, tun)
	if err != nil {
		return newError(req.ID, fmt.Sprintf("connect: build config: %v", err))
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return newError(req.ID, fmt.Sprintf("connect: encode config: %v", err))
	}

	// Tear down any existing connection before starting a new one so we never
	// run two sing-box processes at once.
	d.teardown(StateConnecting, p.ID, chosen.ID)

	runCtx, cancel := context.WithCancel(context.Background())
	if err := d.runner.Start(runCtx, cfgJSON); err != nil {
		cancel()
		// Roll the state back to error; the UI surfaces it and can retry.
		d.setState(State{State: StateError, Profile: p.ID, Node: chosen.ID, Error: err.Error()})
		return newError(req.ID, fmt.Sprintf("connect: start: %v", err))
	}

	d.mu.Lock()
	d.cancel = cancel
	d.generation++
	gen := d.generation
	d.mu.Unlock()

	// Record this node as last-good optimistically. The process watcher demotes
	// nothing on its own; a later failed connect simply won't update it. (A
	// connectivity probe that confirms the tunnel before marking last-good is
	// deferred — see the package notes.)
	if d.lastGood != nil {
		d.lastGood.Set(p.ID, chosen.ID)
	}

	d.startLifecycle(runCtx, gen, p.ID, chosen.ID)

	// The connect response reports connecting; connected is delivered later as a
	// state event once we have a healthy poll, and error if the process exits.
	st := State{State: StateConnecting, Profile: p.ID, Node: chosen.ID, Routing: string(ro.Mode)}
	d.setState(st)
	resp, err := newResult(req.ID, st)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

func (d *Daemon) handleDisconnect(req Request) Response {
	d.teardown(StateIdle, "", "")
	st := d.snapshotState()
	resp, err := newResult(req.ID, st)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// teardown stops the current connection (if any), waits for its goroutines to
// drain, and sets the state to newState carrying profile/node. Passing
// StateConnecting is how connect transitions an old connection out before the
// new one starts; StateIdle is a plain disconnect.
func (d *Daemon) teardown(newState ConnState, profileID, nodeID string) {
	d.mu.Lock()
	cancel := d.cancel
	d.cancel = nil
	// Bump generation so any in-flight watcher/poller from the old connection
	// recognises it is superseded and won't emit further state.
	d.generation++
	d.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// Stop the process and wait for lifecycle goroutines to finish before we
	// declare the new state, so events don't interleave across connections.
	_ = d.runner.Stop()
	d.wg.Wait()

	switch newState {
	case StateIdle:
		d.setState(State{State: StateIdle, Routing: d.snapshotState().Routing})
	default:
		// connecting: state is set by the caller right after Start succeeds; set
		// an interim value so a failure between here and Start is still coherent.
		d.setState(State{State: newState, Profile: profileID, Node: nodeID, Routing: d.snapshotState().Routing})
	}
}

// startLifecycle launches the two background goroutines for an active
// connection: a process watcher that reacts to sing-box exiting, and a traffic
// poller that emits traffic events. Both stop when ctx is cancelled or the
// generation moves on.
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
// state (the tunnel dropped); a clean teardown just returns. It also promotes
// the state from connecting to connected shortly after a successful start, since
// a fake/real runner that stays up is our signal the tunnel is established.
func (d *Daemon) watchProcess(ctx context.Context, gen uint64, profileID, nodeID string, done <-chan error) {
	// Promote to connected once the process has stayed up briefly. This is a
	// pragmatic readiness signal: sing-box that fails fast (bad config, no admin
	// for tun) exits before this fires, so we report error instead. A real
	// connectivity probe is deferred.
	promote := time.NewTimer(connectedAfter)
	defer promote.Stop()

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
		case <-promote.C:
			if !d.isCurrent(gen) {
				return
			}
			if d.snapshotState().State == StateConnecting {
				d.setState(State{State: StateConnected, Profile: profileID, Node: nodeID, Routing: d.snapshotState().Routing})
			}
		}
	}
}

// connectedAfter is how long the process must stay up before we report
// connected. Short enough to feel responsive, long enough that an immediate
// crash is reported as error rather than a connected-then-error flap.
const connectedAfter = 600 * time.Millisecond

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

func (d *Daemon) emitTraffic(ev TrafficEvent) {
	d.mu.Lock()
	emit := d.emit
	d.mu.Unlock()
	if emit != nil {
		emit(EventTraffic, ev)
	}
}

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
