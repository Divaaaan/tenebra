package control

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodecheck"
)

// manyNodes builds n distinguishable profile nodes. Their addresses are never
// dialled for real (see newCheckHarness); what matters is that a profile this
// size takes more than one fanout wave to measure.
func manyNodes(n int) []model.Node {
	nodes := make([]model.Node, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, vlessNode(fmt.Sprintf("N%02d", i), fmt.Sprintf("n%02d.example.%02d", i, i)))
	}
	return nodes
}

// TestCheckNodesProbesOneNodesTargetsTogether: a node's targets are independent
// and the wait through them is entirely network, so measuring them in turn
// bought nothing and cost a dead node the sum of every target's timeout — four
// targets at checkProbeTimeout is over half a minute for one node, and a profile
// with a few dead exits is exactly the state someone is in when they press this.
func TestCheckNodesProbesOneNodesTargetsTogether(t *testing.T) {
	nodes := []model.Node{vlessNode("A", "a.example.11")}
	h, pid := newCheckHarness(t, nodes, 24650)
	h.daemon.checkTargets = []string{
		"https://a.example/204",
		"https://b.example/204",
		"https://c.example/204",
		"https://d.example/204",
	}
	want := len(h.daemon.checkTargets)

	// Each probe parks until every target of the node is in flight at once.
	// Measured one after another, none of them ever is: the first waits out the
	// fallback timeout below and the peak never rises above one.
	var (
		mu       sync.Mutex
		inFlight int
		peak     int
		once     sync.Once
	)
	together := make(chan struct{})
	h.daemon.checkProbe = func(_ context.Context, _ int, _ string) (nodecheck.Stage, int64) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		all := inFlight == want
		mu.Unlock()
		if all {
			once.Do(func() { close(together) })
		}
		select {
		case <-together:
		case <-time.After(2 * time.Second):
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		return nodecheck.StageOK, 20
	}

	got := h.run(t, pid)

	mu.Lock()
	reached := peak
	mu.Unlock()
	if reached != want {
		t.Errorf("at most %d of %d targets were ever in flight at once", reached, want)
	}
	if len(got.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(got.Results))
	}
	if n := len(got.Results[0].Targets); n != want {
		t.Errorf("node reports %d targets, want %d", n, want)
	}
}

// TestCheckNodesAnswersWithinItsBudgetWithWhatItMeasured is the freeze itself.
//
// Twelve nodes is three fanout waves; with every request through them timing out
// — the state that makes someone press "measure" in the first place — the run
// cost the sum of every wave's every target and took minutes. The daemon serves
// one request at a time and the desktop bridge abandons a request after 60s, so
// the caller was told the measurement failed while the daemon was still spending
// its uplink on it, and whatever was pressed next queued behind a run nobody was
// listening for any more.
//
// The budget is scaled down here; the shape is not. What must hold is that the
// command answers inside its budget, that the answer is a result rather than a
// failure, and that every node is named in it — measured or not.
func TestCheckNodesAnswersWithinItsBudgetWithWhatItMeasured(t *testing.T) {
	const (
		nodeCount = 12
		budget    = 500 * time.Millisecond
		perProbe  = 400 * time.Millisecond
	)
	h, pid := newCheckHarness(t, manyNodes(nodeCount), 24700)
	h.daemon.checkBudget = budget
	h.daemon.checkTargets = []string{
		"https://a.example/204",
		"https://b.example/204",
		"https://c.example/204",
		"https://d.example/204",
	}
	// A node that answers nothing: every request through it hangs until it is
	// abandoned.
	h.daemon.checkProbe = func(ctx context.Context, _ int, _ string) (nodecheck.Stage, int64) {
		select {
		case <-ctx.Done():
		case <-time.After(perProbe):
		}
		return nodecheck.StageProbe, 0
	}

	start := time.Now()
	got := h.run(t, pid)
	elapsed := time.Since(start)

	// Three waves of four targets, each waiting out its own timeout, is what the
	// sequential run cost. The ceiling here sits well above the budget and still
	// far below that.
	if ceiling := 5 * budget; elapsed > ceiling {
		t.Errorf("the check took %v on a %v budget; past %v is the freeze", elapsed, budget, ceiling)
	}
	if len(got.Results) != nodeCount {
		t.Fatalf("got %d results, want %d: a node the run never reached is still a node", len(got.Results), nodeCount)
	}
	unmeasured := 0
	for _, r := range got.Results {
		if r.NodeID == "" {
			t.Error("a result came back with no node id")
		}
		if len(r.Targets) == 0 {
			unmeasured++
		}
		if r.Usable() {
			t.Errorf("node %s reported usable with every request through it hanging", r.NodeID)
		}
	}
	if unmeasured == 0 {
		t.Error("every node was measured: the run ground through the whole profile instead of stopping on its budget")
	}
	if got.Best != "" {
		t.Errorf("best = %q, want empty: nothing carried anything", got.Best)
	}
}

// TestCheckNodesBudgetLeavesTheBridgeRoom: the desktop bridge gives up on any
// request after 60s (REQUEST_TIMEOUT in ui-desktop/src-tauri/src/backend/wire.rs)
// and the daemon serves one request at a time, so a check able to outlive that
// ceiling costs twice — the caller is told it failed, and whatever is pressed
// next waits out the rest of it. Half the ceiling leaves the other half to the
// connect the measurement was taken for.
func TestCheckNodesBudgetLeavesTheBridgeRoom(t *testing.T) {
	const bridgeRequestTimeout = 60 * time.Second
	if defaultCheckBudget*2 > bridgeRequestTimeout {
		t.Errorf("a %v check budget leaves less than half of the bridge's %v to the next command",
			defaultCheckBudget, bridgeRequestTimeout)
	}
}

// TestCheckNodesDoesNotHoldTheRequestLoop: a check runs for seconds by design
// and the request loop is otherwise strictly serial, so a status went unanswered
// and a disconnect went unactioned for the whole measurement — which is what made
// the app look like it had stopped responding.
func TestCheckNodesDoesNotHoldTheRequestLoop(t *testing.T) {
	nodes := []model.Node{vlessNode("A", "a.example.11")}
	h, pid := newCheckHarness(t, nodes, 24720)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.daemon.checkProbe = func(ctx context.Context, _ int, _ string) (nodecheck.Stage, int64) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nodecheck.StageProbe, 0
	}

	h.send(Request{ID: 1, Cmd: CmdCheckNodes, Profile: pid})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the check never started")
	}

	h.send(Request{ID: 2, Cmd: CmdStatus})
	if r := h.await(); r.ID != 2 {
		t.Fatalf("the first response back carries id %d, want 2: the status waited out the check", r.ID)
	}

	close(release)
	if r := h.await(); r.ID != 1 {
		t.Fatalf("the second response back carries id %d, want 1", r.ID)
	}
}

// TestCheckNodesRefusesASecondOverlappingRun: the probe process binds a fixed
// range of loopback ports, so a second run started while the first still holds
// them would fail to bind and report every node dead. Saying no is the honest
// answer — a measurement that lies is worse than one that is refused. It matters
// more now that a check no longer occupies the request loop it was serialised by.
func TestCheckNodesRefusesASecondOverlappingRun(t *testing.T) {
	nodes := []model.Node{vlessNode("A", "a.example.11")}
	h, pid := newCheckHarness(t, nodes, 24730)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.daemon.checkProbe = func(ctx context.Context, _ int, _ string) (nodecheck.Stage, int64) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nodecheck.StageOK, 10
	}

	h.send(Request{ID: 1, Cmd: CmdCheckNodes, Profile: pid})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the first check never started")
	}

	h.send(Request{ID: 2, Cmd: CmdCheckNodes, Profile: pid})
	r := h.await()
	if r.ID != 2 {
		t.Fatalf("the first response back carries id %d, want 2", r.ID)
	}
	if r.Ok {
		t.Error("a second check ran while the first still held the probe ports")
	}

	close(release)
	if r := h.await(); r.ID != 1 || !r.Ok {
		t.Fatalf("the first check answered id=%d ok=%v: %s", r.ID, r.Ok, r.Error)
	}
	if h.probe.starts() != 1 {
		t.Errorf("the probe process started %d times, want 1", h.probe.starts())
	}
}

// TestCheckNodesKeepsTheStageAnAnsweringNodeEarned: a failed plain TCP dial to a
// node's own address renamed *every* failure through that node as "address
// unreachable". A node carrying traffic obviously has a reachable address
// whatever that dial did — and a UDP-carried node (Hysteria2, WireGuard) never
// answers a TCP dial at all — so the rename buried the handshake/probe
// distinction this command exists to draw, and showed a working exit as a dead
// host.
func TestCheckNodesKeepsTheStageAnAnsweringNodeEarned(t *testing.T) {
	const blocked = "https://c.example/204"
	nodes := []model.Node{vlessNode("A", "a.example.11")}
	h, pid := newCheckHarness(t, nodes, 24740)
	h.daemon.checkTargets = []string{"https://a.example/204", "https://b.example/204", blocked}
	// The node carries two destinations of three, while its own address answers no
	// dial (the harness fails every one) — the state a UDP-carried node is in
	// permanently.
	h.daemon.checkProbe = func(_ context.Context, _ int, target string) (nodecheck.Stage, int64) {
		if target == blocked {
			return nodecheck.StageProbe, 0
		}
		return nodecheck.StageOK, 25
	}

	got := h.run(t, pid)

	if len(got.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(got.Results))
	}
	if !got.Results[0].Usable() || got.Best == "" {
		t.Fatal("a node carrying two destinations of three was not reported as usable")
	}
	seen := false
	for _, tr := range got.Results[0].Targets {
		if tr.Target != blocked {
			continue
		}
		seen = true
		if tr.Stage != nodecheck.StageProbe {
			t.Errorf("the blocked destination reports %q, want %q: the node's own dial rewrote a verdict it did not earn",
				tr.Stage, nodecheck.StageProbe)
		}
	}
	if !seen {
		t.Fatalf("the blocked destination is missing from the node's targets")
	}
}

// TestCheckNodesStillNamesAnUnreachableAddress is the other half of that rename:
// with nothing coming back through the node, the dial is the only evidence there
// is, and "the address does not answer" is a different problem for the user than
// "the address answers and carries nothing".
func TestCheckNodesStillNamesAnUnreachableAddress(t *testing.T) {
	nodes := []model.Node{vlessNode("A", "a.example.11")}
	h, pid := newCheckHarness(t, nodes, 24750)
	h.verdicts(map[int]nodecheck.Stage{24750: nodecheck.StageHandshake}, nil)

	got := h.run(t, pid)

	if len(got.Results) != 1 || len(got.Results[0].Targets) == 0 {
		t.Fatalf("got %d results, with no targets measured", len(got.Results))
	}
	for _, tr := range got.Results[0].Targets {
		if tr.Stage != nodecheck.StageDial {
			t.Errorf("target %s reports %q, want %q: the address answered nothing", tr.Target, tr.Stage, nodecheck.StageDial)
		}
	}
}
