package fallback

import (
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
)

// TestDefaultStrategiesCascade pins the shape of the escalation cascade: the
// first rung reshapes nothing (a node connects on its own parameters whenever it
// can), and each subsequent rung is at least as divergent as the last, so the
// walk only reaches for a heavier variation after a lighter one has failed.
func TestDefaultStrategiesCascade(t *testing.T) {
	if len(DefaultStrategies) < 2 {
		t.Fatalf("cascade has %d strategies, want at least 2 (default + an escalation)", len(DefaultStrategies))
	}
	if !DefaultStrategies[0].IsDefault() {
		t.Errorf("first strategy = %+v, want the default (no reshaping)", DefaultStrategies[0])
	}
	for i, s := range DefaultStrategies {
		if s.Name == "" {
			t.Errorf("strategy %d has no name", i)
		}
		if i > 0 && s.IsDefault() {
			t.Errorf("strategy %d (%q) reshapes nothing but is not the lead rung", i, s.Name)
		}
	}
	// The fingerprint is introduced before the SNI is also reshaped: the divergence
	// is monotonic, so escalation never steps back toward the node's own params.
	if DefaultStrategies[1].Fingerprint == "" {
		t.Errorf("second rung %+v does not reshape the fingerprint", DefaultStrategies[1])
	}
	last := DefaultStrategies[len(DefaultStrategies)-1]
	if last.ServerName == "" {
		t.Errorf("last rung %+v does not reshape the SNI", last)
	}
}

// TestStrategyApplyReshapesTLS confirms a non-default strategy overrides the
// node's uTLS fingerprint and SNI while leaving the rest of the node intact.
func TestStrategyApplyReshapesTLS(t *testing.T) {
	node := model.Node{
		Protocol: model.VLESS,
		Server:   "example.test",
		Port:     443,
		UUID:     "uuid-1",
		TLS: &model.TLS{
			Enabled:     true,
			ServerName:  "origin.example",
			Fingerprint: "chrome",
			Reality:     &model.Reality{PublicKey: "pk"},
		},
	}
	s := Strategy{Name: "alt-sni", Fingerprint: "firefox", ServerName: "alt.example"}
	got := s.ApplyTo(node)

	if got.TLS.Fingerprint != "firefox" {
		t.Errorf("fingerprint = %q, want firefox", got.TLS.Fingerprint)
	}
	if got.TLS.ServerName != "alt.example" {
		t.Errorf("server_name = %q, want alt.example", got.TLS.ServerName)
	}
	// Untouched fields survive, including the REALITY block (still pointed at the
	// node's key, so the reshaped node is still a valid REALITY outbound).
	if got.TLS.Reality == nil || got.TLS.Reality.PublicKey != "pk" {
		t.Errorf("reality block lost or altered: %+v", got.TLS.Reality)
	}
	if got.UUID != "uuid-1" || got.Server != "example.test" || got.Port != 443 {
		t.Errorf("non-TLS fields changed: %+v", got)
	}
}

// TestStrategyApplyDoesNotMutateInput is the safety property the connect loop
// relies on: it reuses one node slice across every attempt, so ApplyTo must never
// write through to the caller's node or its shared TLS pointer.
func TestStrategyApplyDoesNotMutateInput(t *testing.T) {
	orig := &model.TLS{Enabled: true, ServerName: "origin.example", Fingerprint: "chrome"}
	node := model.Node{Protocol: model.VLESS, TLS: orig}

	_ = Strategy{Name: "firefox-fp", Fingerprint: "firefox"}.ApplyTo(node)

	if orig.Fingerprint != "chrome" || orig.ServerName != "origin.example" {
		t.Errorf("input TLS was mutated: %+v", orig)
	}
}

// TestStrategyApplyLeavesUntouched covers the two no-op paths: the default
// strategy reshapes nothing, and a node without a TLS block has no handshake to
// reshape.
func TestStrategyApplyLeavesUntouched(t *testing.T) {
	tlsNode := model.Node{Protocol: model.VLESS, TLS: &model.TLS{Enabled: true, Fingerprint: "chrome"}}
	if got := (Strategy{Name: "default"}).ApplyTo(tlsNode); got.TLS.Fingerprint != "chrome" {
		t.Errorf("default strategy reshaped the node: %+v", got.TLS)
	}

	noTLS := model.Node{Protocol: model.AmneziaWG, WireGuard: &model.WireGuard{PrivateKey: "k", PeerPublicKey: "p"}}
	got := Strategy{Name: "firefox-fp", Fingerprint: "firefox"}.ApplyTo(noTLS)
	if got.TLS != nil {
		t.Errorf("a TLS-less node gained a TLS block: %+v", got.TLS)
	}
}
