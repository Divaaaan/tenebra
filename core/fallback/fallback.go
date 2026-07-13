// Package fallback implements the anti-DPI protocol fallback walk. When the
// active connection is detected blocked or timed out, the Machine hands the
// caller candidate nodes to try in a deterministic order: whatever last worked
// for the profile first, then by protocol preference (REALITY-flavoured VLESS,
// then Hysteria2, then AmneziaWG by default), then everything else. The walk is
// pure — it performs no network I/O — so it is fully unit-testable, and the
// caller owns the actual dialling and blocking detection.
//
// The package offers two orderings, both producing the same kind of Machine so
// the connect loop is oblivious to which one built it:
//
//   - New ranks by protocol preference (the default anti-DPI strategy);
//   - NewByLatency ranks by measured round-trip time, fastest first, for the
//     "auto-select fastest node" mode. The anti-DPI fallback is preserved either
//     way — if the lead candidate's connect-probe is blocked, the loop advances
//     to the next one in the order.
package fallback

import (
	"sort"

	"github.com/Divaaaan/tenebra/core/model"
)

// DefaultOrder is the protocol preference the brief specifies: VLESS+REALITY,
// then Hysteria2, then AmneziaWG. VLESS here stands for the REALITY variant —
// the model has no separate constant for it. A node whose protocol is not in
// the order still gets tried, after every node whose protocol is.
var DefaultOrder = []model.Protocol{model.VLESS, model.Hysteria2, model.AmneziaWG}

// Attempt is a single node to try, paired with the stable ID the caller keys
// connection state on. It is what last-good persistence stores and restores.
type Attempt struct {
	NodeID string
	Node   model.Node
}

// unreachableRTT is the rank latency-ordering assigns to a candidate with no
// usable RTT (its probe failed, timed out, or was never measured). It sits past
// any realistic real-world ping so such candidates sort to the end of the
// latency walk while still being tried — a server that did not answer a cheap
// TCP probe may still complete a full handshake, and the anti-DPI walk must not
// drop it. Using a sentinel rather than omitting these keeps every candidate in
// the order exactly once, matching the protocol-preference ordering's contract.
const unreachableRTT = int64(1) << 62

// Machine walks an ordered set of attempts for one profile. It is not safe for
// concurrent use; a connection manager drives a single profile's reconnect loop
// from one goroutine. Construct it with New (protocol-preference ordering) or
// NewByLatency (measured-RTT ordering).
type Machine struct {
	profileID string
	lastGood  LastGood

	// order is the resolved sequence of attempts. Index 0 is tried first. It is
	// rebuilt from the candidates and the current last-good whenever the walk
	// (re)starts, so a Success mid-walk influences the next cycle.
	order []Attempt
	// cursor is the index of the next attempt Next will return. len(order) means
	// the walk is exhausted.
	cursor int

	// candidates, pref and latency are kept so the order can be recomputed on
	// Reset and after Success without the caller re-supplying them.
	candidates []Attempt
	pref       []model.Protocol

	// byLatency selects the ordering strategy rebuild applies: false ranks by
	// protocol preference (pref), true ranks by ascending measured RTT (latency).
	byLatency bool
	// latency maps a candidate NodeID to its measured RTT in milliseconds. Only
	// consulted when byLatency is set. A node absent from the map — or present
	// with a non-positive value — is treated as unreachable and sorts last.
	latency map[string]int64
}

// New builds a Machine for profileID over candidates, preferring protocols in
// the given order. A nil or empty order falls back to DefaultOrder. lastGood may
// be nil, in which case the machine simply has no last-good memory. The
// candidates slice is copied, so the caller may reuse it.
func New(profileID string, candidates []Attempt, order []model.Protocol, lastGood LastGood) *Machine {
	if len(order) == 0 {
		order = DefaultOrder
	}
	pref := make([]model.Protocol, len(order))
	copy(pref, order)

	cand := make([]Attempt, len(candidates))
	copy(cand, candidates)

	m := &Machine{
		profileID:  profileID,
		lastGood:   lastGood,
		candidates: cand,
		pref:       pref,
	}
	m.rebuild()
	return m
}

// NewByLatency builds a Machine for profileID that hands candidates out fastest
// first, ranked by the measured round-trip times in rtt (keyed by NodeID, in
// milliseconds). It backs the "auto-select fastest node" mode. A candidate with
// no entry in rtt, or a non-positive one, counts as unreachable and sorts to the
// end — it is still tried last (a server that ignored the cheap TCP probe may
// yet handshake), preserving the anti-DPI fallback. Ties (equal RTT) break
// toward the last-good node first, then by input order, so a re-measured field
// of equally-fast servers stays stable across reconnects. The rtt map and the
// candidates slice are copied, so the caller may reuse both. lastGood may be nil.
//
// Why last-good is only a tiebreak here, not a lead: the user asked for the
// fastest exit, so RTT is authoritative. Letting last-good jump ahead of a
// measurably faster server would silently override that intent. We still record
// last-good on Success (see runFallback) and honour it as a lead in the
// protocol-preference machine, so a later non-auto connect still benefits.
func NewByLatency(profileID string, candidates []Attempt, rtt map[string]int64, lastGood LastGood) *Machine {
	cand := make([]Attempt, len(candidates))
	copy(cand, candidates)

	lat := make(map[string]int64, len(rtt))
	for k, v := range rtt {
		lat[k] = v
	}

	m := &Machine{
		profileID:  profileID,
		lastGood:   lastGood,
		candidates: cand,
		byLatency:  true,
		latency:    lat,
	}
	m.rebuild()
	return m
}

// rebuild recomputes order from candidates, the current last-good, and whichever
// ordering strategy this machine uses, then rewinds the cursor to the front.
// Every candidate appears exactly once. It dispatches to the latency or the
// protocol-preference layout; both share the last-good lookup.
func (m *Machine) rebuild() {
	m.cursor = 0
	m.order = m.order[:0]

	// Index of the last-good candidate, or -1 if none is set or it is stale (no
	// longer offered, e.g. removed by a subscription refresh).
	lastIdx := -1
	if m.lastGood != nil {
		if lastID, ok := m.lastGood.Get(m.profileID); ok {
			for i := range m.candidates {
				if m.candidates[i].NodeID == lastID {
					lastIdx = i
					break
				}
			}
		}
	}

	if m.byLatency {
		m.rebuildByLatency(lastIdx)
		return
	}
	m.rebuildByPreference(lastIdx)
}

// rebuildByPreference lays out order by protocol preference. The last-good node,
// if still among the candidates, leads everything; the rest follow in
// preference order with the original candidate index as a stable tiebreak.
func (m *Machine) rebuildByPreference(lastIdx int) {
	// rank maps a protocol to its position in the preference list; protocols not
	// listed rank after all listed ones so they are still tried, just last.
	rank := func(p model.Protocol) int {
		for i, pp := range m.pref {
			if pp == p {
				return i
			}
		}
		return len(m.pref)
	}

	rest := make([]int, 0, len(m.candidates))
	for i := range m.candidates {
		if i == lastIdx {
			continue
		}
		rest = append(rest, i)
	}
	sort.SliceStable(rest, func(a, b int) bool {
		ia, ib := rest[a], rest[b]
		ra, rb := rank(m.candidates[ia].Node.Protocol), rank(m.candidates[ib].Node.Protocol)
		if ra != rb {
			return ra < rb
		}
		return ia < ib // preserve input order within a protocol
	})

	if lastIdx >= 0 {
		m.order = append(m.order, m.candidates[lastIdx])
	}
	for _, i := range rest {
		m.order = append(m.order, m.candidates[i])
	}
}

// rebuildByLatency lays out order fastest first. Unlike the preference layout,
// last-good does NOT lead — RTT is authoritative for "fastest node" (see
// NewByLatency) — but it does win an exact-RTT tie, so a field of equally-fast
// servers stays stable across reconnects. Unreachable candidates (no usable RTT)
// fold to a sentinel rank and trail, in input order, still tried last.
func (m *Machine) rebuildByLatency(lastIdx int) {
	// rtt returns the candidate's measured round-trip, or the unreachable
	// sentinel when it is missing or non-positive (a failed/absent probe). A
	// zero RTT is not a real "instant" exit, so it is treated as unreachable too.
	rtt := func(i int) int64 {
		if ms, ok := m.latency[m.candidates[i].NodeID]; ok && ms > 0 {
			return ms
		}
		return unreachableRTT
	}

	idx := make([]int, len(m.candidates))
	for i := range m.candidates {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ia, ib := idx[a], idx[b]
		ra, rb := rtt(ia), rtt(ib)
		if ra != rb {
			return ra < rb // lower RTT first
		}
		// Equal RTT: the last-good node breaks the tie so reconnects don't churn
		// between equally-fast servers; otherwise keep the input order.
		if ia == lastIdx {
			return true
		}
		if ib == lastIdx {
			return false
		}
		return ia < ib
	})

	for _, i := range idx {
		m.order = append(m.order, m.candidates[i])
	}
}

// Next returns the next attempt to try and ok=true, or a zero Attempt and
// ok=false once the walk is exhausted. It does not advance: the same attempt is
// returned until the caller reports its outcome via Success or Failure. This
// lets the caller dial, observe, then commit the result.
func (m *Machine) Next() (Attempt, bool) {
	if m.cursor >= len(m.order) {
		return Attempt{}, false
	}
	return m.order[m.cursor], true
}

// Success records attempt as the new last-good for the profile and rewinds the
// walk. The recorded node leads the next cycle, so after a reconnect the machine
// retries what just worked before exploring alternatives.
func (m *Machine) Success(a Attempt) {
	if m.lastGood != nil {
		m.lastGood.Set(m.profileID, a.NodeID)
	}
	m.rebuild()
}

// Failure marks the attempt at the cursor as failed and advances past it. The
// argument is accepted for symmetry and call-site clarity; advancement always
// follows the cursor, matching Next's contract that the cursor attempt is the
// one in flight.
func (m *Machine) Failure(Attempt) {
	if m.cursor < len(m.order) {
		m.cursor++
	}
}

// Reset restarts the walk from the front, honouring the current last-good. Use
// it to begin a fresh fallback cycle — e.g. after a network change — without
// discarding what last worked. It does not clear last-good.
func (m *Machine) Reset() { m.rebuild() }

// Exhausted reports whether every candidate has been handed out and failed —
// equivalently, whether Next now returns ok=false. It is a convenience query for
// callers that track a walk's progress; the connect loop instead keys off Next's
// ok result directly and, once exhausted, reports the terminal "all protocols
// failed". A machine with no candidates is exhausted from the start.
func (m *Machine) Exhausted() bool { return m.cursor >= len(m.order) }

// Order returns the resolved attempt sequence for the current walk, index 0
// first — exactly the order Next hands attempts out in. It is a fresh copy the
// caller may keep or mutate without affecting the machine. The layout is fixed
// for the walk's duration (it is only recomputed on Success or Reset, both of
// which restart the walk), so a caller can render the whole fallback plan up
// front and then track each attempt's progress against it.
func (m *Machine) Order() []Attempt {
	out := make([]Attempt, len(m.order))
	copy(out, m.order)
	return out
}
