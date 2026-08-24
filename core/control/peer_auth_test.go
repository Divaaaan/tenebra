package control

import (
	"errors"
	"testing"
)

// TestPeerAllowedSameIdentity: the peer whose identity matches the console user
// is admitted, and a clean allow raises no warning.
func TestPeerAllowedSameIdentity(t *testing.T) {
	warned := false
	ok := peerAllowed("501", "0", func() (string, error) { return "501", nil }, func(string) { warned = true })
	if !ok {
		t.Fatal("peer matching the console user must be allowed")
	}
	if warned {
		t.Error("a clean allow must not warn")
	}
}

// TestPeerAllowedDifferentIdentityDenied: a local user who is neither the daemon
// account nor the console user is turned away, with no fail-open warning.
func TestPeerAllowedDifferentIdentityDenied(t *testing.T) {
	ok := peerAllowed("502", "0", func() (string, error) { return "501", nil }, func(string) {
		t.Error("a decisive deny must not warn (that channel means fail-open)")
	})
	if ok {
		t.Fatal("a peer that is neither self nor the console user must be denied")
	}
}

// TestPeerAllowedSelfShortCircuits: the daemon's own account (root/SYSTEM) is
// always allowed, and the shortcut must not even consult the console lookup.
func TestPeerAllowedSelfShortCircuits(t *testing.T) {
	consulted := false
	ok := peerAllowed("0", "0", func() (string, error) { consulted = true; return "501", nil }, func(string) {})
	if !ok {
		t.Fatal("the daemon's own account must be allowed")
	}
	if consulted {
		t.Error("the self shortcut must not consult the console user")
	}
}

// TestPeerAllowedUndeterminableConsoleFailsClosed: when the console user can't
// be determined the policy REFUSES the peer and warns. This is the local
// privilege escalation the fail-open version handed out: the channel drives a
// LocalSystem/root process, so a caller the daemon cannot identify as the
// machine's user must not be acted for.
func TestPeerAllowedUndeterminableConsoleFailsClosed(t *testing.T) {
	warned := false
	ok := peerAllowed("502", "0", func() (string, error) { return "", errors.New("no console") }, func(string) { warned = true })
	if ok {
		t.Fatal("an undeterminable console user must fail CLOSED, not open")
	}
	if !warned {
		t.Error("a fail-closed refusal must emit a warning naming the reason")
	}
}

// TestPeerAllowedEmptyConsoleFailsClosed: an empty (but errorless) console
// result is likewise "unknown" and is refused with a warning — an empty string
// must never be compared against a peer as if it were an identity.
func TestPeerAllowedEmptyConsoleFailsClosed(t *testing.T) {
	warned := false
	ok := peerAllowed("502", "0", func() (string, error) { return "", nil }, func(string) { warned = true })
	if ok {
		t.Fatal("an empty console user must fail closed")
	}
	if !warned {
		t.Error("expected a warning naming the refusal's reason")
	}
}

// TestPeerAllowedUnidentifiablePeerDenied: a peer whose own identity could not
// be resolved is refused before the console is consulted. The transport hands us
// "" for a lookup that failed, and "" is not an identity — matching it against
// anything (including an unlucky "" console answer) would be an accident.
func TestPeerAllowedUnidentifiablePeerDenied(t *testing.T) {
	warned := false
	ok := peerAllowed("", "0", func() (string, error) {
		t.Error("an unidentifiable peer must be refused without consulting the console user")
		return "", nil
	}, func(string) { warned = true })
	if ok {
		t.Fatal("a peer with no resolvable identity must be denied")
	}
	if !warned {
		t.Error("expected a warning naming the refusal's reason")
	}
}

// TestPeerAllowedEmptySelfIsNotBlanketAllow: a failed self-lookup (self="") must
// not let an empty-identity peer through the self shortcut.
func TestPeerAllowedEmptySelfIsNotBlanketAllow(t *testing.T) {
	ok := peerAllowed("", "", func() (string, error) { return "501", nil }, func(string) {})
	if ok {
		t.Fatal("an empty peer must not be admitted by the self shortcut")
	}
}

// TestPeerPrivilegedSameAccountAsDaemon: when the core runs as an ordinary
// process of the user who owns the GUI, peer and self are the same account and
// there is no boundary to cross — the bypass commands stay available without any
// administrative check, which is the non-service install.
func TestPeerPrivilegedSameAccountAsDaemon(t *testing.T) {
	if !peerPrivileged("501", "501", false) {
		t.Fatal("a peer running as the daemon's own account must hold its authority")
	}
}

// TestPeerPrivilegedAdminPeer: an administrator talking to the service holds the
// authority already (it can replace the service outright), so the gated commands
// are open to it.
func TestPeerPrivilegedAdminPeer(t *testing.T) {
	if !peerPrivileged("501", "S-1-5-18", true) {
		t.Fatal("an administrative peer must hold the daemon's authority")
	}
}

// TestPeerPrivilegedOrdinaryUserDenied: the case the escalation ran through —
// an ordinary interactive user, admitted to the channel, talking to a
// LocalSystem service. Admitted is not the same as authorized.
func TestPeerPrivilegedOrdinaryUserDenied(t *testing.T) {
	if peerPrivileged("501", "S-1-5-18", false) {
		t.Fatal("a non-administrative peer must not hold the service's authority")
	}
}

// TestPeerPrivilegedEmptyIdentitiesDoNotMatch: an unresolved identity on both
// sides must not collapse into a self match and grant authority.
func TestPeerPrivilegedEmptyIdentitiesDoNotMatch(t *testing.T) {
	if peerPrivileged("", "", false) {
		t.Fatal("two unresolved identities must not be treated as the same account")
	}
}
