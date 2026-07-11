package fallback

// This file holds the failure classifier that drives adaptive transport
// escalation. It is pure — a verdict is a function of the observed signals with
// no network I/O — so it is exhaustively unit-testable on synthetic signals (a
// reset versus a silent hang) and the caller owns the actual observation.
//
// The distinction it draws is the one that decides the walk's next move: a node
// whose entry is reachable but whose handshake is then silently dropped may
// survive a different transport strategy (a reshaped handshake) on the SAME
// node, whereas a node that is simply gone must be abandoned for the next one.

// FailureClass is the verdict ClassifyFailure returns for a connection attempt
// that did not come up, to the precision the observable signals allow.
type FailureClass int

const (
	// Unknown is a failure that fits neither other verdict cleanly: the signals
	// are ambiguous (e.g. the entry never answered at TCP, which a downed host
	// and a transport-layer block share). The walk treats it like Dead — it does
	// not spend a strategy escalation on a node it cannot characterise — but the
	// separate verdict keeps the ambiguity honest rather than asserting a cause.
	Unknown FailureClass = iota
	// Dead is a node whose entry is gone or actively refusing: a TCP reset
	// (connection refused) or an unroutable/unresolvable address. No transport
	// strategy can revive it, so the walk moves on to the next node.
	Dead
	// Censored is the interference fingerprint: the entry accepts a TCP
	// connection but the tunnel handshake then makes no progress and draws no
	// reset — a silent stall consistent with a path that passes the connection
	// and drops the handshake. A different transport strategy on the SAME node
	// may survive it, so the walk escalates the strategy before abandoning it.
	Censored
)

// String renders the verdict as a short lowercase token, used in logs and in the
// attempt snapshot's reason annotation.
func (c FailureClass) String() string {
	switch c {
	case Dead:
		return "dead"
	case Censored:
		return "censored"
	default:
		return "unknown"
	}
}

// TCPResult is what a direct TCP connect to a node's entry observed. It is the
// primary signal ClassifyFailure keys on, because the interference fingerprint
// is defined at this layer: the connection establishes, but the handshake above
// it makes no progress.
type TCPResult int

const (
	// TCPIndeterminate means there was no usable TCP observation — the probe was
	// not attempted, or it failed in a way that maps to none of the buckets below.
	TCPIndeterminate TCPResult = iota
	// TCPConnected: the SYN/SYN-ACK handshake completed — the entry is reachable
	// at the transport layer.
	TCPConnected
	// TCPRefused: the entry answered the SYN with a reset (connection refused) —
	// the host is up but nothing is accepting on that port.
	TCPRefused
	// TCPTimedOut: the SYN drew no answer at all within the dial deadline — no
	// SYN-ACK and no reset. A downed host and a transport-layer SYN block are
	// indistinguishable from here.
	TCPTimedOut
	// TCPUnreachable: the address could not be routed or resolved (no route to
	// host, network unreachable, DNS failure).
	TCPUnreachable
)

// FailureSignal is the set of observations ClassifyFailure reasons over. It is a
// plain value with no I/O, so the verdict is a pure function of what was seen.
type FailureSignal struct {
	// TCP is the outcome of a direct TCP connect to the node's entry, dialled
	// over the physical path (not through the tunnel) so it reflects what a
	// path-level filter would see. It is the primary discriminator.
	TCP TCPResult
	// HandshakeStalled reports that the through-tunnel handshake probe made no
	// progress within its budget — a silent stall — rather than failing fast with
	// a definitive error. It only sharpens the TCPConnected case: a stall there is
	// the interference fingerprint, a fast handshake error after a good TCP
	// connect is not (it reads as a broken or mismatched server).
	HandshakeStalled bool
}

// ClassifyFailure turns the observed signals of a failed attempt into a verdict.
// It is deliberately conservative: it names Censored only on the specific
// fingerprint — a reachable entry whose handshake then silently stalls — reports
// Dead only on an unambiguous refusal, and otherwise yields Unknown rather than
// guess. A strategy escalation is therefore spent only when there is real
// evidence of destination-level interference, not on ordinary packet loss, a
// downed host, or a misconfigured server.
func ClassifyFailure(sig FailureSignal) FailureClass {
	switch sig.TCP {
	case TCPRefused, TCPUnreachable:
		// A reset, or an entry that cannot be routed/resolved, is a dead node. A
		// reshaped handshake cannot revive it.
		return Dead
	case TCPConnected:
		if sig.HandshakeStalled {
			// The entry accepted the connection but the handshake then stalled with
			// no reset: the interference fingerprint. Escalate the transport strategy.
			return Censored
		}
		// Reachable entry, but the handshake failed fast rather than stalling. That
		// is not the interference fingerprint — it reads as a broken or mismatched
		// server — so leave it Unknown and do not escalate.
		return Unknown
	case TCPTimedOut:
		// No SYN-ACK and no reset: a downed host and a transport-layer SYN block are
		// identical from here. Ambiguous by construction.
		return Unknown
	default:
		return Unknown
	}
}
