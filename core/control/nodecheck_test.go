package control

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodecheck"
)

// checkHarness wires a daemon whose probe run needs neither a network nor a
// sing-box: the probe "process" is a fake runner, the listeners are real
// loopback sockets that accept and say nothing, and the per-target verdict comes
// from a table the test writes.
type checkHarness struct {
	*harness
	probe    *fakeRunner
	listener []net.Listener
}

// newCheckHarness prepares a daemon with profile nodes, a probe runner, and
// listeners opened on the ports BuildProbe will assign, so waitForProbeListeners
// is satisfied the way the real process would satisfy it.
func newCheckHarness(t *testing.T, nodes []model.Node, basePort int) (*checkHarness, string) {
	t.Helper()
	h := newHarness(t)
	p := h.addProfile(nodes)

	probe := newFakeRunner()
	h.daemon.SetProbeRunner(func() Runner { return probe })
	h.daemon.checkBasePort = basePort
	h.daemon.checkTargets = []string{"https://a.example/204", "https://b.example/204"}

	ch := &checkHarness{harness: h, probe: probe}
	for i := range nodes {
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(basePort+i)))
		if err != nil {
			t.Fatalf("open fake probe listener: %v", err)
		}
		ch.listener = append(ch.listener, l)
	}
	t.Cleanup(func() {
		for _, l := range ch.listener {
			_ = l.Close()
		}
	})
	return ch, p.ID
}

// verdicts installs a per-port outcome table: the port a node was bound to maps
// to the stage every one of its targets reports.
func (c *checkHarness) verdicts(table map[int]nodecheck.Stage, rtt map[int]int64) {
	var mu sync.Mutex
	c.daemon.checkProbe = func(_ context.Context, port int, _ string) (nodecheck.Stage, int64) {
		mu.Lock()
		defer mu.Unlock()
		st, ok := table[port]
		if !ok {
			return nodecheck.StageProbe, 0
		}
		if st == nodecheck.StageOK {
			return st, rtt[port]
		}
		return st, 0
	}
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
