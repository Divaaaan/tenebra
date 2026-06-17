package control

import (
	"testing"

	"github.com/tenebra-vpn/tenebra/core/model"
	"github.com/tenebra-vpn/tenebra/core/profile"
	"github.com/tenebra-vpn/tenebra/core/routing"
	"github.com/tenebra-vpn/tenebra/core/singbox"
)

// TestNodesAndTagMatchesBuilder is the invariant that protects node selection:
// the tag nodesAndTag computes for the chosen server must equal the tag the
// singbox builder actually assigns it, so the selector default routes through
// the intended exit. We verify it by building a real config and reading back the
// selector's "default" when the chosen node is the only usable one — then it is
// forced to that node's tag rather than the first-tag fallback.
func TestNodesAndTagMatchesBuilder(t *testing.T) {
	cases := []struct {
		name    string
		servers []profile.Server
		// chosenIdx indexes into servers; that server's tag must be derivable.
		chosenIdx int
	}{
		{
			name: "single node",
			servers: []profile.Server{
				srv("a", model.VLESS, "Frankfurt"),
			},
			chosenIdx: 0,
		},
		{
			name: "duplicate names collide and suffix",
			servers: []profile.Server{
				srv("a", model.VLESS, "Node"),
				srv("b", model.Hysteria2, "Node"),
				srv("c", model.Trojan, "Node"),
			},
			chosenIdx: 2, // the third "Node" -> tag "Node-3"
		},
		{
			// The exit-node correctness case: an unknown-protocol node named "X"
			// takes the suffixed tag "X-2" and the builder then frees it; a later
			// node literally named "X-2" must reuse that freed tag. A mirror that
			// fails to free it computes "X-2-2", which is absent from the config,
			// so the builder silently falls back to the first node — the wrong
			// exit. This case fails if nodesAndTag stops mirroring the free.
			name: "unknown protocol frees a tag a later node reclaims",
			servers: []profile.Server{
				srv("a", model.VLESS, "X"),
				srv("b", model.Protocol("future-proto"), "X"), // dropped, frees "X-2"
				srv("c", model.Hysteria2, "X-2"),              // reclaims "X-2"
			},
			chosenIdx: 2,
		},
		{
			name: "zero protocol skipped",
			servers: []profile.Server{
				srv("a", model.VLESS, "Node"),
				{ID: "z", Node: model.Node{Name: "Node", Server: "z.example.com", Port: 1}}, // zero protocol
				srv("c", model.Hysteria2, "Node"),
			},
			chosenIdx: 2,
		},
		{
			name: "amneziawg endpoint counts toward tags",
			servers: []profile.Server{
				awg("a", "WG"),
				srv("b", model.VLESS, "WG"),
			},
			chosenIdx: 1, // second "WG" -> "WG-2"
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := profile.Profile{ID: "p", Name: "P", Source: profile.SourceManual, Servers: c.servers}
			chosen := c.servers[c.chosenIdx]

			nodes, selTag := nodesAndTag(p, chosen)
			if selTag == "" {
				t.Fatalf("nodesAndTag returned empty tag for a usable chosen node")
			}

			cfg, err := singbox.Build(nodes, selTag, routing.Options{Mode: routing.ModeSmart}, singbox.TunOptions{})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			gotDefault := selectorDefault(t, cfg)
			if gotDefault != selTag {
				t.Errorf("builder selector default = %q, nodesAndTag selTag = %q; they must agree", gotDefault, selTag)
			}
			// The chosen tag must actually be one of the selector's outbounds.
			if !selectorHasOutbound(t, cfg, selTag) {
				t.Errorf("selTag %q is not among the selector outbounds", selTag)
			}
		})
	}
}

// selectorDefault extracts the proxy selector's "default" field from a built
// config.
func selectorDefault(t *testing.T, cfg map[string]any) string {
	t.Helper()
	sel := proxySelector(t, cfg)
	def, _ := sel["default"].(string)
	return def
}

func selectorHasOutbound(t *testing.T, cfg map[string]any, tag string) bool {
	t.Helper()
	sel := proxySelector(t, cfg)
	outs, _ := sel["outbounds"].([]string)
	for _, o := range outs {
		if o == tag {
			return true
		}
	}
	return false
}

// proxySelector finds the selector outbound (tag "proxy") in a built config.
func proxySelector(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	outs, ok := cfg["outbounds"].([]map[string]any)
	if !ok {
		t.Fatalf("config outbounds has unexpected type %T", cfg["outbounds"])
	}
	for _, o := range outs {
		if o["type"] == "selector" {
			return o
		}
	}
	t.Fatal("no selector outbound in config")
	return nil
}

func srv(id string, proto model.Protocol, name string) profile.Server {
	n := model.Node{Protocol: proto, Name: name, Server: name + ".example.com", Port: 443}
	switch proto {
	case model.VLESS, model.VMess:
		n.UUID = "11111111-1111-1111-1111-111111111111"
	case model.Hysteria2, model.Trojan:
		n.Password = "pw"
	case model.Shadowsocks:
		n.Password = "pw"
		n.Method = "aes-256-gcm"
	}
	return profile.Server{ID: id, Node: n}
}

func awg(id, name string) profile.Server {
	return profile.Server{ID: id, Node: model.Node{
		Protocol: model.AmneziaWG,
		Name:     name,
		Server:   name + ".example.com",
		Port:     51820,
		WireGuard: &model.WireGuard{
			PrivateKey:    "cHJpdmF0ZQ==",
			PeerPublicKey: "cHVibGlj",
			LocalAddress:  []string{"10.0.0.2/32"},
		},
	}}
}
