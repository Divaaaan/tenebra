package control

import "fmt"

// consoleUser reports the identity the interactive GUI is expected to run as —
// a numeric uid string on unix, a user SID string on Windows. A non-nil error
// (or an empty string) means the console user could not be determined: no one
// is logged in at the physical console, or the OS lookup failed. The policy
// treats that as "unknown" and fails open (see peerAllowed).
type consoleUser func() (string, error)

// peerAllowed is the peer-authentication policy, factored out as a pure function
// so it can be tested exhaustively without a socket. It narrows the historical
// Tailscale-style trust — "any local user may drive the daemon", which the
// control channel grants by design because the socket/pipe is world-reachable
// (see docs/control-protocol.md) — down to "the logged-in user, plus the
// daemon's own account". The GUI must therefore run as the interactive/console
// user for its attach to succeed; an elevated helper running as the daemon's own
// account (root/LocalSystem) is admitted too.
//
//   - self is the daemon's own identity (root uid "0" on unix, the LocalSystem
//     SID on Windows). It is always allowed: the daemon may talk to itself, and
//     a same-account elevated tool is not a privilege escalation over what it
//     already is. An empty self never matches (the "" peer is likewise rejected
//     from this shortcut), so a failed self-lookup can't turn into a blanket
//     allow here.
//   - Otherwise the peer is allowed iff it matches the console user.
//   - If the console user cannot be determined, the policy FAILS OPEN with a
//     warning rather than closed. A wrong "deny" here bricks the unprivileged
//     GUI's attach — the product's core interaction — on legitimate edge cases
//     (fast user switching, a session in the middle of coming up, a transient OS
//     lookup failure), which is a worse outcome than briefly retaining the old
//     any-local-user exposure. The warning makes the degraded state auditable in
//     the daemon log instead of silently widening or narrowing the trust.
func peerAllowed(peer, self string, console consoleUser, warn func(string)) bool {
	if peer != "" && peer == self {
		return true
	}
	cu, err := console()
	if err != nil || cu == "" {
		warn(fmt.Sprintf("control: cannot determine console user (err=%v); allowing peer %q (fail-open)", err, peer))
		return true
	}
	return peer == cu
}
