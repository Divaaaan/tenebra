package control

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodecheck"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// defaultCheckTargets are the destinations a node's verdict is built from.
//
// Several, and deliberately unalike, because a node fails per-destination: the
// exit that prompted this whole path served api.ipify.org normally while timing
// out on gstatic, YouTube and Discord. One control URL would have called it
// healthy. They are also all cheap 204s — the question is whether a request
// survives the node at all, not how fast a page renders.
var defaultCheckTargets = []string{
	"https://www.gstatic.com/generate_204",
	"https://www.youtube.com/generate_204",
	"https://discord.com/robots.txt",
	"https://api.anthropic.com/v1/messages",
}

// defaultCheckBasePort is the preferred start of the per-node probe block.
//
// High, unprivileged, and outside the ports this app already uses (the mixed
// inbound's 2081 and the clash API's 9090). The block is reserved before use
// and the allocator moves elsewhere when another local process already owns it.
const defaultCheckBasePort = 24310

// checkFanout bounds how many nodes are probed at once. Each node costs a few
// concurrent requests, and the point of the measurement is what a node does
// under normal conditions — saturating the uplink with fifty nodes at once would
// measure the uplink instead.
const checkFanout = 4

// checkDialTimeout bounds the plain TCP dial to a node's own address, and
// checkProbeTimeout one control request through it.
const (
	checkDialTimeout  = 4 * time.Second
	checkProbeTimeout = 8 * time.Second
	// checkListenerWait is how long to wait for the probe process to open its
	// loopback listeners before giving up on the run.
	checkListenerWait = 10 * time.Second
	// probeListenerIOTimeout bounds one local SOCKS5 authentication exchange.
	// Loopback either answers immediately or is not the listener we are waiting
	// for; spending a node timeout here would only hide a local start failure.
	probeListenerIOTimeout = 300 * time.Millisecond
)

const (
	// Probe ports stay below the OS ephemeral range on the desktop platforms we
	// ship. The preferred block is tried first; this range is the fallback when
	// that block is occupied or lost in the narrow release-to-spawn race.
	probeFallbackPortMin = 20000
	probeFallbackPortMax = 30000
	probePortSearchLimit = 2048
	probeStartAttempts   = 3

	probeFailureTailLines = 8
	probeFailureLineRunes = 320
	probeFailureMaxRunes  = 3000
)

// defaultCheckBudget bounds a whole run — the wait for the probe's listeners and
// every node measured after it — and is the reason the command can be pressed
// without wondering what it will cost.
//
// Nothing under it was bounded before, only its pieces: the listener wait, then
// one wave of checkFanout nodes after another, each node paying its dial plus a
// request per target. A profile whose dead exits all time out therefore priced
// the run in minutes, and two ceilings sit well below that. The desktop bridge
// abandons a request after 60s (REQUEST_TIMEOUT in
// ui-desktop/src-tauri/src/backend/wire.rs), so past that the caller is told the
// measurement failed while the daemon is still spending its uplink on it. And a
// run that overruns is exactly the run the user is about to press something
// else during — the point of measuring is to connect afterwards — so the budget
// is half the bridge's ceiling, leaving the other half to whatever comes next.
//
// Overrunning it is not an error: what has been measured is reported, and the
// nodes that were not reached come back unmeasured. A partial answer beats a
// bare failure, because a working exit found in the first wave is still the
// right one to connect to.
const defaultCheckBudget = 30 * time.Second

// handleCheckNodes measures what actually survives each node and reports them
// ranked, best first.
//
// This is the command the `ping` command cannot be. A TCP dial to host:port
// proves only that something is listening: on 2026-08-18 an exit accepted TCP
// instantly and then went silent for 19s per request, so it scored as the
// *fastest* node and would have won any latency-based auto-selection while every
// real request through it hung. Here each node gets its own loopback proxy in one
// sing-box process (no tun, nothing touching the system routing table) and is
// judged by whether traffic comes back through it.
func (d *Daemon) handleCheckNodes(ctx context.Context, req Request) Response {
	if req.Profile == "" {
		return newError(req.ID, "check_nodes: missing profile")
	}
	p, ok := d.store.Get(req.Profile)
	if !ok {
		return newError(req.ID, profile.ErrNotFound.Error())
	}
	if d.probeRunner == nil {
		d.emitLog(LogWarn, "check_nodes: no probe runner is configured; cannot measure this profile")
		return newError(req.ID, "check_nodes: probe runner not configured")
	}

	// One run at a time for the whole daemon. Even with dynamically reserved
	// loopback ports, a second probe process would compete for CPU and network and
	// could distort both runs — a measurement
	// that lies is worse than one that is refused. The UI collapses its own
	// double-presses, but it is not the only caller: a session displaced
	// mid-check (the UI restarting) leaves its run unwinding while the new client
	// is already able to ask for another.
	if !d.checkRunning.CompareAndSwap(false, true) {
		return newError(req.ID, "check_nodes: a check is already running")
	}
	defer d.checkRunning.Store(false)

	// The request owns the process lifetime, while the shorter check budget owns
	// only readiness and measurement. If the latter expires, exec.CommandContext
	// must not kill the probe and race the intended successful partial result.
	processParent := ctx
	ctx, cancel := context.WithTimeout(ctx, d.checkBudget)
	defer cancel()

	nodes := make([]model.Node, 0, len(p.Servers))
	for _, s := range p.Servers {
		nodes = append(nodes, s.Node)
	}
	// Render once at an arbitrary valid base to validate the nodes and learn how
	// many listeners the usable subset needs. The actual config is rebuilt only
	// after that many contiguous ports have been reserved successfully.
	_, plannedBindings, err := singbox.BuildProbe(nodes, 1)
	if err != nil {
		return newError(req.ID, fmt.Sprintf("check_nodes: %v", err))
	}

	d.emitLog(LogInfo, fmt.Sprintf("check_nodes: measuring %d node(s) of %q against %d target(s)",
		len(plannedBindings), p.Name, len(d.checkTargets)))

	var (
		results []nodecheck.NodeResult
		tried   []probePortSpan
	)
	for attempt := 1; attempt <= probeStartAttempts; attempt++ {
		reservation, reserveErr := reserveProbePortBlock(len(plannedBindings), d.checkBasePort, tried)
		if reserveErr != nil {
			msg := probeFailureMessage("port reservation", reserveErr, nil)
			d.emitLog(LogWarn, msg)
			return newError(req.ID, msg)
		}
		tried = append(tried, probePortSpan{first: reservation.base, last: reservation.base + len(plannedBindings) - 1})

		cfg, bindings, buildErr := singbox.BuildProbe(nodes, reservation.base)
		if buildErr != nil {
			reservation.release()
			return newError(req.ID, fmt.Sprintf("check_nodes: %v", buildErr))
		}
		raw, marshalErr := json.Marshal(cfg)
		if marshalErr != nil {
			reservation.release()
			return newError(req.ID, fmt.Sprintf("check_nodes: encode probe config: %v", marshalErr))
		}

		runner := d.probeRunner()
		processCtx, cancelProcess := context.WithCancel(processParent)
		stopProcess := func() {
			cancelProcess()
			_ = runner.Stop()
		}
		// Keep the whole contiguous block reserved while the config is rendered,
		// then release it immediately before the process starts. There is no API
		// for handing already-bound sockets to sing-box, so a tiny race remains;
		// authenticated readiness plus the bounded retry below closes it honestly.
		reservation.release()
		if startErr := runner.Start(processCtx, raw); startErr != nil {
			stopProcess()
			msg := probeFailureMessage("startup", startErr, runner)
			if isProbeBindCollision(startErr, runner.Logs()) && attempt < probeStartAttempts {
				d.emitLog(LogWarn, fmt.Sprintf("%s; retrying on a new loopback block (%d/%d)", msg, attempt+1, probeStartAttempts))
				continue
			}
			d.emitLog(LogWarn, msg)
			return newError(req.ID, msg)
		}

		done := runner.Done()
		retryable, readyErr := d.waitForProbeListeners(ctx, bindings, done)
		if readyErr != nil {
			stopProcess()
			msg := probeFailureMessage("authenticated listener startup", readyErr, runner)
			if (retryable || isProbeBindCollision(readyErr, runner.Logs())) && attempt < probeStartAttempts {
				d.emitLog(LogWarn, fmt.Sprintf("%s; retrying on a new loopback block (%d/%d)", msg, attempt+1, probeStartAttempts))
				continue
			}
			d.emitLog(LogWarn, msg)
			return newError(req.ID, msg)
		}

		results, err = d.probeBindings(ctx, p, bindings, done)
		if err != nil {
			stopProcess()
			msg := probeFailureMessage("measurement", err, runner)
			d.emitLog(LogWarn, msg)
			return newError(req.ID, msg)
		}
		stopProcess()
		break
	}
	if results == nil {
		msg := "check_nodes: local probe exhausted its startup attempts; no node was measured"
		d.emitLog(LogWarn, msg)
		return newError(req.ID, msg)
	}
	d.logNodeCheck(results)

	lastGood := ""
	if st := d.snapshotState(); st.Node != "" {
		lastGood = st.Node
	}
	ranked := nodecheck.Rank(results, lastGood)

	out := struct {
		Results []nodecheck.NodeResult `json:"results"`
		Best    string                 `json:"best"`
	}{Results: ranked}
	if best, found := nodecheck.Best(results, lastGood); found {
		out.Best = best.NodeID
		d.emitLog(LogInfo, fmt.Sprintf("check_nodes: best exit is %s", best.NodeID))
	} else {
		d.emitLog(LogWarn, "check_nodes: not one node carried a majority of the targets")
	}

	resp, err := newResult(req.ID, out)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// logNodeCheck writes down what the probe actually observed, per node and per
// stage.
//
// The command's reply carries this too, but the reply goes to whoever asked and
// is then gone. The whole reason this check exists is that a node can accept TCP
// instantly and carry nothing — the exit that scored fastest on 2026-08-18 while
// every real request through it hung for 19 seconds — and that verdict is only
// legible next to the stage breakdown that produced it. A summary line per node
// keeps a fifty-node profile readable; the per-target detail is debug.
func (d *Daemon) logNodeCheck(results []nodecheck.NodeResult) {
	for _, r := range results {
		stages := map[nodecheck.Stage]int{}
		for _, t := range r.Targets {
			stages[t.Stage]++
		}
		verdict := "unusable"
		if r.Usable() {
			verdict = "usable"
		}
		score := "unreachable"
		if s := r.Score(); s != nodecheck.Unreachable {
			score = fmt.Sprintf("%dms", s)
		}
		d.emitLog(LogInfo, fmt.Sprintf("check_nodes: %s %s, median %s (ok=%d dial=%d handshake=%d probe=%d)",
			r.NodeID, verdict, score,
			stages[nodecheck.StageOK], stages[nodecheck.StageDial],
			stages[nodecheck.StageHandshake], stages[nodecheck.StageProbe]))
		for _, t := range r.Targets {
			d.emitDebug(fmt.Sprintf("check_nodes: %s %s -> %s (%dms)", r.NodeID, t.Target, t.Stage, t.RTTMs))
		}
	}
}

// waitForProbeListeners blocks until every probe port completes this run's
// authenticated, no-egress SOCKS5 handshake, the process exits, or the budget
// runs out.
//
// Without it the first targets are measured against a process that has not
// finished starting, and every node scores a failure it did not earn — the same
// class of mistake that once labelled twelve working bypass strategies "did not
// start".
func (d *Daemon) waitForProbeListeners(ctx context.Context, bindings []singbox.ProbeBinding, done <-chan error) (bool, error) {
	deadline := time.Now().Add(checkListenerWait)
	sawForeignListener := false
	for time.Now().Before(deadline) {
		select {
		case err, ok := <-done:
			return isProbeBindCollision(err, nil), probeExitedError(err, ok)
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
		all := true
		for _, b := range bindings {
			owned, listening := probeListenerOwnership(ctx, b)
			if !owned {
				all = false
				sawForeignListener = sawForeignListener || listening
				break
			}
		}
		if all {
			// Do not let a process that died just after its final auth response be
			// promoted to ready. A non-blocking exit read closes that last race.
			select {
			case err, ok := <-done:
				return isProbeBindCollision(err, nil), probeExitedError(err, ok)
			default:
				return false, nil
			}
		}
		select {
		case err, ok := <-done:
			return isProbeBindCollision(err, nil), probeExitedError(err, ok)
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return sawForeignListener, fmt.Errorf("probe listeners did not authenticate within %s", checkListenerWait)
}

// probeListenerOwned authenticates to a mixed inbound over SOCKS5 and stops
// before issuing CONNECT. That proves the listener knows this run's random
// secret without sending a byte toward a node or any external destination.
func probeListenerOwned(ctx context.Context, binding singbox.ProbeBinding) bool {
	owned, _ := probeListenerOwnership(ctx, binding)
	return owned
}

// probeListenerOwnership additionally reports whether something accepted TCP.
// A listener that answers but rejects the run's random auth is evidence that the
// release-to-spawn race was lost and a new port block should be tried.
func probeListenerOwnership(ctx context.Context, binding singbox.ProbeBinding) (owned, listening bool) {
	if binding.Username == "" || binding.Password == "" || len(binding.Username) > 255 || len(binding.Password) > 255 {
		return false, false
	}
	dialCtx, cancel := context.WithTimeout(ctx, probeListenerIOTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(binding.Port)))
	if err != nil {
		return false, false
	}
	defer conn.Close()
	listening = true
	if deadline, ok := dialCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	// Offer only username/password. A no-auth SOCKS server cannot select a
	// different method from the offered set and therefore cannot look like ours.
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		return false, true
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[0] != 5 || reply[1] != 2 {
		return false, true
	}
	auth := make([]byte, 0, 3+len(binding.Username)+len(binding.Password))
	auth = append(auth, 1, byte(len(binding.Username)))
	auth = append(auth, binding.Username...)
	auth = append(auth, byte(len(binding.Password)))
	auth = append(auth, binding.Password...)
	if _, err := conn.Write(auth); err != nil {
		return false, true
	}
	if _, err := io.ReadFull(conn, reply); err != nil {
		return false, true
	}
	return reply[0] == 1 && reply[1] == 0, true
}

type probePortReservation struct {
	base      int
	listeners []net.Listener
}

func (r *probePortReservation) release() {
	for _, l := range r.listeners {
		_ = l.Close()
	}
	r.listeners = nil
}

type probePortSpan struct {
	first int
	last  int
}

// reserveProbePortBlock proves that every port in one contiguous loopback block
// can be bound at the same time and holds the sockets until immediately before
// sing-box starts. A preferred block keeps normal runs stable; fallback search
// moves away from an occupied or previously raced block without trusting a
// connect-only availability check.
func reserveProbePortBlock(count, preferred int, tried []probePortSpan) (*probePortReservation, error) {
	if count < 1 {
		return nil, errors.New("probe needs at least one listener")
	}

	candidates := make([]int, 0, probePortSearchLimit+1)
	if preferred >= 1 && preferred+count-1 <= 65535 {
		candidates = append(candidates, preferred)
	}
	for base := probeFallbackPortMin; base+count-1 <= probeFallbackPortMax && len(candidates) < probePortSearchLimit+1; base++ {
		if base != preferred {
			candidates = append(candidates, base)
		}
	}

	var lastErr error
	for _, base := range candidates {
		candidate := probePortSpan{first: base, last: base + count - 1}
		if overlapsProbeSpan(candidate, tried) {
			continue
		}
		listeners := make([]net.Listener, 0, count)
		for port := candidate.first; port <= candidate.last; port++ {
			l, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				lastErr = err
				for _, opened := range listeners {
					_ = opened.Close()
				}
				listeners = nil
				break
			}
			listeners = append(listeners, l)
		}
		if len(listeners) == count {
			return &probePortReservation{base: base, listeners: listeners}, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no candidate block remained")
	}
	return nil, fmt.Errorf("reserve %d contiguous loopback port(s): %w", count, lastErr)
}

func overlapsProbeSpan(candidate probePortSpan, prior []probePortSpan) bool {
	for _, used := range prior {
		if candidate.first <= used.last && used.first <= candidate.last {
			return true
		}
	}
	return false
}

func probeExitedError(err error, ok bool) error {
	if !ok {
		return errors.New("probe process exit channel closed without a status")
	}
	if err == nil {
		return errors.New("probe process exited unexpectedly with a clean status")
	}
	return fmt.Errorf("probe process exited: %w", err)
}

// isProbeBindCollision recognises the cross-platform diagnostics emitted when a
// port was claimed after reservation release. Only that local, transient start
// failure earns another process; invalid configs and missing binaries fail once
// with their real explanation.
func isProbeBindCollision(err error, logs []string) bool {
	parts := make([]string, 0, len(logs)+1)
	if err != nil {
		parts = append(parts, err.Error())
	}
	parts = append(parts, logs...)
	text := strings.ToLower(strings.Join(parts, "\n"))
	for _, marker := range []string{
		"eaddrinuse",
		"wsaeaddrinuse",
		"address already in use",
		"address is already in use",
		"only one usage of each socket address",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// probeFailureMessage is the single wire/log representation of a local probe
// failure. It keeps only a bounded tail, flattens oversized/multiline entries,
// and then applies the daemon's established secret scrubber before anything can
// reach the UI or diagnostics ring.
func probeFailureMessage(phase string, cause error, runner Runner) string {
	why := "unknown local failure"
	if cause != nil {
		why = cause.Error()
	}
	message := fmt.Sprintf("check_nodes: local probe failed during %s: %s", phase, why)
	if runner != nil {
		lines := runner.Logs()
		if len(lines) > probeFailureTailLines {
			lines = lines[len(lines)-probeFailureTailLines:]
		}
		clean := make([]string, 0, len(lines))
		for _, line := range lines {
			line = strings.Join(strings.Fields(line), " ")
			if line == "" {
				continue
			}
			clean = append(clean, truncateRunes(line, probeFailureLineRunes))
		}
		if len(clean) > 0 {
			message += "; probe output: " + strings.Join(clean, " | ")
		}
	}
	return truncateRunes(scrubSecrets(message), probeFailureMaxRunes)
}

func truncateRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	if max < 2 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "…"
}

// probeBindings measures every node, at most checkFanout at a time, and returns
// one NodeResult per node in binding order.
//
// Every node is named before any of them is measured, so a run that spends its
// budget still reports the ones it never reached — with no targets, which both
// Usable and Score already read as "not measured, not usable" — rather than
// dropping them from the answer or naming them with an empty id.
func (d *Daemon) probeBindings(ctx context.Context, p profile.Profile, bindings []singbox.ProbeBinding, done <-chan error) ([]nodecheck.NodeResult, error) {
	measureCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]nodecheck.NodeResult, len(bindings))
	servers := make([]profile.Server, len(bindings))
	var exitErr error
	for i, b := range bindings {
		id := b.Tag
		if b.Index >= 0 && b.Index < len(p.Servers) {
			servers[i] = p.Servers[b.Index]
			id = servers[i].ID
		}
		// An empty, non-nil slice: the UI iterates this field, and a JSON null
		// there is a crash rather than an empty row.
		results[i] = nodecheck.NodeResult{NodeID: id, Targets: []nodecheck.TargetResult{}}
	}

	sem := make(chan struct{}, checkFanout)
	var wg sync.WaitGroup

launch:
	for i, b := range bindings {
		// Out of budget: the remaining nodes stay unmeasured rather than the run
		// carrying on past the deadline its caller was promised.
		if measureCtx.Err() != nil {
			break
		}
		select {
		case err, ok := <-done:
			exitErr = probeExitedError(err, ok)
			cancel()
			break launch
		case <-measureCtx.Done():
			break launch
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int, binding singbox.ProbeBinding, srv profile.Server) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i].Targets = d.probeNode(measureCtx, binding, srv)
		}(i, b, servers[i])
	}
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	if exitErr != nil {
		<-finished
		return nil, exitErr
	}
	select {
	case err, ok := <-done:
		cancel()
		<-finished
		return nil, probeExitedError(err, ok)
	case <-finished:
		// The process may have exited in the instant the last worker completed.
		select {
		case err, ok := <-done:
			return nil, probeExitedError(err, ok)
		default:
			return results, nil
		}
	}
}

// probeNode measures one node: every target through its loopback proxy, and a
// plain dial to the node's own address, all at once.
//
// At once because the targets are independent and the wait is entirely network:
// run in turn, four targets at checkProbeTimeout apiece cost a single dead node
// most of a run's budget, and a handful of dead nodes in a profile is exactly
// the state someone is in when they press this. Concurrency changes nothing
// about the verdict — the ordering carried no information — while the load a
// node sees, four cheap 204s at once, is less than opening one web page.
func (d *Daemon) probeNode(ctx context.Context, binding singbox.ProbeBinding, srv profile.Server) []nodecheck.TargetResult {
	// Whether the node's own address answers at all decides which failure the
	// targets get reported as: unreachable address is a different problem for the
	// user (routing, firewall, dead host) than an address that answers and then
	// carries nothing. Buffered, so the dial is never what the probes wait on.
	reachable := make(chan bool, 1)
	go func() { reachable <- srv.Server == "" || d.pingOne(ctx, srv).Ok }()

	measured := make([]bool, len(d.checkTargets))
	probed := make([]nodecheck.TargetResult, len(d.checkTargets))
	var wg sync.WaitGroup
	for i, t := range d.checkTargets {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int, t string) {
			defer wg.Done()
			stage, rtt := d.checkProbe(ctx, binding, t)
			probed[i] = nodecheck.TargetResult{Target: t, Stage: stage, RTTMs: rtt}
			measured[i] = true
		}(i, t)
	}
	wg.Wait()

	targets := make([]nodecheck.TargetResult, 0, len(probed))
	ok := 0
	for i := range probed {
		if !measured[i] {
			continue
		}
		if probed[i].Stage == nodecheck.StageOK {
			ok++
		}
		targets = append(targets, probed[i])
	}

	// The address failing to answer renames the failures — but only when nothing
	// came back through the node at all. A single target that completed proves
	// the address is reachable whatever the plain dial did, and the dial is not
	// evidence on its own: a UDP-carried node (Hysteria2, WireGuard) never
	// answers a TCP dial in the first place. Rewriting per-destination failures
	// as "address unreachable" there buries the honest verdict — the handshake or
	// probe stage the whole command exists to tell apart — under a wrong one, and
	// shows a working exit as a dead host.
	if ok == 0 && !<-reachable {
		for i := range targets {
			targets[i].Stage = nodecheck.StageDial
		}
	}
	return targets
}

// defaultCheckProbe runs one control request through the loopback proxy on port
// and reports how far it got.
//
// It drives the CONNECT by hand rather than handing the proxy to http.Transport,
// because the distinction the caller needs is invisible through a transport: a
// CONNECT that the proxy refuses means the tunnel to the node never established
// (dial or proxy handshake), while a request that fails after a successful
// CONNECT means the tunnel came up and traffic did not survive it. Collapsed into
// one "request failed" error, a black-hole node and an unreachable one look the
// same, and the UI can only show a red dot instead of saying what broke.
func (d *Daemon) defaultCheckProbe(ctx context.Context, binding singbox.ProbeBinding, target string) (nodecheck.Stage, int64) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return nodecheck.StageProbe, 0
	}
	host := u.Hostname()
	dest := net.JoinHostPort(host, portOrDefault(u))

	ctx, cancel := context.WithTimeout(ctx, checkProbeTimeout)
	defer cancel()

	start := d.now()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(binding.Port)))
	if err != nil {
		// The listener is ours; failing to reach it is not the node's fault, but the
		// node cannot be credited either.
		return nodecheck.StageHandshake, 0
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	proxyAuth := base64.StdEncoding.EncodeToString([]byte(binding.Username + ":" + binding.Password))
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", dest, dest, proxyAuth); err != nil {
		return nodecheck.StageHandshake, 0
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		// The proxy could not open the tunnel: the node accepted nothing, or accepted
		// the connection and never completed its own handshake.
		return nodecheck.StageHandshake, 0
	}

	tlsConn := tls.Client(conn, &tls.Config{ServerName: host})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nodecheck.StageProbe, 0
	}
	path := u.RequestURI()
	if _, err := fmt.Fprintf(tlsConn, "GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: tenebra-nodecheck\r\nConnection: close\r\n\r\n", path, host); err != nil {
		return nodecheck.StageProbe, 0
	}
	// Time to first byte of the response, which is what "does traffic survive this
	// node" actually costs the user. Any status counts: a censored destination
	// fails by timing out or resetting, not by answering 403.
	if _, err := http.ReadResponse(bufio.NewReader(tlsConn), nil); err != nil {
		return nodecheck.StageProbe, 0
	}
	rtt := d.now().Sub(start).Milliseconds()
	if rtt <= 0 {
		rtt = 1
	}
	return nodecheck.StageOK, rtt
}

// portOrDefault returns the URL's port, or the scheme's default.
func portOrDefault(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "http" {
		return "80"
	}
	return "443"
}
