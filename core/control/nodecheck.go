package control

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
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

// defaultCheckBasePort is where the per-node probe listeners start.
//
// High, unprivileged, and outside the ports this app already uses (the mixed
// inbound's 2081 and the clash API's 9090), so a check run cannot collide with
// the live tunnel it is running beside.
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
		return newError(req.ID, "check_nodes: probe runner not configured")
	}

	// One run at a time for the whole daemon. The probe process binds a fixed
	// range of loopback ports, so a second run started while the first still
	// holds them would fail to bind and report every node dead — a measurement
	// that lies is worse than one that is refused. The UI collapses its own
	// double-presses, but it is not the only caller: a session displaced
	// mid-check (the UI restarting) leaves its run unwinding while the new client
	// is already able to ask for another.
	if !d.checkRunning.CompareAndSwap(false, true) {
		return newError(req.ID, "check_nodes: a check is already running")
	}
	defer d.checkRunning.Store(false)

	// Everything below is bounded by one budget, and overrunning it truncates the
	// run rather than failing it (see defaultCheckBudget).
	ctx, cancel := context.WithTimeout(ctx, d.checkBudget)
	defer cancel()

	nodes := make([]model.Node, 0, len(p.Servers))
	for _, s := range p.Servers {
		nodes = append(nodes, s.Node)
	}
	cfg, bindings, err := singbox.BuildProbe(nodes, d.checkBasePort)
	if err != nil {
		return newError(req.ID, fmt.Sprintf("check_nodes: %v", err))
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return newError(req.ID, fmt.Sprintf("check_nodes: encode probe config: %v", err))
	}

	runner := d.probeRunner()
	if err := runner.Start(ctx, raw); err != nil {
		return newError(req.ID, fmt.Sprintf("check_nodes: start probe: %v", err))
	}
	// The probe process is ours alone and must not outlive the command, including
	// when the caller cancels: a stranded sing-box holding loopback ports would
	// make the next run fail to bind.
	defer func() { _ = runner.Stop() }()

	if !d.waitForProbeListeners(ctx, bindings) {
		return newError(req.ID, "check_nodes: probe listeners never came up")
	}

	results := d.probeBindings(ctx, p, bindings)

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
	}

	resp, err := newResult(req.ID, out)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// waitForProbeListeners blocks until every probe port accepts a connection, or
// the budget runs out.
//
// Without it the first targets are measured against a process that has not
// finished starting, and every node scores a failure it did not earn — the same
// class of mistake that once labelled twelve working bypass strategies "did not
// start".
func (d *Daemon) waitForProbeListeners(ctx context.Context, bindings []singbox.ProbeBinding) bool {
	deadline := time.Now().Add(checkListenerWait)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		all := true
		for _, b := range bindings {
			c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(b.Port)), 300*time.Millisecond)
			if err != nil {
				all = false
				break
			}
			_ = c.Close()
		}
		if all {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return false
}

// probeBindings measures every node, at most checkFanout at a time, and returns
// one NodeResult per node in binding order.
//
// Every node is named before any of them is measured, so a run that spends its
// budget still reports the ones it never reached — with no targets, which both
// Usable and Score already read as "not measured, not usable" — rather than
// dropping them from the answer or naming them with an empty id.
func (d *Daemon) probeBindings(ctx context.Context, p profile.Profile, bindings []singbox.ProbeBinding) []nodecheck.NodeResult {
	results := make([]nodecheck.NodeResult, len(bindings))
	servers := make([]profile.Server, len(bindings))
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

	for i, b := range bindings {
		// Out of budget: the remaining nodes stay unmeasured rather than the run
		// carrying on past the deadline its caller was promised.
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(i, port int, srv profile.Server) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i].Targets = d.probeNode(ctx, port, srv)
		}(i, b.Port, servers[i])
	}
	wg.Wait()
	return results
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
func (d *Daemon) probeNode(ctx context.Context, port int, srv profile.Server) []nodecheck.TargetResult {
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
			stage, rtt := d.checkProbe(ctx, port, t)
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
func (d *Daemon) defaultCheckProbe(ctx context.Context, port int, target string) (nodecheck.Stage, int64) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return nodecheck.StageProbe, 0
	}
	host := u.Hostname()
	dest := net.JoinHostPort(host, portOrDefault(u))

	ctx, cancel := context.WithTimeout(ctx, checkProbeTimeout)
	defer cancel()

	start := d.now()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		// The listener is ours; failing to reach it is not the node's fault, but the
		// node cannot be credited either.
		return nodecheck.StageHandshake, 0
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", dest, dest); err != nil {
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
