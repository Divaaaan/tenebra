package fallback

import "testing"

// TestClassifyFailure walks every observable signal combination the classifier
// must separate, since the verdict decides the walk's next move: a Censored node
// is re-tried with another transport strategy, a Dead one is abandoned, and an
// ambiguous one is left Unknown rather than guessed. The two fingerprints that
// matter most are a fast reset (Dead) versus a silent stall after a good TCP
// connect (Censored) — the pair a real interference event turns on.
func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name string
		sig  FailureSignal
		want FailureClass
	}{
		{
			// The interference fingerprint: the entry accepts a TCP connection but
			// the handshake then stalls silently. This is the one case that escalates.
			name: "connected then silent handshake stall is censored",
			sig:  FailureSignal{TCP: TCPConnected, HandshakeStalled: true},
			want: Censored,
		},
		{
			// A reachable entry whose handshake failed fast is a broken/mismatched
			// server, not the silent-stall fingerprint — do not escalate.
			name: "connected then fast handshake error is unknown",
			sig:  FailureSignal{TCP: TCPConnected, HandshakeStalled: false},
			want: Unknown,
		},
		{
			// A reset (connection refused) is a reachable host with nothing
			// listening: a dead entry no handshake reshaping can revive.
			name: "tcp refused is dead",
			sig:  FailureSignal{TCP: TCPRefused, HandshakeStalled: false},
			want: Dead,
		},
		{
			// A reset is dead regardless of what the through-tunnel probe reported:
			// the reset is definitive.
			name: "tcp refused stays dead even if probe stalled",
			sig:  FailureSignal{TCP: TCPRefused, HandshakeStalled: true},
			want: Dead,
		},
		{
			// No route / unreachable network: the entry cannot be reached at all.
			name: "tcp unreachable is dead",
			sig:  FailureSignal{TCP: TCPUnreachable, HandshakeStalled: false},
			want: Dead,
		},
		{
			// SYN drew no answer and no reset: a downed host and a SYN-level block
			// are indistinguishable, so the verdict is ambiguous.
			name: "tcp timeout is unknown",
			sig:  FailureSignal{TCP: TCPTimedOut, HandshakeStalled: false},
			want: Unknown,
		},
		{
			// A stalled through-tunnel probe does NOT upgrade a SYN timeout to
			// censored: without a completed TCP connect there is no fingerprint to
			// key on, only ambiguity.
			name: "tcp timeout stays unknown even if probe stalled",
			sig:  FailureSignal{TCP: TCPTimedOut, HandshakeStalled: true},
			want: Unknown,
		},
		{
			// No usable TCP observation at all: nothing to conclude.
			name: "indeterminate tcp is unknown",
			sig:  FailureSignal{TCP: TCPIndeterminate, HandshakeStalled: true},
			want: Unknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyFailure(tt.sig); got != tt.want {
				t.Errorf("ClassifyFailure(%+v) = %v, want %v", tt.sig, got, tt.want)
			}
		})
	}
}

// TestFailureClassString pins the lowercase tokens the reason annotation and log
// lines depend on.
func TestFailureClassString(t *testing.T) {
	tests := []struct {
		c    FailureClass
		want string
	}{
		{Unknown, "unknown"},
		{Dead, "dead"},
		{Censored, "censored"},
		{FailureClass(99), "unknown"}, // an unrecognised value renders as unknown
	}
	for _, tt := range tests {
		if got := tt.c.String(); got != tt.want {
			t.Errorf("FailureClass(%d).String() = %q, want %q", tt.c, got, tt.want)
		}
	}
}
