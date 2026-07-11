package singbox

import (
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/routing"
)

// tlsFragmentOf reports an outbound's tls.fragment / fragment_fallback_delay and
// whether the outbound carries a tls object at all, so a test can assert both the
// TLS-bearing and the TLS-less protocols.
func tlsFragmentOf(o map[string]any) (fragment bool, delay string, hasTLS bool) {
	tls, ok := o["tls"].(map[string]any)
	if !ok {
		return false, "", false
	}
	frag, _ := tls["fragment"].(bool)
	d, _ := tls["fragment_fallback_delay"].(string)
	return frag, d, true
}

// TestTLSObjectEmitsFragment unit-tests tlsObject directly: a TLS block with
// Fragment set renders sing-box's tls.fragment plus the explicit fallback delay,
// and one without it emits neither key.
func TestTLSObjectEmitsFragment(t *testing.T) {
	on := tlsObject(&model.TLS{Enabled: true, ServerName: "e.test", Fragment: true})
	if on["fragment"] != true {
		t.Errorf("fragment = %v, want true", on["fragment"])
	}
	if on["fragment_fallback_delay"] != fragmentFallbackDelay {
		t.Errorf("fragment_fallback_delay = %v, want %q", on["fragment_fallback_delay"], fragmentFallbackDelay)
	}

	off := tlsObject(&model.TLS{Enabled: true, ServerName: "e.test"})
	if _, has := off["fragment"]; has {
		t.Errorf("fragment emitted for an unfragmented node: %v", off["fragment"])
	}
	if _, has := off["fragment_fallback_delay"]; has {
		t.Errorf("fragment_fallback_delay emitted for an unfragmented node: %v", off["fragment_fallback_delay"])
	}
}

// TestPerNodeFragmentRendersIntoOutbound is the per-node path (what the adaptive
// walk's fragment strategy sets): a node whose TLS carries Fragment renders
// tls.fragment in its outbound with no global toggle in play.
func TestPerNodeFragmentRendersIntoOutbound(t *testing.T) {
	node := model.Node{
		Protocol: model.VLESS, Name: "frag", Server: "f.test", Port: 443,
		UUID: "11111111-1111-1111-1111-111111111111",
		TLS:  &model.TLS{Enabled: true, ServerName: "f.test", Fragment: true},
	}
	cfg, err := Build([]model.Node{node}, "", routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	frag, delay, hasTLS := tlsFragmentOf(outboundsByTag(t, cfg)["frag"])
	if !hasTLS || !frag {
		t.Errorf("per-node fragment not rendered: fragment=%v hasTLS=%v", frag, hasTLS)
	}
	if delay != fragmentFallbackDelay {
		t.Errorf("fragment_fallback_delay = %q, want %q", delay, fragmentFallbackDelay)
	}
}

// TestNoFragmentByDefault guards that fragmentation is strictly opt-in: with the
// global toggle off and no node marked, no outbound carries tls.fragment.
func TestNoFragmentByDefault(t *testing.T) {
	by := outboundsByTag(t, buildFake(t))
	for tag, o := range by {
		if frag, _, _ := tlsFragmentOf(o); frag {
			t.Errorf("outbound %q carries fragment with the toggle off", tag)
		}
	}
}

// TestGlobalTLSFragmentMarksTLSOutbounds pins the global toggle: with
// routing.Options.TLSFragment set, every TLS-bearing outbound carries
// tls.fragment, while a TLS-less protocol (Shadowsocks) gains no tls object and no
// fragment. This is the whole-config counterpart to the per-node walk.
func TestGlobalTLSFragmentMarksTLSOutbounds(t *testing.T) {
	cfg, err := Build(fakeNodes(), "hy2",
		routing.Options{Mode: routing.ModeGlobal, TLSFragment: true}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	by := outboundsByTag(t, cfg)

	// TLS-bearing outbounds must all be fragmented, with the explicit fallback delay.
	for _, tag := range []string{"vless-reality", "vless-ws", "hy2", "trojan"} {
		frag, delay, hasTLS := tlsFragmentOf(by[tag])
		if !hasTLS {
			t.Errorf("%s should carry a tls object", tag)
			continue
		}
		if !frag {
			t.Errorf("%s not fragmented under the global toggle", tag)
		}
		if delay != fragmentFallbackDelay {
			t.Errorf("%s fragment_fallback_delay = %q, want %q", tag, delay, fragmentFallbackDelay)
		}
	}

	// Shadowsocks has no TLS handshake, so the toggle leaves it with no tls object —
	// nothing to fragment, and no bogus fragment field bolted onto a plain outbound.
	if _, has := by["ss"]["tls"]; has {
		t.Errorf("shadowsocks gained a tls object under the fragment toggle: %v", by["ss"]["tls"])
	}
}

// TestForceFragmentDoesNotMutateInput is the safety property the connect loop
// leans on: the global toggle marks a copy, never the caller's shared node slice
// or its TLS pointers.
func TestForceFragmentDoesNotMutateInput(t *testing.T) {
	nodes := fakeNodes()
	if _, err := Build(nodes, "hy2",
		routing.Options{Mode: routing.ModeGlobal, TLSFragment: true}, TunOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.TLS != nil && n.TLS.Fragment {
			t.Errorf("node %q TLS was mutated in place by the fragment toggle", n.Name)
		}
	}
}
