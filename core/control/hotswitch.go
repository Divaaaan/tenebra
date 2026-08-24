package control

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Divaaaan/tenebra/core/nodecheck"
	"github.com/Divaaaan/tenebra/core/profile"
)

// Live-switch timings and thresholds.
//
// A live switch has to be confirmed before it is believed: pointing the selector
// at another node always "succeeds" (the API answers 204 the moment the tag
// exists), so without a probe through the new exit the daemon would report a move
// onto a black hole exactly as confidently as onto a working node.
// defaultSwitchVerifyBudget is how long that confirmation may take before the
// switch is called off and the old exit put back, and defaultSwitchProbeTimeout
// bounds one attempt inside it. Both are short because a user-driven switch holds
// the command loop while it runs, and a working exit answers its first probe in
// well under a second — what the budget really bounds is how long a BROKEN one is
// given before the request falls back to a reconnect.
const (
	defaultSwitchProbeTimeout = 3 * time.Second
	defaultSwitchVerifyBudget = 6 * time.Second
)

// Hysteresis for the automatic (degradation-driven) switch.
//
// The entry condition is already three consecutive failed health probes — about
// 75s of sustained trouble at the default interval — so these guard the far side:
// what stops a bad network from walking the user around the whole node list.
//
//   - defaultAutoSwitchCooldown: no second automatic switch within this window.
//     One switch has to be given time to prove itself; the health watchdog can
//     otherwise reach its threshold again ~75s later, which on a broken uplink
//     (where EVERY exit fails) means an exit change every minute or so forever.
//   - defaultMaxAutoSwitches within defaultAutoSwitchWindow: past that the daemon
//     stops moving and says so. Three moves that all failed to fix it is enough
//     evidence that the problem is not the exit; churning further only adds a
//     handshake per node to an already broken connection.
//   - defaultDegradedRetryAfter: a node that ran out of health probes is passed
//     over for this long. Without it, two exits that both flap simply hand the
//     user back and forth.
//
// The window is a sliding one, so it self-resets: a session that has been quiet
// for defaultAutoSwitchWindow starts with a full budget again.
const (
	defaultAutoSwitchCooldown = 3 * time.Minute
	defaultAutoSwitchWindow   = 15 * time.Minute
	defaultMaxAutoSwitches    = 3
	defaultDegradedRetryAfter = 10 * time.Minute
)

// defaultSwitchScanLimit caps how many candidate exits one degradation scan
// measures. Each candidate costs a handful of requests through the live process,
// and the point is to find a working exit quickly while the user's connection is
// already bad — not to rank the whole subscription. check_nodes remains the
// command for a full survey.
const defaultSwitchScanLimit = 6

// switchScanFanout bounds how many candidates are measured at once, matching
// checkFanout: several nodes probed in parallel would otherwise measure the
// contended uplink rather than the nodes.
const switchScanFanout = 4

// liveConfig is what the running sing-box can be steered to: the generation and
// profile it belongs to, the selector membership the config actually emitted, and
// each profile server's outbound tag within it.
//
// It is captured at connect time rather than recomputed on demand because the two
// can differ: a subscription refresh between the connect and the switch renames or
// drops nodes, and steering the live process by a tag derived from the NEW profile
// would either fail outright or — worse, if a name collided — seat the tunnel on a
// different server than the one the user picked.
type liveConfig struct {
	gen       uint64
	profileID string
	// tagOf maps a profile server ID to its outbound tag in the running config.
	tagOf map[string]string
	// sel is the selector as emitted: its default tag and its membership.
	sel selectorShape
}

// setLiveConfig records what the process that just came up can be steered to.
// Called from recordSuccess, under the same generation gate as every other
// post-connect publication, so a superseded walk cannot install its own map over a
// newer connection's.
func (d *Daemon) setLiveConfig(gen uint64, profileID string, tags map[string]string, sel selectorShape) {
	cp := make(map[string]string, len(tags))
	for k, v := range tags {
		cp[k] = v
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.generation != gen {
		return
	}
	d.live = &liveConfig{gen: gen, profileID: profileID, tagOf: cp, sel: sel}
}

// liveSwitchTarget resolves nodeID into the tag a live switch would use, and
// reports whether the running process can be steered there at all. It says no —
// and the caller then falls back to a full reconnect, which is the behaviour that
// existed before any of this — when:
//
//   - nothing is connected, or the tunnel is mid-transition;
//   - the request names a different profile than the one running;
//   - the running config predates the current generation (a reconnect is already
//     in flight);
//   - the node has no outbound in the running config (it was added by a refresh,
//     or the builder dropped it), or its outbound is not in the selector — which
//     is what multihop looks like, since that collapses the group onto the exit.
func (d *Daemon) liveSwitchTarget(profileID, nodeID string) (tag, prevTag, prevNode string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state.State != StateConnected || d.live == nil {
		return "", "", "", false
	}
	if d.live.gen != d.generation || d.live.profileID != profileID || d.state.Profile != profileID {
		return "", "", "", false
	}
	if nodeID == "" || nodeID == d.state.Node {
		return "", "", "", false
	}
	tag = d.live.tagOf[nodeID]
	if tag == "" || !d.live.sel.has(tag) {
		return "", "", "", false
	}
	prevNode = d.state.Node
	prevTag = d.live.tagOf[prevNode]
	return tag, prevTag, prevNode, true
}

// switchNode moves the running tunnel to nodeID without rebuilding anything: the
// sing-box process, the tun device and its routes all stay exactly as they are and
// only the selector's choice of downstream outbound changes. It reports whether
// the move took.
//
// The order matters. The selector is pointed at the new exit, then traffic is
// confirmed to flow through it, and only then is the state told. A switch that
// cannot be confirmed is undone — the selector goes back to the exit that was
// working — so a failed attempt costs the user nothing but a few seconds, and the
// caller can fall back to a full reconnect from a tunnel that is still up. That is
// the whole reason this is not simply "PUT and hope": the API answers 204 for any
// tag that exists, including one whose server died an hour ago.
//
// Callers must hold connMu, so this cannot interleave with a teardown or with one
// of the off-command connects.
//
// remember marks a user-driven switch, which records the new exit as the
// last-connect intent exactly as a user-driven connect does; an automatic switch
// leaves that intent alone, because the node the watchdog happened to land on is
// not what the user asked for.
func (d *Daemon) switchNode(ctx context.Context, profileID, nodeID, reason string, remember bool) bool {
	tag, prevTag, prevNode, ok := d.liveSwitchTarget(profileID, nodeID)
	if !ok {
		return false
	}

	selCtx, cancel := context.WithTimeout(ctx, d.switchProbeTimeout)
	err := d.runner.Select(selCtx, proxySelectorTag, tag)
	cancel()
	if err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("switch: could not steer the live tunnel to %s (%v); reconnecting instead",
			d.nodeLabel(profileID, nodeID), err))
		return false
	}

	if !d.confirmSwitch(ctx) {
		// Put the working exit back before handing the caller a reconnect: leaving
		// the selector on an exit that carries nothing would black-hole the user's
		// traffic for the whole of the reconnect that follows.
		if prevTag != "" {
			revertCtx, cancelRevert := context.WithTimeout(context.Background(), d.switchProbeTimeout)
			_ = d.runner.Select(revertCtx, proxySelectorTag, prevTag)
			cancelRevert()
		}
		d.emitLog(LogWarn, fmt.Sprintf("switch: %s did not carry traffic; putting %s back and reconnecting instead",
			d.nodeLabel(profileID, nodeID), d.nodeLabel(profileID, prevNode)))
		return false
	}

	d.commitSwitch(profileID, nodeID, prevNode, reason, remember)
	return true
}

// confirmSwitch waits for traffic to actually flow through the newly selected
// exit, retrying within the verify budget. It is the same in-tunnel proof the
// connect walk uses to promote a candidate to connected, so "switched" means
// exactly as much as "connected" does.
func (d *Daemon) confirmSwitch(ctx context.Context) bool {
	budget, cancelBudget := context.WithTimeout(ctx, d.switchVerifyBudget)
	defer cancelBudget()
	for {
		probeCtx, cancelProbe := context.WithTimeout(budget, d.switchProbeTimeout)
		_, err := d.runner.Probe(probeCtx, proxySelectorTag)
		cancelProbe()
		if err == nil {
			return true
		}
		select {
		case <-budget.Done():
			return false
		case <-time.After(d.probeRetry):
		}
	}
}

// commitSwitch publishes a confirmed live switch: the profile's last-good node
// moves, a user-driven switch updates the last-connect intent, the fallback
// snapshot is replaced with a one-item view of the exit now carrying traffic, and
// the state is re-announced.
//
// The state stays "connected" throughout — it never dips through connecting —
// because nothing disconnected. Reporting a reconnect here would be a lie in the
// one direction that matters: it would teach the user that changing exits costs
// them their session, which is precisely what this path removed.
func (d *Daemon) commitSwitch(profileID, nodeID, prevNode, reason string, remember bool) {
	if d.lastGood != nil {
		d.lastGood.Set(profileID, nodeID)
	}
	if remember {
		d.rememberLastConn(profileID, nodeID)
	}

	d.mu.Lock()
	gen := d.generation
	d.mu.Unlock()
	d.emitSwitchAttempt(gen, profileID, nodeID)

	from := d.nodeLabel(profileID, prevNode)
	if from == "" {
		from = "the previous node"
	}
	d.emitLog(LogInfo, fmt.Sprintf("switch: now exiting through %s (%s) — the tunnel was not restarted, and connections already open finish on %s",
		d.nodeLabel(profileID, nodeID), reason, from))
	d.setState(State{State: StateConnected, Profile: profileID, Node: nodeID,
		Routing: d.snapshotState().Routing})
}

// emitSwitchAttempt replaces the fallback-walk snapshot with the single exit now
// carrying traffic. Without it the panel would keep showing the walk that first
// brought the tunnel up, marking a node "ok" that no longer carries anything —
// the same class of stale-but-green report the rest of this package goes out of
// its way to avoid.
func (d *Daemon) emitSwitchAttempt(gen uint64, profileID, nodeID string) {
	proto := ""
	if p, ok := d.store.Get(profileID); ok {
		for _, srv := range p.Servers {
			if srv.ID == nodeID {
				proto = string(srv.Protocol)
				break
			}
		}
	}
	d.emitAttempts(gen, attemptsEvent{
		Items: []attemptItem{{
			Seq:      1,
			Protocol: proto,
			Node:     nodeID,
			Status:   AttemptOK,
			LastGood: true,
		}},
		Outcome: AttemptOutcomeConnected,
	})
}

// autoSwitchAway moves the tunnel off a degraded exit onto one that is measurably
// working, without a reconnect. It reports whether it took ownership; false leaves
// the caller to fall back to the reconnect-based failover.
//
// It is the automatic counterpart of a user tapping another node, and it is gated
// by the hysteresis in allowAutoSwitch: the tunnel must be steerable, the daemon
// must not have moved the exit too recently or too often, and the candidate must
// pass a real measurement before anything moves.
func (d *Daemon) autoSwitchAway(ctx context.Context, gen uint64, profileID, degraded string) bool {
	if !d.allowAutoSwitch(gen, profileID, degraded) {
		return false
	}

	target, ok := d.scanForExit(ctx, profileID, degraded)
	if !ok {
		return false
	}

	// TryLock, not Lock. This runs on the health watchdog's goroutine, which
	// teardown waits for (d.wg) while holding connMu — so blocking here would
	// deadlock a disconnect that lands mid-scan. Taking the lock only when it is
	// free removes the cycle: if it succeeds nobody is inside a teardown, and while
	// it is held a teardown queues on connMu rather than on the watchdog. The rare
	// miss simply falls through to the reconnect-based failover, which needs no
	// lock of its own (see startConnectIfCurrent).
	if !d.connMu.TryLock() {
		return false
	}
	defer d.connMu.Unlock()
	// The generation is re-checked under connMu for the same reason every
	// off-command connect re-checks it: a user command may have landed while the
	// scan ran, and it wins.
	if !d.isCurrent(gen) {
		return false
	}
	if !d.switchNode(ctx, profileID, target, "the previous exit stopped carrying traffic", false) {
		return false
	}
	d.recordAutoSwitch()
	return true
}

// allowAutoSwitch is the hysteresis gate. It marks the degraded node so nothing
// switches back to it for a while, then decides whether an automatic move is
// allowed at all: not while the tunnel is not steerable, not within the cooldown
// of the last one, and not past the per-window cap. The two refusals that are
// about restraint rather than capability are logged once, because a user whose
// exit is degraded and is NOT being moved deserves to know that is a decision.
func (d *Daemon) allowAutoSwitch(gen uint64, profileID, degraded string) bool {
	now := d.now()

	d.mu.Lock()
	if d.degradedAt == nil {
		d.degradedAt = make(map[string]time.Time)
	}
	if degraded != "" {
		d.degradedAt[degraded] = now
	}
	steerable := d.live != nil && d.live.gen == gen && d.live.gen == d.generation && d.live.profileID == profileID
	// Only the switches inside the window count, so a quiet session recovers its
	// full budget without anything having to reset it.
	recent := d.autoSwitches[:0:0]
	for _, t := range d.autoSwitches {
		if now.Sub(t) < d.autoSwitchWindow {
			recent = append(recent, t)
		}
	}
	d.autoSwitches = recent
	spent := len(recent)
	var sinceLast time.Duration
	if spent > 0 {
		sinceLast = now.Sub(recent[spent-1])
	}
	d.mu.Unlock()

	if !steerable {
		return false
	}
	if spent > 0 && sinceLast < d.autoSwitchCooldown {
		return false
	}
	if spent >= d.maxAutoSwitches {
		d.emitLog(LogWarn, fmt.Sprintf("switch: already moved the exit %d times and it is still degrading; leaving it alone — this looks like the local network rather than the node",
			spent))
		return false
	}
	return true
}

// recordAutoSwitch stamps an automatic switch against the window budget.
func (d *Daemon) recordAutoSwitch() {
	d.mu.Lock()
	d.autoSwitches = append(d.autoSwitches, d.now())
	d.mu.Unlock()
}

// scanForExit measures candidate exits through the process that is already
// running and returns the best one that actually works.
//
// Every candidate outbound is already present in the live config — the selector
// lists them all — so a candidate can be judged by asking the running sing-box to
// fetch several control destinations through THAT outbound, with no second
// process, no ports, and nothing touching the tun. The verdicts go through
// core/nodecheck, so the standard it is held to is the one check_nodes uses: a
// strict majority of unalike targets has to survive the node, which is what keeps
// an exit that serves one URL and black-holes the rest from winning.
//
// Candidates that recently ran out of health probes are skipped for
// degradedRetryAfter, which is the other half of the hysteresis: two flapping
// exits must not hand the user back and forth.
func (d *Daemon) scanForExit(ctx context.Context, profileID, degraded string) (string, bool) {
	live, targets, cutoff := d.scanInputs()
	if live == nil || live.profileID != profileID || len(targets) == 0 {
		return "", false
	}
	p, ok := d.store.Get(profileID)
	if !ok {
		return "", false
	}

	cands := d.scanCandidates(live, p, degraded, cutoff)
	if len(cands) == 0 {
		return "", false
	}

	results := d.measureCandidates(ctx, cands, targets)
	lastGood := ""
	if d.lastGood != nil {
		if id, found := d.lastGood.Get(profileID); found && id != degraded {
			lastGood = id
		}
	}
	best, found := nodecheck.Best(results, lastGood)
	if !found {
		d.emitLog(LogWarn, "switch: the current exit is degraded and none of the others carried traffic either")
		return "", false
	}
	return best.NodeID, true
}

// scanInputs snapshots what a scan needs out of the daemon under one lock.
func (d *Daemon) scanInputs() (*liveConfig, []string, time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	targets := append([]string(nil), d.checkTargets...)
	return d.live, targets, d.now().Add(-d.degradedRetryAfter)
}

// scanCandidate is one exit a degradation scan will measure.
type scanCandidate struct {
	nodeID string
	tag    string
}

// scanCandidates picks which exits are worth measuring: members of the live
// selector, minus the degraded one, minus anything that ran out of health probes
// since cutoff, ordered last-good first (an exit that was working earlier in this
// session is the likeliest to be working now) and capped at switchScanLimit so a
// large subscription does not turn a degradation into a minute of probing.
func (d *Daemon) scanCandidates(live *liveConfig, p profile.Profile, degraded string, cutoff time.Time) []scanCandidate {
	d.mu.Lock()
	degradedAt := make(map[string]time.Time, len(d.degradedAt))
	for k, v := range d.degradedAt {
		degradedAt[k] = v
	}
	d.mu.Unlock()

	lastGood := ""
	if d.lastGood != nil {
		if id, ok := d.lastGood.Get(p.ID); ok {
			lastGood = id
		}
	}

	out := make([]scanCandidate, 0, len(p.Servers))
	for _, srv := range p.Servers {
		if srv.ID == degraded {
			continue
		}
		if at, seen := degradedAt[srv.ID]; seen && at.After(cutoff) {
			continue
		}
		tag := live.tagOf[srv.ID]
		if tag == "" || !live.sel.has(tag) {
			continue // not in the running process; a reconnect is the only way there
		}
		out = append(out, scanCandidate{nodeID: srv.ID, tag: tag})
	}

	if lastGood != "" && lastGood != degraded {
		sort.SliceStable(out, func(a, b int) bool { return out[a].nodeID == lastGood })
	}
	if d.switchScanLimit > 0 && len(out) > d.switchScanLimit {
		out = out[:d.switchScanLimit]
	}
	return out
}

// measureCandidates probes every candidate against every target through the live
// process, at most switchScanFanout candidates at a time, and returns one
// nodecheck.NodeResult per candidate in candidate order.
func (d *Daemon) measureCandidates(ctx context.Context, cands []scanCandidate, targets []string) []nodecheck.NodeResult {
	results := make([]nodecheck.NodeResult, len(cands))
	sem := make(chan struct{}, switchScanFanout)
	var wg sync.WaitGroup

	for i, c := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, c scanCandidate) {
			defer wg.Done()
			defer func() { <-sem }()

			tr := make([]nodecheck.TargetResult, 0, len(targets))
			for _, t := range targets {
				if ctx.Err() != nil {
					break
				}
				probeCtx, cancel := context.WithTimeout(ctx, d.switchProbeTimeout)
				delay, err := d.runner.ProbeVia(probeCtx, c.tag, t)
				cancel()
				if err != nil {
					// The clash API runs the whole request through the outbound, so a
					// failure here cannot distinguish a dead address from a tunnel that
					// established and carried nothing. StageProbe is the honest label:
					// the request did not survive the node. Naming a stage we did not
					// measure would be worse than a coarse one.
					tr = append(tr, nodecheck.TargetResult{Target: t, Stage: nodecheck.StageProbe})
					continue
				}
				if delay <= 0 {
					delay = 1
				}
				tr = append(tr, nodecheck.TargetResult{Target: t, Stage: nodecheck.StageOK, RTTMs: int64(delay)})
			}
			results[i] = nodecheck.NodeResult{NodeID: c.nodeID, Targets: tr}
		}(i, c)
	}
	wg.Wait()
	return results
}

// nodeLabel is the human name of a profile server, falling back to its stable ID
// when the profile no longer carries it. Log lines about a switch name the exit
// the user recognises from the node list; a bare ID would make the one line that
// explains why their exit moved unreadable to the person reading it.
func (d *Daemon) nodeLabel(profileID, nodeID string) string {
	if nodeID == "" {
		return ""
	}
	if p, ok := d.store.Get(profileID); ok {
		for _, srv := range p.Servers {
			if srv.ID == nodeID && srv.Name != "" {
				return srv.Name
			}
		}
	}
	return nodeID
}

// liveNode returns the node the tunnel is actually on, falling back to fallbackID
// when the state carries none. The per-connection goroutines are handed the node
// their connect landed on, but a live switch moves the tunnel underneath them
// without a new generation — so a watchdog or a relaunch that trusted its captured
// value would probe, exclude, or restore the wrong exit.
func (d *Daemon) liveNode(fallbackID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state.Node != "" {
		return d.state.Node
	}
	return fallbackID
}
