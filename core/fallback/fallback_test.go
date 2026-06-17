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
