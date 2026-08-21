package nodecheck

import "testing"

// ok builds a successful target result with the given round-trip.
func ok(target string, rtt int64) TargetResult {
	return TargetResult{Target: target, Stage: StageOK, RTTMs: rtt}
}

// bad builds a failed target result at the given stage.
func bad(target string, stage Stage) TargetResult {
	return TargetResult{Target: target, Stage: stage}
}

func TestUsableNeedsMajority(t *testing.T) {
	cases := []struct {
		name    string
		targets []TargetResult
		want    bool
	}{
		{"no targets probed", nil, false},
		{"all ok", []TargetResult{ok("a", 10), ok("b", 12)}, true},
		{"all failed", []TargetResult{bad("a", StageProbe), bad("b", StageProbe)}, false},
		{
			"majority ok",
			[]TargetResult{ok("a", 10), ok("b", 12), bad("c", StageProbe)},
			true,
		},
		{
			// Exactly half is not a majority: a node splitting evenly is not
			// something auto-select should hand a session to.
			"exactly half",
			[]TargetResult{ok("a", 10), bad("b", StageProbe)},
			false,
		},
		{
			// The 2026-08-18 field failure: the node served one incidental
			// target and black-holed everything the user actually wanted.
			"one lucky target out of five",
			[]TargetResult{
				ok("ipify", 40),
				bad("gstatic", StageProbe),
				bad("youtube", StageProbe),
				bad("discord", StageProbe),
				bad("cloudflare", StageProbe),
			},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (NodeResult{NodeID: "n", Targets: tc.targets}).Usable(); got != tc.want {
				t.Fatalf("Usable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestScoreIsMedianOfSuccesses(t *testing.T) {
	cases := []struct {
		name    string
		targets []TargetResult
		want    int64
	}{
		{
			// The 900ms outlier must not drag the score the way a mean would
			// (mean here is 340).
			"outlier does not move the median",
			[]TargetResult{ok("a", 100), ok("b", 20), ok("c", 900)},
			100,
		},
		{
			// Even count takes the lower middle sample, so the score stays a
			// value that was actually measured.
			"even count takes lower middle",
			[]TargetResult{ok("a", 10), ok("b", 20), ok("c", 30), ok("d", 40)},
			20,
		},
		{
			"failed targets are ignored",
			[]TargetResult{ok("a", 50), bad("b", StageHandshake), bad("c", StageDial)},
			50,
		},
		{"nothing succeeded", []TargetResult{bad("a", StageDial)}, Unreachable},
		{"no targets", nil, Unreachable},
		{
			// A zero RTT is not a real instant response, so it does not count
			// as a sample.
			"zero rtt is not a sample",
			[]TargetResult{{Target: "a", Stage: StageOK, RTTMs: 0}},
			Unreachable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (NodeResult{NodeID: "n", Targets: tc.targets}).Score(); got != tc.want {
				t.Fatalf("Score() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRankPrefersWorkingOverFast(t *testing.T) {
	// This is the regression the package exists for: `fast` is what a TCP ping
	// sees — instant dial, nothing behind it — and it must not win.
	fast := NodeResult{NodeID: "fast-but-dead", Targets: []TargetResult{
		bad("gstatic", StageHandshake),
		bad("youtube", StageHandshake),
		bad("discord", StageHandshake),
	}}
	slow := NodeResult{NodeID: "slow-but-alive", Targets: []TargetResult{
		ok("gstatic", 400), ok("youtube", 420), ok("discord", 380),
	}}

	got := Rank([]NodeResult{fast, slow}, "")
	if got[0].NodeID != "slow-but-alive" {
		t.Fatalf("ranked %q first, want the working node", got[0].NodeID)
	}

	best, found := Best([]NodeResult{fast, slow}, "")
	if !found || best.NodeID != "slow-but-alive" {
		t.Fatalf("Best() = %q (found=%v), want slow-but-alive", best.NodeID, found)
	}
}

func TestRankOrdersByCoverageThenLatency(t *testing.T) {
	// Wider coverage outranks a slightly lower median: carrying more of the
	// user's destinations is worth more than a few milliseconds.
	broad := NodeResult{NodeID: "broad", Targets: []TargetResult{
		ok("a", 100), ok("b", 100), ok("c", 100), bad("d", StageProbe),
	}}
	narrow := NodeResult{NodeID: "narrow", Targets: []TargetResult{
		ok("a", 10), ok("b", 10), bad("c", StageProbe),
	}}
	// Same coverage as narrow but slower, so it must trail it.
	narrowSlow := NodeResult{NodeID: "narrow-slow", Targets: []TargetResult{
		ok("a", 90), ok("b", 90), bad("c", StageProbe),
	}}

	got := Rank([]NodeResult{narrowSlow, narrow, broad}, "")
	want := []string{"broad", "narrow", "narrow-slow"}
	for i, id := range want {
		if got[i].NodeID != id {
			t.Fatalf("position %d = %q, want %q (order: %v)", i, got[i].NodeID, id, ids(got))
		}
	}
}

func TestRankLastGoodBreaksTiesOnly(t *testing.T) {
	a := NodeResult{NodeID: "a", Targets: []TargetResult{ok("x", 50), ok("y", 50)}}
	b := NodeResult{NodeID: "b", Targets: []TargetResult{ok("x", 50), ok("y", 50)}}

	// Identical scores: the previously-good node leads so a re-check does not
	// churn a live session for nothing.
	got := Rank([]NodeResult{a, b}, "b")
	if got[0].NodeID != "b" {
		t.Fatalf("tie: got %q first, want last-good b", got[0].NodeID)
	}

	// A measurably faster node still wins over last-good — the user asked for
	// the best exit, not the familiar one.
	faster := NodeResult{NodeID: "faster", Targets: []TargetResult{ok("x", 5), ok("y", 5)}}
	got = Rank([]NodeResult{a, faster}, "a")
	if got[0].NodeID != "faster" {
		t.Fatalf("got %q first, want faster to beat last-good", got[0].NodeID)
	}
}

func TestRankIsStableAndPure(t *testing.T) {
	in := []NodeResult{
		{NodeID: "first", Targets: []TargetResult{ok("x", 30), ok("y", 30)}},
		{NodeID: "second", Targets: []TargetResult{ok("x", 30), ok("y", 30)}},
	}
	got := Rank(in, "")
	if got[0].NodeID != "first" || got[1].NodeID != "second" {
		t.Fatalf("equal nodes reordered: %v", ids(got))
	}
	// The caller keeps its slice: Rank copies before sorting.
	if in[0].NodeID != "first" {
		t.Fatalf("Rank mutated its input: %v", ids(in))
	}
}

func TestBestReportsNoWorkingNode(t *testing.T) {
	dead := []NodeResult{
		{NodeID: "a", Targets: []TargetResult{bad("x", StageDial), bad("y", StageDial)}},
		{NodeID: "b", Targets: []TargetResult{bad("x", StageHandshake), bad("y", StageProbe)}},
	}
	if _, found := Best(dead, ""); found {
		t.Fatal("Best() picked a node when every exit is broken")
	}
	if _, found := Best(nil, ""); found {
		t.Fatal("Best() picked a node from an empty result set")
	}
}

// ids extracts node ids for readable failure messages.
func ids(rs []NodeResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.NodeID
	}
	return out
}
