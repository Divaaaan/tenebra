package tenebracore

import (
	"encoding/json"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/profile"
)

// bridgeProfile builds a small stored profile from nodes with the real profile
// constructor, so server IDs are the same content-stable ones production assigns.
func bridgeProfile(t *testing.T, nodes ...model.Node) profile.Profile {
	t.Helper()
	p, err := profile.NewProfile("test", profile.SourceManual, "", nodes)
	if err != nil {
		t.Fatalf("NewProfile: %v", err)
	}
	return p
}

func vlessNode(name string) model.Node {
	return model.Node{
		Protocol: model.VLESS,
		Name:     name,
		Server:   name + ".example.test",
		Port:     443,
		UUID:     "11111111-1111-1111-1111-111111111111",
		TLS:      &model.TLS{Enabled: true, ServerName: name + ".example.test"},
	}
}

func TestGenerateConfigHappyPath(t *testing.T) {
	req := generateRequest{Profile: bridgeProfile(t, vlessNode("a"), vlessNode("b"))}
	reqJSON, _ := json.Marshal(req)

	out, err := generateConfig(string(reqJSON))
	if err != nil {
		t.Fatalf("generateConfig() error = %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	// A desktop-shaped config: one tun inbound and the proxy selector over the nodes.
	inbounds, ok := cfg["inbounds"].([]any)
	if !ok || len(inbounds) != 1 {
		t.Fatalf("inbounds = %v, want 1", cfg["inbounds"])
	}
	if _, ok := cfg["outbounds"].([]any); !ok {
		t.Fatalf("outbounds missing or not an array: %v", cfg["outbounds"])
	}
}

func TestGenerateConfigExternalTunOmitsAutoRoute(t *testing.T) {
	req := generateRequest{
		Profile: bridgeProfile(t, vlessNode("a")),
		Tun:     &tunOptions{ExternalTun: true, MTU: 1400, Stack: "gvisor"},
	}
	reqJSON, _ := json.Marshal(req)

	out, err := generateConfig(string(reqJSON))
	if err != nil {
		t.Fatalf("generateConfig() error = %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	in := cfg["inbounds"].([]any)[0].(map[string]any)
	if in["type"] != "tun" {
		t.Fatalf("inbound type = %v, want tun", in["type"])
	}
	// The whole point of externalTun: the platform owns routing, so these must be
	// absent from the emitted config even though the flag came in over the envelope.
	for _, k := range []string{"auto_route", "strict_route"} {
		if _, present := in[k]; present {
			t.Errorf("external tun must not emit %s in generated config, got %v", k, in[k])
		}
	}
}

func TestGenerateConfigErrors(t *testing.T) {
	if _, err := generateConfig("{not json"); err == nil {
		t.Error("malformed JSON: want error, got nil")
	}
	// A profile with no nodes has nothing usable to build.
	empty, _ := json.Marshal(generateRequest{Profile: bridgeProfile(t)})
	if _, err := generateConfig(string(empty)); err == nil {
		t.Error("empty profile: want a no-usable-nodes error, got nil")
	}
}
