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

// TestPeerAllowedUndeterminableConsoleFailsOpen: when the console user can't be
// determined the policy allows the peer and warns — never fail-closed, which
// would brick GUI attach on edge cases.
func TestPeerAllowedUndeterminableConsoleFailsOpen(t *testing.T) {
	warned := false
	ok := peerAllowed("502", "0", func() (string, error) { return "", errors.New("no console") }, func(string) { warned = true })
	if !ok {
		t.Fatal("an undeterminable console user must fail OPEN, not closed")
	}
	if !warned {
		t.Error("a fail-open decision must emit a warning")
	}
}

// TestPeerAllowedEmptyConsoleFailsOpen: an empty (but errorless) console result
// is likewise "unknown" and fails open with a warning.
func TestPeerAllowedEmptyConsoleFailsOpen(t *testing.T) {
	warned := false
	ok := peerAllowed("502", "0", func() (string, error) { return "", nil }, func(string) { warned = true })
	if !ok {
		t.Fatal("an empty console user must fail open")
	}
	if !warned {
		t.Error("expected a fail-open warning")
	}
}

// TestPeerAllowedEmptySelfIsNotBlanketAllow: a failed self-lookup (self="") must
// not let an empty-identity peer through the self shortcut; the console check
// still governs.
func TestPeerAllowedEmptySelfIsNotBlanketAllow(t *testing.T) {
	ok := peerAllowed("", "", func() (string, error) { return "501", nil }, func(string) {})
	if ok {
		t.Fatal("an empty peer must not be admitted by the self shortcut")
	}
}
