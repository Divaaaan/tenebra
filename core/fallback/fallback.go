// Package fallback implements the anti-DPI protocol fallback walk. When the
// active connection is detected blocked or timed out, the Machine hands the
// caller candidate nodes to try in a deterministic order: whatever last worked
// for the profile first, then by protocol preference (REALITY-flavoured VLESS,
// then Hysteria2, then AmneziaWG by default), then everything else. The walk is
// pure — it performs no network I/O — so it is fully unit-testable, and the
// caller owns the actual dialling and blocking detection.
package fallback

import (
	"errors"
	"sort"

	"github.com/Divaaaan/tenebra/core/model"
)

// ErrExhausted is returned by Next once every candidate has been handed out and
// failed. The caller surfaces it as "all protocols blocked".
var ErrExhausted = errors.New("fallback: all candidates exhausted")

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

// Machine walks an ordered set of attempts for one profile. It is not safe for
// concurrent use; a connection manager drives a single profile's reconnect loop
// from one goroutine. Construct it with New.
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

	// candidates and pref are kept so the order can be recomputed on Reset and
	// after Success without the caller re-supplying them.
	candidates []Attempt
	pref       []model.Protocol
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

// rebuild recomputes order from candidates, the protocol preference and the
// current last-good, then rewinds the cursor to the front. The last-good node,
// if still among the candidates, is moved ahead of everything; the rest follow
// in protocol-preference order with the original candidate index as a stable
// tiebreak. Every candidate appears exactly once.
func (m *Machine) rebuild() {
	m.cursor = 0
	m.order = m.order[:0]

	lastID, haveLast := "", false
	if m.lastGood != nil {
		lastID, haveLast = m.lastGood.Get(m.profileID)
	}

	// Index of the last-good candidate, or -1 if it is stale (no longer offered).
	lastIdx := -1
	if haveLast {
		for i := range m.candidates {
			if m.candidates[i].NodeID == lastID {
				lastIdx = i
				break
			}
		}
	}

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

// Exhausted reports whether every candidate has been handed out and failed.
// When true, Next returns ok=false and the caller should surface ErrExhausted.
// A machine with no candidates is exhausted from the start.
func (m *Machine) Exhausted() bool { return m.cursor >= len(m.order) }
