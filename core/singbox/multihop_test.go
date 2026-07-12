package singbox

import (
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/routing"
)

// These tests cover the multihop chain the builder emits: the exit outbound gains
// a detour through the entry outbound, the selector collapses to the exit so the
// route final egresses via exit -> entry, and — crucially — the whole thing degrades
// to the normal single-hop selector for any selection that can't form a real chain
// (missing tag, equal endpoints, an AmneziaWG endpoint that isn't a regular
// outbound), never a config carrying a dangling detour. TestMultihopPassesSingBoxCheck
// validates the emitted shape against a real sing-box.

// selectorOf returns the proxy selector object from a built config.
func selectorOf(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	sel, ok := outboundsByTag(t, cfg)[proxyTag]
	if !ok {
		t.Fatal("no proxy selector in config")
	}
	return sel
}

// TestMultihopChainSetsDetourAndNarrowsSelector: with a valid entry/exit pair the
// exit outbound carries detour=<entry tag>, the entry carries none, and the
// selector lists only the exit (default exit) while the route final stays proxyTag.
func TestMultihopChainSetsDetourAndNarrowsSelector(t *testing.T) {
	cfg, err := Build(checkNodes(), "", routing.Options{
		Mode:          routing.ModeGlobal,
		Multihop:      true,
		MultihopEntry: "vless-ws",
		MultihopExit:  "hy2",
	}, TunOptions{})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	by := outboundsByTag(t, cfg)
	if got := by["hy2"]["detour"]; got != "vless-ws" {
		t.Errorf("exit outbound detour = %v, want the entry tag %q", got, "vless-ws")
	}
	if _, ok := by["vless-ws"]["detour"]; ok {
		t.Error("entry outbound must not carry a detour; it is the chain's first hop")
	}

	sel := selectorOf(t, cfg)
	outs, _ := sel["outbounds"].([]string)
	if len(outs) != 1 || outs[0] != "hy2" {
		t.Errorf("selector outbounds = %v, want only the exit [hy2]", outs)
	}
	if sel["default"] != "hy2" {
		t.Errorf("selector default = %v, want the exit hy2", sel["default"])
	}

	// The route final still targets the selector, so exit -> entry chaining is
	// reached without touching the routing block.
	if final := cfg["route"].(map[string]any)["final"]; final != proxyTag {
		t.Errorf("route final = %v, want %q so the chain is reached via the selector", final, proxyTag)
	}
}

// TestMultihopDefaultsOff: without the toggle the config is a plain single-hop
// selector over every node and no outbound carries a detour.
func TestMultihopDefaultsOff(t *testing.T) {
	cfg, err := Build(checkNodes(), "", routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	for tag, o := range outboundsByTag(t, cfg) {
		if _, ok := o["detour"]; ok {
			t.Errorf("outbound %q carries a detour with multihop off", tag)
		}
	}
	sel := selectorOf(t, cfg)
	if outs, _ := sel["outbounds"].([]string); len(outs) != 2 {
		t.Errorf("selector lists %d outbounds, want both nodes with multihop off", len(outs))
	}
}

// TestMultihopInertOnUnresolvableSelection: a selection the builder can't turn into
// a real two-hop chain must leave the normal single-hop selector untouched rather
// than emit a dangling detour (which sing-box accepts and then silently misroutes).
func TestMultihopInertOnUnresolvableSelection(t *testing.T) {
	cases := []struct {
		name        string
		entry, exit string
	}{
		{"missing entry tag", "ghost", "hy2"},
		{"missing exit tag", "vless-ws", "ghost"},
		{"equal endpoints", "hy2", "hy2"},
		{"empty entry", "", "hy2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Build(checkNodes(), "", routing.Options{
				Mode:          routing.ModeGlobal,
				Multihop:      true,
				MultihopEntry: c.entry,
				MultihopExit:  c.exit,
			}, TunOptions{})
			if err != nil {
				t.Fatalf("Build() error: %v", err)
			}
			for tag, o := range outboundsByTag(t, cfg) {
				if _, ok := o["detour"]; ok {
					t.Errorf("outbound %q carries a detour for an unresolvable multihop selection", tag)
				}
			}
			if outs, _ := selectorOf(t, cfg)["outbounds"].([]string); len(outs) != 2 {
				t.Errorf("selector narrowed to %d outbounds; an unresolvable selection must keep the full selector", len(outs))
			}
		})
	}
}

// TestMultihopInertWhenEndpointIsWireGuard: an AmneziaWG node is emitted as a
// top-level endpoint, not a regular outbound, so it can neither carry a detour nor
// be one. Selecting it as the exit leaves the config single-hop.
func TestMultihopInertWhenEndpointIsWireGuard(t *testing.T) {
	nodes := []model.Node{
		{
			Protocol: model.VLESS, Name: "vless-ws", Server: "ws.example.test", Port: 443,
			UUID: "22222222-2222-2222-2222-222222222222",
			TLS:  &model.TLS{Enabled: true, ServerName: "ws.example.test"},
		},
		{
			Protocol: model.AmneziaWG, Name: "awg", Server: "wg.example.test", Port: 51820,
			WireGuard: &model.WireGuard{PrivateKey: "cHJpdmF0ZQ==", PeerPublicKey: "cHVibGlj", LocalAddress: []string{"10.0.0.2/32"}},
		},
	}
	cfg, err := Build(nodes, "", routing.Options{
		Mode:          routing.ModeGlobal,
		Multihop:      true,
		MultihopEntry: "vless-ws",
		MultihopExit:  "awg",
	}, TunOptions{})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if _, ok := outboundsByTag(t, cfg)["vless-ws"]["detour"]; ok {
		t.Error("a WireGuard-endpoint exit must not chain: no detour should be set")
	}
}

// TestMultihopPassesSingBoxCheck feeds a real `sing-box check` a multihop config, so
// the emitted detour field is validated against the bundled sing-box schema rather
// than only the offline assertions. sing-box does not reject a dangling detour, so
// this guards the emitted shape, not the builder's own resolution guards (covered
// above). Skipped when no sing-box binary is available, like the sibling checks.
func TestMultihopPassesSingBoxCheck(t *testing.T) {
	bin, _, ok := findSingBox()
	if !ok {
		t.Skip("sing-box binary not found (resources/ or bin/ or PATH); skipping real config check")
	}
	cfg, err := Build(checkNodes(), "", routing.Options{
		Mode:          routing.ModeGlobal,
		Multihop:      true,
		MultihopEntry: "vless-ws",
		MultihopExit:  "hy2",
	}, TunOptions{})
	if err != nil {
		t.Fatalf("build multihop config: %v", err)
	}
	singBoxCheck(t, bin, cfg)
}
