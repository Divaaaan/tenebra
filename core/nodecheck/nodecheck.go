// Package nodecheck ranks proxy nodes by what actually works through them,
// rather than by whether their address accepts a TCP connection.
//
// Why this exists as a separate layer from the `ping` command: a TCP dial to
// host:port proves only that something is listening. A node whose REALITY/TLS
// handshake has stopped answering still completes that dial instantly, so it
// scores as the *fastest* node and wins auto-selection — while every real
// request through it hangs until timeout. That failure was observed in the
// field on 2026-08-18: the exit accepted TCP on :8443 and then went silent for
// 19s per request, and the urltest group kept handing traffic to it because the
// cheap probe said it was alive.
//
// The second lesson from that incident is that a single control URL is not
// enough. The same dead node answered api.ipify.org normally while timing out
// on gstatic, YouTube and Discord — censorship and node-side breakage are both
// per-destination. A verdict therefore comes from several targets, and a node
// is only "usable" when a majority of them succeed.
package nodecheck

import "sort"

// Stage is how far a probe got before it failed. It is reported so the UI can
// say *what* is broken instead of a bare red dot: a dial failure means the
// address/port is unreachable (routing, firewall, dead host), a handshake
// failure means the address answers but the proxy layer does not (wrong keys,
// blocked SNI, throttled REALITY), and a probe failure means the tunnel came up
// but traffic does not survive it.
type Stage string

const (
	// StageOK marks a target that completed end to end.
	StageOK Stage = "ok"
	// StageDial marks failure to establish TCP/UDP to the node address.
	StageDial Stage = "dial"
	// StageHandshake marks a node that accepted the connection but never
	// completed the proxy handshake. This is the case a TCP ping cannot see.
	StageHandshake Stage = "handshake"
	// StageProbe marks a tunnel that established but could not carry the
	// request to the target (timeout, reset, non-2xx/3xx status).
	StageProbe Stage = "probe"
)

// TargetResult is the outcome of one control request through one node.
type TargetResult struct {
	// Target is the probed URL, kept so the UI can name what failed.
	Target string `json:"target"`
	Stage  Stage  `json:"stage"`
	// RTTMs is the time to first byte in milliseconds; meaningful only when
	// Stage is StageOK. Named on the wire the way the rest of the control
	// protocol names it (see PingResult): the UI decodes both through the same
	// bridge, and one snake_case field among camelCase ones decodes as zero
	// rather than failing, so every measured latency would silently read as 0ms.
	RTTMs int64 `json:"rttMs"`
}

// OK reports whether this target completed end to end.
func (t TargetResult) OK() bool { return t.Stage == StageOK }

// NodeResult aggregates every target probed through a single node.
type NodeResult struct {
	NodeID  string         `json:"node"`
	Targets []TargetResult `json:"targets"`
}

// Usable reports whether a strict majority of probed targets succeeded.
//
// A majority rather than "any" because one lucky target is exactly how the
// 2026-08-18 node passed for hours: it served ipify while everything the user
// cared about timed out. A majority rather than "all" because a single
// destination being blocked (or simply down) is common and should not condemn
// an otherwise healthy exit. With no targets at all the node is not usable —
// an unmeasured node must never outrank a measured one.
func (n NodeResult) Usable() bool {
	if len(n.Targets) == 0 {
		return false
	}
	return n.okCount()*2 > len(n.Targets)
}

// okCount is how many targets completed end to end.
func (n NodeResult) okCount() int {
	c := 0
	for _, t := range n.Targets {
		if t.OK() {
			c++
		}
	}
	return c
}

// Score is the node's latency in milliseconds: the median round-trip over the
// targets that succeeded, or Unreachable when none did.
//
// Median, not mean: one target routed through a congested peering link would
// drag an average far enough to reorder otherwise equal nodes, while the median
// tracks what the connection usually feels like. With an even number of
// successes it takes the lower of the two middle samples — no interpolation, so
// the score stays an actually-observed measurement.
func (n NodeResult) Score() int64 {
	rtts := make([]int64, 0, len(n.Targets))
	for _, t := range n.Targets {
		if t.OK() && t.RTTMs > 0 {
			rtts = append(rtts, t.RTTMs)
		}
	}
	if len(rtts) == 0 {
		return Unreachable
	}
	sort.Slice(rtts, func(a, b int) bool { return rtts[a] < rtts[b] })
	return rtts[(len(rtts)-1)/2]
}

// Unreachable is the sentinel score for a node with no successful target. It
// sorts after every real measurement while remaining a plain int64, so callers
// can compare scores without special-casing.
const Unreachable int64 = 1<<62 - 1

// Rank orders nodes best-first for auto-selection.
//
// The ordering is deliberately lexicographic rather than a weighted score:
//
//  1. usable nodes before unusable ones — a working slow exit beats a fast
//     broken one, which is the entire point of this package;
//  2. among usable nodes, more successful targets first — a node that carries
//     4/5 destinations is worth more than one that carries 3/5 slightly faster;
//  3. then lower median RTT;
//  4. then the previously-good node, so a field of equally-scoring exits does
//     not reshuffle on every re-check and drop the user's session for nothing;
//  5. then input order, keeping the result deterministic.
//
// lastGood may be empty. The input slice is not modified.
func Rank(results []NodeResult, lastGood string) []NodeResult {
	out := make([]NodeResult, len(results))
	copy(out, results)

	// idx preserves the original position for the final tiebreak, because
	// sort.SliceStable alone cannot express "stable *after* other keys".
	idx := make(map[string]int, len(results))
	for i, r := range results {
		if _, seen := idx[r.NodeID]; !seen {
			idx[r.NodeID] = i
		}
	}

	sort.SliceStable(out, func(a, b int) bool {
		ra, rb := out[a], out[b]

		if ua, ub := ra.Usable(), rb.Usable(); ua != ub {
			return ua
		}
		if ca, cb := ra.okCount(), rb.okCount(); ca != cb {
			return ca > cb
		}
		if sa, sb := ra.Score(), rb.Score(); sa != sb {
			return sa < sb
		}
		if lastGood != "" && ra.NodeID != rb.NodeID {
			if ra.NodeID == lastGood {
				return true
			}
			if rb.NodeID == lastGood {
				return false
			}
		}
		return idx[ra.NodeID] < idx[rb.NodeID]
	})
	return out
}

// Best returns the node auto-selection should connect to, and whether one was
// found at all.
//
// It returns a node only when that node is usable: with every exit broken there
// is no meaningful "fastest", and silently returning the least-bad one would
// reproduce the original bug — auto-select confidently handing traffic to a
// black hole. The caller is expected to surface "no working node" instead, and
// may still fall back to the existing latency ordering to keep trying.
func Best(results []NodeResult, lastGood string) (NodeResult, bool) {
	ranked := Rank(results, lastGood)
	if len(ranked) == 0 || !ranked[0].Usable() {
		return NodeResult{}, false
	}
	return ranked[0], true
}
