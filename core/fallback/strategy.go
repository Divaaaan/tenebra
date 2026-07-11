package fallback

import "github.com/Divaaaan/tenebra/core/model"

// A Strategy is one transport variation applied on top of a node before its
// outbound is built: a client-side reshaping of the TLS handshake (the uTLS
// fingerprint, the presented SNI) that leaves the protocol and the destination
// untouched. It is the unit the adaptive walk escalates through when a node's
// entry is reachable but its handshake is being silently dropped — so the same
// exit can be re-tried with a different handshake shape before the walk abandons
// it for another node.
//
// A Strategy never changes WHICH node is dialled, only HOW the handshake to it
// looks. That scoping is deliberate: the fingerprint is an axis a client can
// vary unilaterally, with no server coordination, and it is the axis a
// handshake-shape filter keys on. The SNI axis is included for completeness and
// extensibility; because a strategy is only ever tried on a node whose native
// parameters have already failed, a variation that does not help simply fails
// too and the walk moves on — it can waste a bounded attempt but never harms a
// node that was working.
type Strategy struct {
	// Name is a short, stable identifier for logs and attempt snapshots
	// (e.g. "default", "firefox-fp", "alt-sni").
	Name string
	// Fingerprint, when non-empty, overrides the node's uTLS fingerprint. Varying
	// it away from a heavily-fingerprinted default is the primary client-side
	// escalation.
	Fingerprint string
	// ServerName, when non-empty, overrides the presented TLS SNI. It is the more
	// conservative axis: for a handshake bound to a specific server name it may
	// not help, but on an already-failing node the attempt costs only itself.
	ServerName string
}

// IsDefault reports whether the strategy reshapes nothing — a node built under it
// carries its own native parameters. The first rung of a cascade is always the
// default so a node connects unchanged whenever it still can.
func (s Strategy) IsDefault() bool {
	return s.Fingerprint == "" && s.ServerName == ""
}

// ApplyTo returns a copy of n with the strategy's reshaping folded into its TLS
// parameters: a non-empty Fingerprint or ServerName overrides the node's own. A
// node without a TLS block (e.g. a WireGuard endpoint) is returned unchanged —
// there is no handshake fingerprint to reshape — as is any node under the default
// strategy. The node's TLS is deep-copied before mutation, so a shared node slice
// (the connect loop reuses one across attempts) is never altered in place.
func (s Strategy) ApplyTo(n model.Node) model.Node {
	if s.IsDefault() || n.TLS == nil {
		return n
	}
	tls := *n.TLS // copy so the caller's node (and its shared slice) is untouched
	if s.Fingerprint != "" {
		tls.Fingerprint = s.Fingerprint
	}
	if s.ServerName != "" {
		tls.ServerName = s.ServerName
	}
	n.TLS = &tls
	return n
}

// altServerName is the SNI the most divergent default strategy presents: a real,
// widely-reachable static-content host, offered as a benign handshake name when a
// node's own SNI is not getting through. It is a starting default; a subscription
// or a per-node cascade can specialise it.
const altServerName = "yastatic.net"

// DefaultStrategies is the escalation cascade applied to a node when its
// handshake looks interfered with: try it unchanged first, then reshape the uTLS
// fingerprint away from the (commonly default) chrome, then also present a
// neutral, widely-reachable SNI. The list is ordered least-to-most divergent from
// the node's own parameters, so a node connects with its native settings whenever
// they still work and only escalates under an active block. It is a package
// variable, not a constant, so a future build can extend or replace it (e.g. a
// server-informed per-node cascade) without touching the walk.
var DefaultStrategies = []Strategy{
	{Name: "default"},
	{Name: "firefox-fp", Fingerprint: "firefox"},
	{Name: "alt-sni", Fingerprint: "firefox", ServerName: altServerName},
}
