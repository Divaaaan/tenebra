package control

import (
	"context"
	"fmt"
)

// consoleUser reports the identity the interactive GUI is expected to run as —
// a numeric uid string on unix, a user SID string on Windows. A non-nil error
// (or an empty string) means the console user could not be determined: no one
// is logged in at the physical console, or the OS lookup failed. The policy
// treats that as "unknown" and refuses (see peerAllowed).
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
//   - If the console user cannot be determined, the policy FAILS CLOSED and
//     warns. An identity the daemon cannot establish is not an identity it may
//     act for: the whole channel drives a LocalSystem/root process, so admitting
//     an unknown caller hands the machine to whoever asked first. The cost is
//     real — a session mid-transition or a host with no session manager cannot
//     attach its GUI until the lookup answers — but a stuck GUI is recoverable
//     and a privilege escalation is not. The warning names the reason so the
//     refusal is diagnosable from the daemon log instead of looking like a bug.
func peerAllowed(peer, self string, console consoleUser, warn func(string)) bool {
	if peer != "" && peer == self {
		return true
	}
	if peer == "" {
		warn("control: refusing a peer whose identity could not be established")
		return false
	}
	cu, err := console()
	if err != nil || cu == "" {
		warn(fmt.Sprintf("control: cannot determine console user (err=%v); refusing peer %q (fail-closed)", err, peer))
		return false
	}
	return peer == cu
}

// peerPrivileged reports whether an already-admitted peer holds the daemon's own
// authority, which is what the commands in adminOnlyCommands need. Two ways to
// hold it, and they are not the same statement:
//
//   - peer == self: there is no privilege boundary to cross. This is the core
//     running as an ordinary process of the user who owns the GUI (the stdio
//     sidecar, or `--pipe` started from that user's console) — the daemon is
//     already that user, so handing it code changes nothing about who can run
//     what. Note the "" guard: an unresolved identity on either side must not
//     collapse into a match.
//   - admin: the peer's token/uid carries administrative rights (a member of
//     Administrators with the group enabled on Windows, uid 0 on unix). Such a
//     caller can install a service or rewrite the daemon's binary anyway, so
//     letting it place code in the daemon's directory grants it nothing new.
//
// Everyone else — the ordinary interactive user talking to the LocalSystem
// service, including an administrator's UAC-filtered token, whose Administrators
// membership is deny-only — is admitted to the channel but not to these
// commands. That is the whole point: the filtered token is what UAC exists to
// make ask.
func peerPrivileged(peer, self string, admin bool) bool {
	if peer != "" && peer == self {
		return true
	}
	return admin
}

// adminOnlyCommands are the control commands through which a caller can hand the
// daemon executable code of its own choosing. In a service installation the
// bundle directory is clamped to SYSTEM+Administrators precisely so an
// unprivileged user cannot plant something the service will trust (see
// secureDataDir), and the daemon runs the bundle's .bat through cmd.exe as
// LocalSystem — so a command that writes caller-supplied files there without a
// privilege check hands that caller SYSTEM without ever showing a UAC prompt.
//
// The line is *supplying* the code, not running it. Everything the daemon starts
// out of that directory it put there itself: an archive downloaded from the
// pinned upstream release and matched against a checksum compiled into this
// binary, or the copy embedded in the binary. A caller cannot influence those
// bytes, and the directory's own ACL keeps it from adding to them.
//
// So what is deliberately NOT here:
//
//   - start_zapret, pick_zapret: they attach a filter that is already installed
//     and already trusted. Gating them was this list's one real mistake: the
//     desktop shell runs as the ordinary interactive user and holds a
//     UAC-filtered token, so gating them did not prompt anybody — it removed the
//     bypass from the product for every user who did not know to relaunch the
//     app elevated, which is all of them. Refusing to start already-trusted code
//     buys nothing: the same caller may raise and drop the whole tunnel.
//   - update_zapret: it fetches the pinned upstream release and verifies its
//     checksum before unpacking. The caller supplies no bytes and chooses no
//     version, and the same fetch runs unattended on a timer anyway.
//   - stop_zapret, set_zapret_auto_update: neither places nor starts code, and
//     gating the flag would also stop an ordinary user turning it OFF, which is
//     the safer direction.
//   - connect: it may auto-start an installed bundle, for the same reason
//     start_zapret may.
var adminOnlyCommands = map[string]struct{}{
	CmdImportZapret: {},
}

// requiresAdminPeer reports whether cmd may only be run by a peer that already
// holds the daemon's authority.
func requiresAdminPeer(cmd string) bool {
	_, ok := adminOnlyCommands[cmd]
	return ok
}

// peerPrivilegeKey is the context key under which a session records what the
// peer on the other end of its stream is allowed to ask for.
type peerPrivilegeKey struct{}

// withPeerPrivilege stamps a request context with the privilege of the peer the
// request came from. Every transport that carries an EXTERNAL peer sets it:
// ServeListener stamps each session from the credentials the kernel attached to
// the accepted connection, and Serve stamps the stdio sidecar, whose "peer" is
// the parent process that spawned this one and therefore already holds at least
// its privileges.
func withPeerPrivilege(ctx context.Context, privileged bool) context.Context {
	return context.WithValue(ctx, peerPrivilegeKey{}, privileged)
}

// peerPrivilegeFrom reports the privilege recorded for the peer a request is
// being served for. An unstamped context means there is no external peer at all
// — an in-process call, which is the daemon acting on its own behalf (its own
// background jobs, and unit tests calling Handle directly) — so it carries the
// daemon's authority. That is not a fail-open default: both transports that can
// introduce a caller from outside this process stamp the context unconditionally
// before a single request is dispatched.
func peerPrivilegeFrom(ctx context.Context) bool {
	privileged, stamped := ctx.Value(peerPrivilegeKey{}).(bool)
	if !stamped {
		return true
	}
	return privileged
}
