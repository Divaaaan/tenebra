package control

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodecheck"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// checkHarness wires a daemon whose probe run needs neither a network nor a
// sing-box: the probe "process" is a fake runner, the listeners perform only the
// authenticated SOCKS5 preflight (never CONNECT), and the per-target verdict
// comes from a table the test writes.
type checkHarness struct {
	*harness
	probe         *checkProbeRunner
	preferredBase int
}

// checkProbeRunner is a local, no-egress stand-in for sing-box. It binds the
// listeners from the rendered config and implements only the SOCKS5
// authentication exchange: readiness can prove the process owns each port, but
// the fake never receives a CONNECT command and therefore never reaches a node.
type checkProbeRunner struct {
	*fakeRunner
	listenerMu         sync.Mutex
	listeners          []net.Listener
	bases              []int
	onStart            func(context.Context, []byte, *checkProbeRunner) error
	exitOnStartContext bool
	startContextDone   chan struct{}
}

func newCheckProbeRunner() *checkProbeRunner {
	return &checkProbeRunner{fakeRunner: newFakeRunner()}
}

func (r *checkProbeRunner) Start(ctx context.Context, configJSON []byte) error {
	if err := r.fakeRunner.Start(ctx, configJSON); err != nil {
		return err
	}
	var err error
	if r.onStart != nil {
		err = r.onStart(ctx, configJSON, r)
	} else {
		err = r.serveConfig(configJSON)
	}
	if err != nil {
		return err
	}
	if r.exitOnStartContext {
		r.startContextDone = make(chan struct{})
		go func() {
			<-ctx.Done()
			r.exit(ctx.Err())
			close(r.startContextDone)
		}()
	}
	return nil
}

func (r *checkProbeRunner) serveConfig(configJSON []byte) error {
	var cfg struct {
		Inbounds []struct {
			ListenPort int `json:"listen_port"`
			Users      []struct {
				Username string `json:"username"`
				Password string `json:"password"`
			} `json:"users"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return err
	}
	if len(cfg.Inbounds) == 0 {
		return errors.New("fake probe: no inbounds")
	}

	opened := make([]net.Listener, 0, len(cfg.Inbounds))
	for _, in := range cfg.Inbounds {
		if len(in.Users) != 1 || in.Users[0].Username == "" || in.Users[0].Password == "" {
			for _, prior := range opened {
				_ = prior.Close()
			}
			return errors.New("fake probe: inbound has no authentication")
		}
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(in.ListenPort)))
		if err != nil {
			for _, prior := range opened {
				_ = prior.Close()
			}
			return err
		}
		opened = append(opened, l)
		go serveProbeAuth(l, in.Users[0].Username, in.Users[0].Password)
	}

	r.listenerMu.Lock()
	r.listeners = append(r.listeners, opened...)
	r.bases = append(r.bases, cfg.Inbounds[0].ListenPort)
	r.listenerMu.Unlock()
	return nil
}

func (r *checkProbeRunner) Stop() error {
	r.listenerMu.Lock()
	listeners := r.listeners
	r.listeners = nil
	r.listenerMu.Unlock()
	for _, l := range listeners {
		_ = l.Close()
	}
	return r.fakeRunner.Stop()
}

func (r *checkProbeRunner) lastBase() int {
	r.listenerMu.Lock()
	defer r.listenerMu.Unlock()
	if len(r.bases) == 0 {
		return 0
	}
	return r.bases[len(r.bases)-1]
}

func serveProbeAuth(l net.Listener, username, password string) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go answerProbeAuth(conn, username, password)
	}
}

func answerProbeAuth(conn net.Conn, username, password string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	br := bufio.NewReader(conn)
	hello := make([]byte, 3)
	if _, err := io.ReadFull(br, hello); err != nil || hello[0] != 5 || hello[1] != 1 || hello[2] != 2 {
		return
	}
	if _, err := conn.Write([]byte{5, 2}); err != nil {
		return
	}
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil || head[0] != 1 {
		return
	}
	ub := make([]byte, int(head[1]))
	if _, err := io.ReadFull(br, ub); err != nil {
		return
	}
	plen, err := br.ReadByte()
	if err != nil {
		return
	}
	pb := make([]byte, int(plen))
	if _, err := io.ReadFull(br, pb); err != nil {
		return
	}
	status := byte(1)
	if string(ub) == username && string(pb) == password {
		status = 0
	}
	_, _ = conn.Write([]byte{1, status})
}

// newCheckHarness prepares a daemon with profile nodes and a probe runner that
// opens the authenticated listeners from the config handed to Start, satisfying
// readiness the way the real process does without any external traffic.
func newCheckHarness(t *testing.T, nodes []model.Node, basePort int) (*checkHarness, string) {
	t.Helper()
	h := newHarness(t)
	p := h.addProfile(nodes)

	probe := newCheckProbeRunner()
	h.daemon.SetProbeRunner(func() Runner { return probe })
	h.daemon.checkBasePort = basePort
	h.daemon.checkTargets = []string{"https://a.example/204", "https://b.example/204"}

	// None of these nodes exists, so the plain dial to a node's own address would
	// spend a real DNS lookup to find that out. Fail it outright and in no time:
	// that dial only decides how a failure is *named* (see probeNode), and every
	// test here that cares about the naming says so explicitly.
	h.daemon.dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no such host")
	}

	ch := &checkHarness{harness: h, probe: probe, preferredBase: basePort}
	t.Cleanup(func() { _ = probe.Stop() })
	return ch, p.ID
}

// verdicts installs a per-port outcome table: the port a node was bound to maps
// to the stage every one of its targets reports.
func (c *checkHarness) verdicts(table map[int]nodecheck.Stage, rtt map[int]int64) {
	var mu sync.Mutex
	c.daemon.checkProbe = func(_ context.Context, binding singbox.ProbeBinding, _ string) (nodecheck.Stage, int64) {
		mu.Lock()
		defer mu.Unlock()
		logicalPort := c.preferredBase + binding.Port - c.probe.lastBase()
		st, ok := table[logicalPort]
		if !ok {
			return nodecheck.StageProbe, 0
		}
		if st == nodecheck.StageOK {
			return st, rtt[logicalPort]
		}
		return st, 0
	}
}

// TestProbeListenerOwnershipRequiresMatchingAuthentication catches the exact
// false-ready path: accepting TCP is not ownership. An unrelated local SOCKS
// listener that selects no-auth must be rejected, while the per-run credentials
// rendered into our listener must complete only the auth exchange (no CONNECT).
func TestProbeListenerOwnershipRequiresMatchingAuthentication(t *testing.T) {
	ours, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ours.Close()
	go serveProbeAuth(ours, "probe-user", "probe-pass")
	port := ours.Addr().(*net.TCPAddr).Port
	if !probeListenerOwned(context.Background(), singbox.ProbeBinding{
		Port: port, Username: "probe-user", Password: "probe-pass",
	}) {
		t.Fatal("matching authenticated listener was not recognised")
	}
	if probeListenerOwned(context.Background(), singbox.ProbeBinding{
		Port: port, Username: "probe-user", Password: "wrong-pass",
	}) {
		t.Fatal("listener accepted with the wrong per-run password")
	}

	foreign, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	go func() {
		for {
			conn, err := foreign.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 3)
				_, _ = io.ReadFull(conn, buf)
				_, _ = conn.Write([]byte{5, 0}) // unrelated no-auth SOCKS server
			}()
		}
	}()
	if probeListenerOwned(context.Background(), singbox.ProbeBinding{
		Port: foreign.Addr().(*net.TCPAddr).Port, Username: "probe-user", Password: "probe-pass",
	}) {
		t.Fatal("an unrelated listener was mistaken for the probe process")
	}
}

// TestDefaultCheckProbeAuthenticatesItsCONNECT protects the handoff from
// authenticated readiness to real measurement. Requiring credentials only in
// the sing-box config would make every subsequent CONNECT earn a 407 and label
// every healthy node dead.
func TestDefaultCheckProbeAuthenticatesItsCONNECT(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	header := make(chan string, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			header <- "read-error: " + err.Error()
			return
		}
		header <- req.Header.Get("Proxy-Authorization")
		_, _ = io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
	}()

	h := newHarness(t)
	port := l.Addr().(*net.TCPAddr).Port
	h.daemon.defaultCheckProbe(context.Background(), singbox.ProbeBinding{
		Port: port, Username: "probe-user", Password: "probe-pass",
	}, "https://example.test/204")
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("probe-user:probe-pass"))
	if got := <-header; got != want {
		t.Fatalf("Proxy-Authorization = %q, want %q", got, want)
	}
}

// TestCheckNodesMovesOffAnOccupiedPreferredPort catches the fixed-port incident:
// a local process already owning 24310 used to make sing-box fail after spawn,
// which was then reported as every remote node being unavailable.
func TestCheckNodesMovesOffAnOccupiedPreferredPort(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	preferred := blocker.Addr().(*net.TCPAddr).Port - 1
	if preferred < 1 {
		t.Skip("OS selected port 1; cannot place the blocker inside a two-port range")
	}

	h, pid := newCheckHarness(t, []model.Node{
		vlessNode("A", "a.example.11"),
		vlessNode("B", "b.example.22"),
	}, preferred)
	h.verdicts(
		map[int]nodecheck.Stage{preferred: nodecheck.StageOK, preferred + 1: nodecheck.StageOK},
		map[int]int64{preferred: 20, preferred + 1: 25},
	)
	h.run(t, pid)

	if got := h.probe.lastBase(); got == preferred {
		t.Fatalf("probe still used occupied preferred port %d", preferred)
	}
}

// TestCheckNodesRetriesAnAsyncBindCollision models the narrow race between
// releasing a verified-free block and sing-box binding it. The first process
// exits with the platform's bind diagnostic; the second attempt must use a new
// block and produce a measurement, not node-level failures.
func TestCheckNodesRetriesAnAsyncBindCollision(t *testing.T) {
	const preferred = 24820
	h, pid := newCheckHarness(t, []model.Node{vlessNode("A", "a.example.11")}, preferred)
	first := newCheckProbeRunner()
	first.onStart = func(_ context.Context, _ []byte, r *checkProbeRunner) error {
		r.setLogs("FATAL listen tcp 127.0.0.1: bind: address already in use")
		go r.exit(errors.New("exit status 1"))
		return nil
	}
	second := newCheckProbeRunner()
	h.probe = second
	h.verdicts(map[int]nodecheck.Stage{preferred: nodecheck.StageOK}, map[int]int64{preferred: 25})
	var factoryCalls int
	h.daemon.SetProbeRunner(func() Runner {
		factoryCalls++
		if factoryCalls == 1 {
			return first
		}
		return second
	})
	h.daemon.checkBudget = 2 * time.Second

	h.run(t, pid)
	if factoryCalls != 2 {
		t.Fatalf("probe runner factory called %d times, want 2", factoryCalls)
	}
	firstCfgs, secondCfgs := first.startCfgs(), second.startCfgs()
	if len(firstCfgs) != 1 || len(secondCfgs) != 1 {
		t.Fatalf("start configs: first=%d second=%d, want one each", len(firstCfgs), len(secondCfgs))
	}
	if probeConfigBase(t, firstCfgs[0]) == probeConfigBase(t, secondCfgs[0]) {
		t.Fatal("bind retry reused the collided port block")
	}
}

// TestCheckNodesReportsAndScrubsProbeExit ensures a local process death is never
// converted into per-node verdicts. The RPC error and daemon log retain the exit
// plus the useful tail, while UUID-shaped credentials are masked.
func TestCheckNodesReportsAndScrubsProbeExit(t *testing.T) {
	const leaked = "11111111-2222-4333-8444-555555555555"
	h, pid := newCheckHarness(t, []model.Node{vlessNode("A", "a.example.11")}, 24830)
	var generated []string
	h.probe.onStart = func(_ context.Context, raw []byte, r *checkProbeRunner) error {
		var cfg struct {
			Inbounds []struct {
				Users []struct {
					Username string `json:"username"`
					Password string `json:"password"`
				} `json:"users"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("decode generated auth: %w", err)
		}
		if len(cfg.Inbounds) == 0 || len(cfg.Inbounds[0].Users) != 1 {
			return errors.New("generated config has no probe auth")
		}
		generated = []string{cfg.Inbounds[0].Users[0].Username, cfg.Inbounds[0].Users[0].Password}
		r.setLogs(
			"config loaded for user="+generated[0],
			"FATAL password="+generated[1]+" credential="+leaked+" could not bind",
		)
		go r.exit(errors.New("exit status 1"))
		return nil
	}
	h.daemon.checkBudget = 2 * time.Second
	probes := 0
	h.daemon.checkProbe = func(context.Context, singbox.ProbeBinding, string) (nodecheck.Stage, int64) {
		probes++
		return nodecheck.StageOK, 1
	}

	start := time.Now()
	resp := h.daemon.handleCheckNodes(context.Background(), Request{ID: 1, Cmd: CmdCheckNodes, Profile: pid})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("local probe exit took %v to report", elapsed)
	}
	if resp.Error == "" {
		t.Fatal("probe exit returned a successful node report")
	}
	for _, want := range []string{"exit status 1", "could not bind", "***"} {
		if !strings.Contains(resp.Error, want) {
			t.Errorf("RPC error %q does not contain %q", resp.Error, want)
		}
	}
	for _, secret := range append(generated, leaked) {
		if secret != "" && strings.Contains(resp.Error, secret) {
			t.Fatalf("RPC error leaked credential-like text: %s", resp.Error)
		}
	}
	if probes != 0 {
		t.Fatalf("measured %d node targets after local probe death", probes)
	}
	logs := h.daemon.logs.snapshot()
	joined := ""
	for _, entry := range logs {
		joined += entry.Msg + "\n"
	}
	if !strings.Contains(joined, "exit status 1") || !strings.Contains(joined, "could not bind") {
		t.Fatalf("daemon log lost the exit/tail: %s", joined)
	}
	for _, secret := range append(generated, leaked) {
		if secret != "" && strings.Contains(joined, secret) {
			t.Fatalf("daemon log leaked credential-like text: %s", joined)
		}
	}
}

// TestCheckNodesAbortsWhenProbeDiesDuringMeasurement covers the second half of
// the Done contract: a process can die after readiness. In-flight target work
// must be cancelled and the whole command must fail locally rather than return a
// partially fabricated list of dead nodes.
func TestCheckNodesAbortsWhenProbeDiesDuringMeasurement(t *testing.T) {
	h, pid := newCheckHarness(t, []model.Node{vlessNode("A", "a.example.11")}, 24840)
	h.daemon.checkBudget = 2 * time.Second
	entered := make(chan struct{})
	var once sync.Once
	h.daemon.checkProbe = func(ctx context.Context, _ singbox.ProbeBinding, _ string) (nodecheck.Stage, int64) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return nodecheck.StageProbe, 0
	}
	go func() {
		<-entered
		h.probe.setLogs("fatal: runtime crash")
		h.probe.exit(errors.New("exit status 2"))
	}()

	start := time.Now()
	resp := h.daemon.handleCheckNodes(context.Background(), Request{ID: 1, Cmd: CmdCheckNodes, Profile: pid})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("mid-measurement probe exit took %v to report", elapsed)
	}
	if resp.Error == "" || !strings.Contains(resp.Error, "exit status 2") {
		t.Fatalf("mid-measurement exit returned %q, want local process error", resp.Error)
	}
}

// TestCheckNodesBudgetDoesNotOwnProbeProcessLifetime catches a race between the
// intentional partial-result budget and exec.CommandContext. Expiring the
// measurement budget must stop scheduling/probing and return what completed; it
// must not kill sing-box underneath that collection and turn the same timeout
// into a local-process failure. The separately owned process context is still
// cancelled explicitly once the command has its result.
func TestCheckNodesBudgetDoesNotOwnProbeProcessLifetime(t *testing.T) {
	const (
		preferred = 24850
		budget    = 100 * time.Millisecond
	)
	h, pid := newCheckHarness(t, manyNodes(12), preferred)
	h.probe.exitOnStartContext = true
	h.daemon.checkBudget = budget
	h.daemon.checkTargets = []string{"https://a.example/204"}
	h.daemon.checkProbe = func(ctx context.Context, _ singbox.ProbeBinding, _ string) (nodecheck.Stage, int64) {
		<-ctx.Done()
		// Make the process-context exit deterministic under the old wiring: its
		// Done signal is queued before the measurement workers finish.
		time.Sleep(50 * time.Millisecond)
		return nodecheck.StageProbe, 0
	}

	resp := h.daemon.handleCheckNodes(context.Background(), Request{ID: 1, Cmd: CmdCheckNodes, Profile: pid})
	if resp.Error != "" {
		t.Fatalf("ordinary measurement budget became a local probe failure: %s", resp.Error)
	}
	var out checkReply
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("decode partial result: %v", err)
	}
	if len(out.Results) != 12 {
		t.Fatalf("partial result contains %d nodes, want all 12 named", len(out.Results))
	}
	unmeasured := 0
	for _, result := range out.Results {
		if len(result.Targets) == 0 {
			unmeasured++
		}
	}
	if unmeasured == 0 {
		t.Fatal("budget expiry measured every node instead of returning a partial result")
	}
	select {
	case <-h.probe.startContextDone:
	case <-time.After(time.Second):
		t.Fatal("probe process lifetime context was not cancelled after the partial result")
	}
}

func TestProbeBindCollisionClassifierIsSpecific(t *testing.T) {
	tests := []struct {
		name string
		err  error
		logs []string
		want bool
	}{
		{name: "linux errno name", logs: []string{"listen tcp: EADDRINUSE"}, want: true},
		{name: "windows errno name", logs: []string{"listen tcp: WSAEADDRINUSE"}, want: true},
		{name: "unix message", err: errors.New("bind: address already in use"), want: true},
		{name: "windows message", logs: []string{"Only one usage of each socket address is normally permitted"}, want: true},
		{name: "generic bind failure", logs: []string{"failed to bind listener: permission denied"}},
		{name: "wrong local address", logs: []string{"listen tcp 127.0.0.1: cannot assign requested address"}},
		{name: "generic listen failure", logs: []string{"listen tcp 127.0.0.1: permission denied"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isProbeBindCollision(tt.err, tt.logs); got != tt.want {
				t.Fatalf("isProbeBindCollision(%v, %q) = %t, want %t", tt.err, tt.logs, got, tt.want)
			}
		})
	}
}

func probeConfigBase(t *testing.T, raw []byte) int {
	t.Helper()
	var cfg struct {
		Inbounds []struct {
			ListenPort int `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode probe config: %v", err)
	}
	if len(cfg.Inbounds) == 0 {
		t.Fatal("probe config has no inbounds")
	}
	return cfg.Inbounds[0].ListenPort
}

type checkReply struct {
	Results []nodecheck.NodeResult `json:"results"`
	Best    string                 `json:"best"`
}

func (c *checkHarness) run(t *testing.T, profileID string) checkReply {
	t.Helper()
	resp := c.daemon.handleCheckNodes(context.Background(), Request{ID: 1, Cmd: CmdCheckNodes, Profile: profileID})
	if resp.Error != "" {
		t.Fatalf("check_nodes: %s", resp.Error)
	}
	var out checkReply
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return out
}

// TestCheckNodesPrefersWhatWorksOverWhatIsFast is the whole reason this command
// exists: on 2026-08-18 an exit accepted TCP instantly and then carried nothing,
// so a latency-ranked auto-select handed traffic to a black hole. A node that
// answers slowly must outrank a fast one that answers nothing.
func TestCheckNodesPrefersWhatWorksOverWhatIsFast(t *testing.T) {
	nodes := []model.Node{vlessNode("Fast-Dead", "a.example.11"), vlessNode("Slow-Alive", "b.example.22")}
	h, pid := newCheckHarness(t, nodes, 24610)
	h.verdicts(
		map[int]nodecheck.Stage{24610: nodecheck.StageProbe, 24611: nodecheck.StageOK},
		map[int]int64{24611: 400},
	)

	got := h.run(t, pid)

	if len(got.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(got.Results))
	}
	if !got.Results[0].Usable() {
		t.Error("the ranked-first node is not usable")
	}
	if got.Results[0].NodeID == got.Results[1].NodeID {
		t.Fatal("both results carry the same node id")
	}
	if got.Best == "" {
		t.Fatal("no best node reported while one is usable")
	}
	if got.Best != got.Results[0].NodeID {
		t.Errorf("best = %q but ranking leads with %q", got.Best, got.Results[0].NodeID)
	}
}

// TestCheckNodesReportsNoBestWhenEveryNodeIsBroken: with every exit dead there is
// no meaningful "fastest", and answering with the least-bad one would reproduce
// exactly the bug this path exists to prevent.
func TestCheckNodesReportsNoBestWhenEveryNodeIsBroken(t *testing.T) {
	nodes := []model.Node{vlessNode("A", "a.example.11"), vlessNode("B", "b.example.22")}
	h, pid := newCheckHarness(t, nodes, 24620)
	h.verdicts(map[int]nodecheck.Stage{24620: nodecheck.StageProbe, 24621: nodecheck.StageHandshake}, nil)

	got := h.run(t, pid)

	if got.Best != "" {
		t.Errorf("best = %q, want empty: nothing works", got.Best)
	}
	for _, r := range got.Results {
		if r.Usable() {
			t.Errorf("node %s reported usable with every target failing", r.NodeID)
		}
	}
}

// TestCheckNodesStopsItsOwnProcess: the probe holds loopback ports and runs
// beside the live tunnel. Leaving it behind would make the next run fail to bind
// and leave a stray sing-box on the user's machine.
func TestCheckNodesStopsItsOwnProcess(t *testing.T) {
	nodes := []model.Node{vlessNode("A", "a.example.11")}
	h, pid := newCheckHarness(t, nodes, 24630)
	h.verdicts(map[int]nodecheck.Stage{24630: nodecheck.StageOK}, map[int]int64{24630: 30})

	h.run(t, pid)

	if h.probe.starts() != 1 {
		t.Errorf("probe started %d times, want 1", h.probe.starts())
	}
	if h.probe.stops() < 1 {
		t.Error("probe process was left running")
	}
	if h.runner.starts() != 0 {
		t.Error("the check touched the tunnel's own runner")
	}
}

// TestCheckNodesProbeConfigNeverTouchesTheRoutingTable: the check runs while a
// tunnel (possibly someone else's) is up. A second tun with its own default route
// is how a machine loses connectivity, so the config must carry neither.
func TestCheckNodesProbeConfigNeverTouchesTheRoutingTable(t *testing.T) {
	nodes := []model.Node{vlessNode("A", "a.example.11")}
	h, pid := newCheckHarness(t, nodes, 24640)
	h.verdicts(map[int]nodecheck.Stage{24640: nodecheck.StageOK}, map[int]int64{24640: 20})

	h.run(t, pid)

	cfgs := h.probe.startCfgs()
	if len(cfgs) != 1 {
		t.Fatalf("probe was started %d times", len(cfgs))
	}
	var cfg struct {
		Inbounds []struct {
			Type      string `json:"type"`
			AutoRoute bool   `json:"auto_route"`
		} `json:"inbounds"`
		Route struct {
			Final string `json:"final"`
		} `json:"route"`
	}
	if err := json.Unmarshal(cfgs[0], &cfg); err != nil {
		t.Fatalf("probe config is not valid JSON: %v", err)
	}
	for _, in := range cfg.Inbounds {
		if in.Type == "tun" || in.AutoRoute {
			t.Errorf("probe config raises a tun / auto_route inbound: %+v", in)
		}
	}
	// A probe request that misses its rule must fail, not egress unproxied: a
	// verdict measured over the direct link would certify a dead node as working.
	if cfg.Route.Final != "block" {
		t.Errorf("route.final = %q, want block", cfg.Route.Final)
	}
}

// TestCheckNodesNeedsAProbeRunner: without one the platform cannot run a second
// sing-box, and saying so plainly beats failing somewhere deeper.
func TestCheckNodesNeedsAProbeRunner(t *testing.T) {
	h := newHarness(t)
	p := h.addProfile([]model.Node{vlessNode("A", "a.example.11")})

	resp := h.daemon.handleCheckNodes(context.Background(), Request{ID: 1, Cmd: CmdCheckNodes, Profile: p.ID})
	if resp.Error == "" {
		t.Fatal("check_nodes succeeded with no probe runner configured")
	}
}
