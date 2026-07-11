package fallback

import (
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
)

// node builds an Attempt with the given id and protocol; the rest of the Node is
// irrelevant to the walk, which only looks at NodeID and Protocol.
func node(id string, p model.Protocol) Attempt {
	return Attempt{NodeID: id, Node: model.Node{Protocol: p, Name: id}}
}

// drain runs the machine to exhaustion, failing every attempt, and returns the
// node IDs in the order they were handed out.
func drain(m *Machine) []string {
	var got []string
	for {
		a, ok := m.Next()
		if !ok {
			return got
		}
		got = append(got, a.NodeID)
		m.Failure(a)
	}
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestProtocolPreferenceOrder(t *testing.T) {
	// Candidates are deliberately out of preference order; the walk must reorder
	// them VLESS, Hysteria2, AmneziaWG regardless of input order.
	cands := []Attempt{
		node("wg", model.AmneziaWG),
		node("hy", model.Hysteria2),
		node("re", model.VLESS),
	}
	m := New("p1", cands, nil, nil)
	eq(t, drain(m), []string{"re", "hy", "wg"})
}

func TestUnlistedProtocolSortsLast(t *testing.T) {
	// Trojan and Shadowsocks are not in DefaultOrder; they must come after the
	// listed protocols but still be tried, in their original relative order.
	cands := []Attempt{
		node("tr", model.Trojan),
		node("hy", model.Hysteria2),
		node("ss", model.Shadowsocks),
		node("re", model.VLESS),
	}
	m := New("p1", cands, nil, nil)
	eq(t, drain(m), []string{"re", "hy", "tr", "ss"})
}

func TestStableWithinProtocol(t *testing.T) {
	// Two nodes share a protocol: input order is the tiebreak.
	cands := []Attempt{
		node("re2", model.VLESS),
		node("re1", model.VLESS),
		node("hy", model.Hysteria2),
	}
	m := New("p1", cands, nil, nil)
	eq(t, drain(m), []string{"re2", "re1", "hy"})
}

func TestLastGoodLeadsThenPreference(t *testing.T) {
	// last-good is an AmneziaWG node that would normally sort last; it must lead,
	// and must not be duplicated among the preference-sorted remainder.
	lg := NewMemLastGood()
	lg.Set("p1", "wg")
	cands := []Attempt{
		node("re", model.VLESS),
		node("hy", model.Hysteria2),
		node("wg", model.AmneziaWG),
	}
	m := New("p1", cands, nil, lg)
	eq(t, drain(m), []string{"wg", "re", "hy"})
}

func TestOrderReflectsResolvedPlan(t *testing.T) {
	// Order must return the same sequence Next hands out, up front and without
	// advancing the walk. last-good leads, then protocol preference.
	lg := NewMemLastGood()
	lg.Set("p1", "wg")
	cands := []Attempt{
		node("re", model.VLESS),
		node("hy", model.Hysteria2),
		node("wg", model.AmneziaWG),
	}
	m := New("p1", cands, nil, lg)

	order := m.Order()
	got := make([]string, len(order))
	for i, a := range order {
		got[i] = a.NodeID
	}
	eq(t, got, []string{"wg", "re", "hy"})

	// Order does not consume the walk: Next still yields the lead, and the drained
	// order matches what Order reported.
	if a, ok := m.Next(); !ok || a.NodeID != "wg" {
		t.Fatalf("Next after Order = %q,%v; want wg,true", a.NodeID, ok)
	}
	eq(t, drain(m), []string{"wg", "re", "hy"})

	// The returned slice is a copy: mutating it must not disturb the machine.
	order[0].NodeID = "tampered"
	eq(t, drain(New("p1", cands, nil, lg)), []string{"wg", "re", "hy"})
}

func TestFailureAdvances(t *testing.T) {
	cands := []Attempt{
		node("re", model.VLESS),
		node("hy", model.Hysteria2),
	}
	m := New("p1", cands, nil, nil)

	a, ok := m.Next()
	if !ok || a.NodeID != "re" {
		t.Fatalf("first Next = %q,%v; want re,true", a.NodeID, ok)
	}
	// Next without an outcome returns the same attempt.
	if a2, _ := m.Next(); a2.NodeID != "re" {
		t.Fatalf("Next without outcome = %q; want re (no advance)", a2.NodeID)
	}
	m.Failure(a)
	if a3, ok := m.Next(); !ok || a3.NodeID != "hy" {
		t.Fatalf("after Failure Next = %q,%v; want hy,true", a3.NodeID, ok)
	}
}

func TestExhaustion(t *testing.T) {
	cands := []Attempt{
		node("re", model.VLESS),
		node("hy", model.Hysteria2),
	}
	m := New("p1", cands, nil, nil)
	if m.Exhausted() {
		t.Fatal("fresh machine with candidates reports exhausted")
	}
	drain(m)
	if !m.Exhausted() {
		t.Fatal("after failing all attempts, not exhausted")
	}
	if a, ok := m.Next(); ok {
		t.Fatalf("Next after exhaustion = %q,true; want zero,false", a.NodeID)
	}
	// Failure past the end is a no-op, not a panic or negative cursor.
	m.Failure(Attempt{})
	if !m.Exhausted() {
		t.Fatal("Failure past end changed exhaustion state")
	}
}

func TestSuccessRecordsLastGood(t *testing.T) {
	lg := NewMemLastGood()
	cands := []Attempt{
		node("re", model.VLESS),
		node("hy", model.Hysteria2),
		node("wg", model.AmneziaWG),
	}
	m := New("p1", cands, nil, lg)

	// Fail the first, succeed on the second.
	first, _ := m.Next()
	m.Failure(first)
	second, _ := m.Next()
	if second.NodeID != "hy" {
		t.Fatalf("second attempt = %q; want hy", second.NodeID)
	}
	m.Success(second)

	if id, ok := lg.Get("p1"); !ok || id != "hy" {
		t.Fatalf("last-good = %q,%v; want hy,true", id, ok)
	}
	// Success rewinds and re-leads with the recorded node.
	eq(t, drain(m), []string{"hy", "re", "wg"})
}

func TestResetRestartsFromLastGood(t *testing.T) {
	lg := NewMemLastGood()
	lg.Set("p1", "hy")
	cands := []Attempt{
		node("re", model.VLESS),
		node("hy", model.Hysteria2),
	}
	m := New("p1", cands, nil, lg)

	// Walk partway: skip the leading last-good, land on the next.
	a, _ := m.Next()
	if a.NodeID != "hy" {
		t.Fatalf("lead attempt = %q; want hy", a.NodeID)
	}
	m.Failure(a)
	if a2, _ := m.Next(); a2.NodeID != "re" {
		t.Fatalf("after one failure = %q; want re", a2.NodeID)
	}

	m.Reset()
	if a3, ok := m.Next(); !ok || a3.NodeID != "hy" {
		t.Fatalf("after Reset Next = %q,%v; want hy,true", a3.NodeID, ok)
	}
	// Reset does not clear last-good.
	if id, _ := lg.Get("p1"); id != "hy" {
		t.Fatalf("Reset cleared last-good: %q", id)
	}
}

func TestEmptyCandidates(t *testing.T) {
	m := New("p1", nil, nil, NewMemLastGood())
	if !m.Exhausted() {
		t.Fatal("machine with no candidates not exhausted")
	}
	if a, ok := m.Next(); ok {
		t.Fatalf("Next on empty = %q,true; want zero,false", a.NodeID)
	}
	// These must be safe to call on an empty machine.
	m.Failure(Attempt{})
	m.Reset()
	if a, ok := m.Next(); ok {
		t.Fatalf("Next on empty after ops = %q,true; want zero,false", a.NodeID)
	}
}

func TestStaleLastGoodIgnored(t *testing.T) {
	// last-good points at a node that is no longer offered (e.g. removed by a
	// subscription refresh). The walk must ignore it and fall back to pure
	// protocol preference, handing out every current candidate exactly once.
	lg := NewMemLastGood()
	lg.Set("p1", "gone")
	cands := []Attempt{
		node("hy", model.Hysteria2),
		node("re", model.VLESS),
	}
	m := New("p1", cands, nil, lg)
	eq(t, drain(m), []string{"re", "hy"})
}

func TestCustomOrder(t *testing.T) {
	// A caller-supplied order overrides DefaultOrder.
	cands := []Attempt{
		node("re", model.VLESS),
		node("hy", model.Hysteria2),
		node("wg", model.AmneziaWG),
	}
	order := []model.Protocol{model.AmneziaWG, model.VLESS, model.Hysteria2}
	m := New("p1", cands, order, nil)
	eq(t, drain(m), []string{"wg", "re", "hy"})
}

func TestNilLastGoodSafe(t *testing.T) {
	// With no LastGood, Success must not panic and the walk stays preference-only.
	cands := []Attempt{
		node("re", model.VLESS),
		node("hy", model.Hysteria2),
	}
	m := New("p1", cands, nil, nil)
	a, _ := m.Next()
	m.Success(a) // no store to write to; must be a safe no-op + rewind
	eq(t, drain(m), []string{"re", "hy"})
}

func TestCandidatesCopied(t *testing.T) {
	// Mutating the caller's slice after New must not change the walk.
	cands := []Attempt{
		node("re", model.VLESS),
		node("hy", model.Hysteria2),
	}
	m := New("p1", cands, nil, nil)
	cands[0] = node("mutated", model.Trojan)
	eq(t, drain(m), []string{"re", "hy"})
}

// --- latency ordering (NewByLatency) ----------------------------------------

func TestByLatencyOrdersAscending(t *testing.T) {
	// Candidates are out of RTT order; the walk must hand them out fastest first
	// regardless of protocol preference (the slowest here is the VLESS node that
	// protocol-ordering would have led with).
	cands := []Attempt{
		node("re", model.VLESS),     // 180ms
		node("hy", model.Hysteria2), // 40ms
		node("wg", model.AmneziaWG), // 90ms
	}
	rtt := map[string]int64{"re": 180, "hy": 40, "wg": 90}
	m := NewByLatency("p1", cands, rtt, nil)
	eq(t, drain(m), []string{"hy", "wg", "re"})
}

func TestByLatencyUnreachableSortLast(t *testing.T) {
	// A node missing from the map and one with a non-positive RTT are both
	// unreachable: they trail the reachable nodes, in input order, but are still
	// handed out (anti-DPI must not drop a server that ignored the TCP probe).
	cands := []Attempt{
		node("missing", model.VLESS),  // no entry -> unreachable
		node("fast", model.Hysteria2), // 30ms
		node("zero", model.Trojan),    // 0ms -> unreachable
		node("mid", model.AmneziaWG),  // 120ms
	}
	rtt := map[string]int64{"fast": 30, "mid": 120, "zero": 0}
	m := NewByLatency("p1", cands, rtt, nil)
	// Reachable first by RTT (fast, mid), then unreachable in input order
	// (missing before zero).
	eq(t, drain(m), []string{"fast", "mid", "missing", "zero"})
}

func TestByLatencyAllUnreachableKeepsInputOrder(t *testing.T) {
	// With no usable RTT for anyone, every candidate is equally (un)ranked, so
	// the stable sort preserves input order — and all are still tried.
	cands := []Attempt{
		node("a", model.VLESS),
		node("b", model.Hysteria2),
		node("c", model.AmneziaWG),
	}
	m := NewByLatency("p1", cands, map[string]int64{}, nil)
	eq(t, drain(m), []string{"a", "b", "c"})
}

func TestByLatencyEqualRTTStableByInput(t *testing.T) {
	// Equal RTTs with no last-good fall back to input order.
	cands := []Attempt{
		node("x", model.VLESS),
		node("y", model.Hysteria2),
		node("z", model.AmneziaWG),
	}
	rtt := map[string]int64{"x": 50, "y": 50, "z": 50}
	m := NewByLatency("p1", cands, rtt, nil)
	eq(t, drain(m), []string{"x", "y", "z"})
}

func TestByLatencyLastGoodBreaksTieOnly(t *testing.T) {
	// last-good "y" shares the lowest RTT with "x": it must win the tie and lead.
	// But it must NOT jump ahead of a strictly faster node — RTT stays primary.
	lg := NewMemLastGood()
	lg.Set("p1", "y")
	cands := []Attempt{
		node("x", model.VLESS),        // 50ms
		node("y", model.Hysteria2),    // 50ms (last-good, ties with x)
		node("fast", model.AmneziaWG), // 20ms (strictly faster)
	}
	rtt := map[string]int64{"x": 50, "y": 50, "fast": 20}
	m := NewByLatency("p1", cands, rtt, lg)
	// fast leads on pure RTT; among the 50ms pair, last-good y precedes x.
	eq(t, drain(m), []string{"fast", "y", "x"})
}

func TestByLatencyLastGoodDoesNotOverrideFaster(t *testing.T) {
	// last-good is the SLOWEST node. Under "fastest" it must stay last — proving
	// last-good never overrides a measurably faster server (the design invariant).
	lg := NewMemLastGood()
	lg.Set("p1", "slow")
	cands := []Attempt{
		node("slow", model.VLESS),     // 300ms (last-good)
		node("mid", model.Hysteria2),  // 90ms
		node("fast", model.AmneziaWG), // 30ms
	}
	rtt := map[string]int64{"slow": 300, "mid": 90, "fast": 30}
	m := NewByLatency("p1", cands, rtt, lg)
	eq(t, drain(m), []string{"fast", "mid", "slow"})
}

func TestByLatencyStaleLastGoodIgnored(t *testing.T) {
	// last-good points at a node no longer offered; it simply has no effect, and
	// pure RTT order stands.
	lg := NewMemLastGood()
	lg.Set("p1", "gone")
	cands := []Attempt{
		node("a", model.VLESS),     // 70ms
		node("b", model.Hysteria2), // 20ms
	}
	rtt := map[string]int64{"a": 70, "b": 20}
	m := NewByLatency("p1", cands, rtt, lg)
	eq(t, drain(m), []string{"b", "a"})
}

func TestByLatencyRecordsLastGoodOnSuccess(t *testing.T) {
	// Even in latency mode, a successful attempt is recorded as last-good so a
	// later non-auto (protocol-preference) connect can lead with it. Success also
	// rewinds; the rewound order is still by RTT (last-good only breaks ties).
	lg := NewMemLastGood()
	cands := []Attempt{
		node("fast", model.VLESS),     // 25ms
		node("mid", model.Hysteria2),  // 80ms
		node("slow", model.AmneziaWG), // 150ms
	}
	rtt := map[string]int64{"fast": 25, "mid": 80, "slow": 150}
	m := NewByLatency("p1", cands, rtt, lg)

	// Fastest is blocked; we land on the next.
	first, _ := m.Next()
	if first.NodeID != "fast" {
		t.Fatalf("lead = %q; want fast", first.NodeID)
	}
	m.Failure(first)
	second, _ := m.Next()
	if second.NodeID != "mid" {
		t.Fatalf("second = %q; want mid", second.NodeID)
	}
	m.Success(second)

	if id, ok := lg.Get("p1"); !ok || id != "mid" {
		t.Fatalf("last-good = %q,%v; want mid,true", id, ok)
	}
	// Rewound order is unchanged by the recorded last-good: still pure RTT,
	// because "mid" does not tie with anyone.
	eq(t, drain(m), []string{"fast", "mid", "slow"})
}

func TestByLatencyRTTMapCopied(t *testing.T) {
	// Mutating the caller's rtt map after construction must not change the walk.
	cands := []Attempt{
		node("a", model.VLESS),
		node("b", model.Hysteria2),
	}
	rtt := map[string]int64{"a": 90, "b": 30}
	m := NewByLatency("p1", cands, rtt, nil)
	rtt["a"] = 1 // would make a fastest if the map were aliased
	eq(t, drain(m), []string{"b", "a"})
}

func TestByLatencyEmptyCandidates(t *testing.T) {
	m := NewByLatency("p1", nil, nil, NewMemLastGood())
	if !m.Exhausted() {
		t.Fatal("latency machine with no candidates not exhausted")
	}
	if a, ok := m.Next(); ok {
		t.Fatalf("Next on empty = %q,true; want zero,false", a.NodeID)
	}
}
