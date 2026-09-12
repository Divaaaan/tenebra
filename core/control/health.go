package control

import (
	"context"
	"fmt"
	"time"

	"github.com/Divaaaan/tenebra/core/fallback"
)

// Health-watchdog defaults. They are deliberately conservative: probing every
// defaultHealthInterval keeps the extra load negligible, and requiring
// defaultHealthFailThreshold consecutive failures (roughly a minute and a quarter
// of sustained trouble at the default interval) before failing over rides out a
// single slow probe or a brief hiccup rather than churning the tunnel on noise.
// defaultHealthProbeTimeout bounds one probe so a black-holed node is declared
// failed promptly instead of stalling the whole interval.
const (
	defaultHealthInterval      = 25 * time.Second
	defaultHealthProbeTimeout  = 5 * time.Second
	defaultHealthFailThreshold = 3
)

// healthWatch is the per-connection watchdog: while this generation is live it
// probes the active node every healthInterval and, once the node misses
// healthFailThreshold probes in a row, reconnects to another node on its own (see
// healthFailover). It is one of the goroutines startLifecycle launches under d.wg,
// so a teardown/disconnect cancels its ctx and waits for it to drain; it also
// bails the moment its generation is superseded, so a user connect/disconnect that
// lands first always wins. A disabled toggle or a non-positive interval parks it
// without probing rather than tearing anything down.
func (d *Daemon) healthWatch(ctx context.Context, gen uint64, profileID, nodeID string) {
	interval := d.healthInterval
	probe := d.healthProbe
	if interval <= 0 || probe == nil {
		return // watchdog disabled by configuration
	}
	threshold := d.healthFailThreshold
	if threshold < 1 {
		threshold = 1
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	fails := 0
	warnedNoAlt := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !d.isCurrent(gen) {
				return // superseded; a newer connection owns the state
			}
			// The toggle is read live so a mid-session disable parks the watchdog
			// (without a reconnect) and a later enable resumes from a clean slate.
			d.mu.Lock()
			enabled := d.autoFailover
			d.mu.Unlock()
			if !enabled {
				fails, warnedNoAlt = 0, false
				continue
			}

			if d.healthProbeOK(ctx, probe) {
				fails, warnedNoAlt = 0, false
				continue
			}
			fails++
			if fails < threshold {
				d.emitLog(LogWarn, fmt.Sprintf("health: active node probe failed (%d/%d)", fails, threshold))
				continue
			}

			// A live switch moves the exit without a new generation, so the node this
			// goroutine was started on may not be the one degrading. Everything past
			// here is about the exit actually carrying traffic right now.
			active := d.liveNode(nodeID)

			// Threshold reached: the node has missed too many probes in a row. Try to
			// move the exit without touching the tunnel first — the user keeps their
			// session, and everything already open finishes on the old exit instead of
			// being cut. Only when that is impossible or does not hold up does this
			// fall through to the reconnect-based failover.
			switch d.autoSwitchAway(ctx, gen, profileID, active) {
			case autoSwitchSucceeded, autoSwitchSuppressed:
				fails, warnedNoAlt = 0, false
				continue
			}

			switch d.healthFailover(gen, profileID, active) {
			case failoverStarted:
				// Scheduling does not yet transfer ownership: the queued reconnect
				// can yield to a manual switch or fail validation. Keep monitoring
				// until an actual teardown cancels this context/generation.
				fails, warnedNoAlt = 0, false
			case failoverNoAlternative:
				// A single-node profile has nowhere to fail over to. Keep monitoring so
				// a later subscription refresh or a recovery is still picked up, but warn
				// only once per degradation episode rather than on every threshold hit.
				if !warnedNoAlt {
					d.emitLog(LogWarn, "health: active node degraded but the profile has no other node to fail over to")
					warnedNoAlt = true
				}
				fails = 0
			default: // failoverAborted — the profile vanished; nothing to do but keep watching.
				fails = 0
			}
		}
	}
}

// healthProbeOK runs one health probe within healthProbeTimeout and reports
// whether it succeeded. A nil error means traffic still flows through the active
// outbound; any error (a black-holed node, a blocked protocol, a cancelled ctx on
// teardown) is a failure.
func (d *Daemon) healthProbeOK(ctx context.Context, probe func(context.Context) error) bool {
	probeCtx, cancel := context.WithTimeout(ctx, d.healthProbeTimeout)
	defer cancel()
	return probe(probeCtx) == nil
}

// defaultHealthProbe is the production health probe: a clash-API delay test
// through the selector, the same in-tunnel reachability check the connect walk
// uses to confirm a node came up. It is honest proof the tunnel still carries
// traffic — it fails when the upstream dies or the path is silently dropped, not
// merely when the process exits (that is watchProcess's job). Injectable via
// d.healthProbe so the watchdog is unit-testable without a live network.
func (d *Daemon) defaultHealthProbe(ctx context.Context) error {
	_, err := d.runner.Probe(ctx, proxySelectorTag)
	return err
}

// failoverResult reports what healthFailover did, so the watchdog knows whether to
// stop (a reconnect took over) or keep monitoring (nowhere to go / aborted).
type failoverResult int

const (
	// failoverStarted: a reconnect was queued. The current watchdog remains until
	// that reconnect actually cancels its generation.
	failoverStarted failoverResult = iota
	// failoverNoAlternative: the profile has no other node to move to, so the
	// current connection is left as-is and the watchdog keeps monitoring.
	failoverNoAlternative
	// failoverAborted: the connection could not be resolved (the profile is gone);
	// nothing was started.
	failoverAborted
)

// healthFailover reconnects away from a degraded node, reusing the ordinary
// connect walk with that node excluded so it lands on a different exit. It first
// confirms another renderable node exists — otherwise there is nothing to fail
// over to and the (possibly recoverable) tunnel is left running. The reconnect
// runs off the watchdog's own stack (teardown waits on d.wg, which the watchdog
// is part of), re-checks generation and policy under connMu, and yields to user
// commands that raced it.
func (d *Daemon) healthFailover(gen uint64, profileID, nodeID string) failoverResult {
	if !d.allowAutoRecovery(gen, profileID, nodeID) {
		return failoverAborted
	}
	d.mu.Lock()
	chain := d.multihop.Enabled
	d.mu.Unlock()
	if chain {
		// The selected chain has exactly one exit. Other stored nodes are not
		// authorization to change that chain or fall back to a single hop.
		return failoverNoAlternative
	}
	p, ok := d.store.Get(profileID)
	if !ok {
		d.emitLog(LogWarn, "health: cannot fail over, profile no longer stored")
		return failoverAborted
	}
	// buildCandidates without an explicit node yields every renderable server;
	// dropping the degraded one tells us whether a failover target exists at all.
	cands, err := buildCandidates(p, "")
	if err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("health: cannot fail over: %v", err))
		return failoverAborted
	}
	if len(dropCandidate(cands, nodeID)) == 0 {
		return failoverNoAlternative
	}

	d.emitLog(LogWarn, fmt.Sprintf("health: active node failed %d health probes in a row; failing over to another node", d.healthFailThreshold))
	// Run off the watchdog stack: teardown waits for that watchdog. Re-check and
	// spend the shared budget under connMu, where no user or automatic switch can
	// interleave. A queued reconnect must yield if the exit changed during its wait.
	d.relaunchWG.Add(1)
	go func() {
		defer d.relaunchWG.Done()
		if d.beforeReconnect != nil {
			d.beforeReconnect()
		}
		d.connMu.Lock()
		defer d.connMu.Unlock()
		if !d.allowAutoRecovery(gen, profileID, nodeID) || d.liveNode("") != nodeID {
			return
		}
		latest, ok := d.store.Get(profileID)
		if !ok {
			return
		}
		d.recordAutoSwitch()
		d.setState(State{State: StateHealthReconnecting, Profile: profileID, Node: nodeID,
			Routing: d.snapshotState().Routing})
		if _, err := d.startConnect(context.Background(), latest, "", false, false, nodeID); err != nil {
			// Validation errors leave the old engine up. Preserve that state while
			// counting the failed recovery attempt so repeated failures cannot churn.
			d.setState(State{State: StateConnected, Profile: profileID, Node: nodeID})
			d.emitLog(LogWarn, fmt.Sprintf("health: failover reconnect could not start: %v", err))
		}
	}()
	return failoverStarted
}

// dropCandidate returns a copy of cands without the attempt whose node is nodeID.
// It is how a health failover excludes the exit it is leaving from the walk. A
// fresh slice is returned so neither the caller's slice nor the excluded entry is
// mutated.
func dropCandidate(cands []fallback.Attempt, nodeID string) []fallback.Attempt {
	out := make([]fallback.Attempt, 0, len(cands))
	for _, a := range cands {
		if a.NodeID == nodeID {
			continue
		}
		out = append(out, a)
	}
	return out
}
