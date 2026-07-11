package control

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"

	"github.com/Divaaaan/tenebra/core/fallback"
	"github.com/Divaaaan/tenebra/core/model"
)

// classifyAttemptFailure decides why a just-failed connect attempt to node
// failed, so the fallback loop can escalate a transport strategy on a node that
// looks blocked but keep advancing past a dead one. It pairs a direct TCP probe
// of the node's entry (what a path-level filter sees) with the through-tunnel
// stall signal the probe reported, and hands both to the pure classifier.
//
// It is the production value of the daemon's injectable classify seam; tests
// swap in a scripted verdict so the escalation logic can be exercised without a
// live network. stalled comes from probeUntilUp: whether the tunnel handshake
// made no progress within its budget (a silent stall) rather than failing fast.
func (d *Daemon) classifyAttemptFailure(ctx context.Context, node model.Node, stalled bool) fallback.FailureClass {
	tcp := d.tcpProbe(ctx, node)
	return fallback.ClassifyFailure(fallback.FailureSignal{TCP: tcp, HandshakeStalled: stalled})
}

// tcpProbe makes a single direct TCP connection to the node's entry and reports
// what it observed. It dials over the same physical-interface-bound dialer the
// ping path uses (d.dial): while the tunnel is up with auto_route a routed dial
// would land on the tun and measure sing-box, not the real first hop, so binding
// to the physical NIC is what makes this reflect the path a filter acts on. The
// probe distinguishes a completed handshake from a reset, a timeout, and an
// unreachable/unresolvable address — the discriminators the classifier needs.
func (d *Daemon) tcpProbe(ctx context.Context, node model.Node) fallback.TCPResult {
	if node.Server == "" || node.Port == 0 {
		return fallback.TCPIndeterminate
	}
	dialCtx, cancel := context.WithTimeout(ctx, pingDialTimeout)
	defer cancel()
	addr := net.JoinHostPort(node.Server, strconv.Itoa(node.Port))
	conn, err := d.dial(dialCtx, "tcp", addr)
	if err == nil {
		_ = conn.Close()
		return fallback.TCPConnected
	}
	return classifyDialError(err, dialCtx.Err())
}

// classifyDialError maps a failed TCP dial's error into a TCPResult. ctxErr is
// the dial context's own error, consulted so a dial the deadline cut short is
// read as a timeout even when the wrapped OS error does not say so. The buckets
// are ordered most-specific first: a reset and an unreachable/unresolvable
// address are definitive dead-node signals, a timeout is the ambiguous
// no-answer case, and anything else stays indeterminate rather than being forced
// into a verdict.
func classifyDialError(err error, ctxErr error) fallback.TCPResult {
	// A reset from a reachable host: the port refused the connection.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fallback.TCPRefused
	}
	// No route to host or unreachable network: the entry can't be reached at all.
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return fallback.TCPUnreachable
	}
	// The host could not be resolved.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fallback.TCPUnreachable
	}
	// The SYN drew no answer within the deadline — no SYN-ACK and no reset. This
	// covers both an explicit deadline and a net timeout error.
	if errors.Is(err, context.DeadlineExceeded) || ctxErr != nil {
		return fallback.TCPTimedOut
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fallback.TCPTimedOut
	}
	// An error that fits none of the buckets: don't force a verdict.
	return fallback.TCPIndeterminate
}
