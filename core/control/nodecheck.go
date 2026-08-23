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

	d.emitLog(LogInfo, fmt.Sprintf("check_nodes: measuring %d node(s) of %q against %d target(s)",
		len(bindings), p.Name, len(d.checkTargets)))

	runner := d.probeRunner()
	if err := runner.Start(ctx, raw); err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("check_nodes: the probe sing-box would not start: %v", err))
		return newError(req.ID, fmt.Sprintf("check_nodes: start probe: %v", err))
	}
	// The probe process is ours alone and must not outlive the command, including
	// when the caller cancels: a stranded sing-box holding loopback ports would
	// make the next run fail to bind.
	defer func() { _ = runner.Stop() }()

	if !d.waitForProbeListeners(ctx, bindings) {
		// This one is worth naming precisely: every node would otherwise score a
		// failure it did not earn, and the report would blame the exits for a
		// local process that never bound its ports.
		d.emitLog(LogWarn, fmt.Sprintf("check_nodes: the probe's loopback listeners never came up within %s; no node was actually measured",
			checkListenerWait))
		return newError(req.ID, "check_nodes: probe listeners never came up")
	}

	results := d.probeBindings(ctx, p, bindings)
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
func (d *Daemon) probeBindings(ctx context.Context, p profile.Profile, bindings []singbox.ProbeBinding) []nodecheck.NodeResult {
	results := make([]nodecheck.NodeResult, len(bindings))
	sem := make(chan struct{}, checkFanout)
	var wg sync.WaitGroup

	for i, b := range bindings {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, b singbox.ProbeBinding) {
			defer wg.Done()
			defer func() { <-sem }()

			id := b.Tag
			var srv profile.Server
			if b.Index >= 0 && b.Index < len(p.Servers) {
				srv = p.Servers[b.Index]
				id = srv.ID
			}

			// Whether the node's own address answers at all decides which failure the
			// targets get reported as: unreachable address is a different problem for
			// the user (routing, firewall, dead host) than an address that answers and
			// then carries nothing.
			reachable := srv.Server == "" || d.pingOne(ctx, srv).Ok

			targets := make([]nodecheck.TargetResult, 0, len(d.checkTargets))
			for _, t := range d.checkTargets {
				if ctx.Err() != nil {
					break
				}
				stage, rtt := d.checkProbe(ctx, b.Port, t)
				if stage != nodecheck.StageOK && !reachable {
					stage = nodecheck.StageDial
				}
				targets = append(targets, nodecheck.TargetResult{Target: t, Stage: stage, RTTMs: rtt})
			}
			results[i] = nodecheck.NodeResult{NodeID: id, Targets: targets}
		}(i, b)
	}
	wg.Wait()
	return results
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
