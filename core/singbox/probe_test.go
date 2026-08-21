package singbox

import (
	"encoding/json"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
)

// probeNodes returns three dialable nodes with distinct names.
func probeNodes() []model.Node {
	return []model.Node{
		{Protocol: model.VLESS, Name: "de-fra", Server: "fra.example.test", Port: 8443,
			UUID: "11111111-1111-1111-1111-111111111111"},
		{Protocol: model.Trojan, Name: "fi-hel", Server: "hel.example.test", Port: 443,
			Password: "pw"},
		{Protocol: model.Hysteria2, Name: "de-het", Server: "het.example.test", Port: 443,
			Password: "pw"},
	}
}

func TestBuildProbeGivesEachNodeItsOwnPort(t *testing.T) {
	cfg, bindings, err := BuildProbe(probeNodes(), 24100)
	if err != nil {
		t.Fatalf("BuildProbe: %v", err)
	}
	if len(bindings) != 3 {
		t.Fatalf("got %d bindings, want 3", len(bindings))
	}

	seenPort := map[int]bool{}
	seenTag := map[string]bool{}
	for i, b := range bindings {
		if want := 24100 + i; b.Port != want {
			t.Errorf("binding %d port = %d, want %d", i, b.Port, want)
		}
		if seenPort[b.Port] {
			t.Errorf("port %d assigned twice", b.Port)
		}
		if seenTag[b.Tag] {
			t.Errorf("tag %q assigned twice", b.Tag)
		}
		seenPort[b.Port], seenTag[b.Tag] = true, true
	}

	if got := []string{bindings[0].Name, bindings[1].Name, bindings[2].Name}; got[0] != "de-fra" ||
		got[1] != "fi-hel" || got[2] != "de-het" {
		t.Errorf("names = %v, want the input node names", got)
	}

	ins, _ := cfg["inbounds"].([]map[string]any)
	if len(ins) != 3 {
		t.Fatalf("got %d inbounds, want 3", len(ins))
	}
	for _, in := range ins {
		if in["type"] != "mixed" {
			t.Errorf("inbound type = %v, want mixed", in["type"])
		}
		if in["listen"] != "127.0.0.1" {
			t.Errorf("inbound listen = %v, want loopback only", in["listen"])
		}
	}
}

func TestBuildProbeRoutesEachListenerToItsOwnNode(t *testing.T) {
	cfg, bindings, err := BuildProbe(probeNodes(), 24100)
	if err != nil {
		t.Fatalf("BuildProbe: %v", err)
	}
	route, _ := cfg["route"].(map[string]any)
	rules, _ := route["rules"].([]map[string]any)

	// First rule is the sniff action; the rest pin one inbound to one outbound.
	if len(rules) != len(bindings)+1 {
		t.Fatalf("got %d rules, want %d (sniff + one per node)", len(rules), len(bindings)+1)
	}
	if rules[0]["action"] != "sniff" {
		t.Fatalf("first rule = %v, want the sniff action", rules[0])
	}
	for i, b := range bindings {
		r := rules[i+1]
		in, _ := r["inbound"].([]string)
		if len(in) != 1 || in[0] != probeInboundTag(i) {
			t.Errorf("rule %d inbound = %v, want %q", i, r["inbound"], probeInboundTag(i))
		}
		if r["outbound"] != b.Tag {
			t.Errorf("rule %d outbound = %v, want %q", i, r["outbound"], b.Tag)
		}
	}
}

// A probe that escapes its rule must fail, not egress unproxied: a request that
// leaked to the open internet would time the *direct* link and report a dead
// node as healthy — the exact misdiagnosis this path exists to prevent.
func TestBuildProbeBlocksInsteadOfLeakingDirect(t *testing.T) {
	cfg, _, err := BuildProbe(probeNodes(), 24100)
	if err != nil {
		t.Fatalf("BuildProbe: %v", err)
	}
	route, _ := cfg["route"].(map[string]any)
	if route["final"] != blockTag {
		t.Errorf("route.final = %v, want %q", route["final"], blockTag)
	}

	outs, _ := cfg["outbounds"].([]map[string]any)
	for _, o := range outs {
		if o["type"] == "direct" {
			t.Fatalf("probe config carries a direct outbound: %v", o)
		}
	}
}

// The probe runs while a tunnel may already be up. A tun inbound would install a
// second default route and take the machine's connectivity down with it.
func TestBuildProbeNeverBuildsATun(t *testing.T) {
	cfg, _, err := BuildProbe(probeNodes(), 24100)
	if err != nil {
		t.Fatalf("BuildProbe: %v", err)
	}
	ins, _ := cfg["inbounds"].([]map[string]any)
	for _, in := range ins {
		if in["type"] == "tun" {
			t.Fatalf("probe config carries a tun inbound: %v", in)
		}
		if _, ok := in["auto_route"]; ok {
			t.Fatalf("probe inbound sets auto_route: %v", in)
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{`"tun"`, `"auto_route"`, `"strict_route"`} {
		if containsSub(string(raw), forbidden) {
			t.Errorf("probe config mentions %s", forbidden)
		}
	}
}

func TestBuildProbeSkipsUnusableNodes(t *testing.T) {
	nodes := []model.Node{
		{Protocol: model.VLESS, Name: "good", Server: "a.example.test", Port: 443,
			UUID: "11111111-1111-1111-1111-111111111111"},
		// Keyless REALITY: sing-box cannot handshake it, so there is nothing to
		// measure and it must not consume a port.
		{Protocol: model.VLESS, Name: "keyless", Server: "b.example.test", Port: 443,
			UUID: "22222222-2222-2222-2222-222222222222",
			TLS:  &model.TLS{Enabled: true, Reality: &model.Reality{}}},
		{Protocol: model.Trojan, Name: "no-password", Server: "c.example.test", Port: 443},
	}
	_, bindings, err := BuildProbe(nodes, 24100)
	if err != nil {
		t.Fatalf("BuildProbe: %v", err)
	}
	if len(bindings) != 1 || bindings[0].Name != "good" {
		t.Fatalf("bindings = %+v, want only the usable node", bindings)
	}
}

func TestBuildProbeRejectsBadInput(t *testing.T) {
	if _, _, err := BuildProbe(nil, 24100); err == nil {
		t.Error("no nodes: want an error")
	}
	if _, _, err := BuildProbe(probeNodes(), 0); err == nil {
		t.Error("port 0: want an error")
	}
	if _, _, err := BuildProbe(probeNodes(), 65535); err == nil {
		t.Error("range past 65535: want an error")
	}
}

// containsSub is strings.Contains, kept local so the test file adds no import
// the package does not already carry.
func containsSub(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
