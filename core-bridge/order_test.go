package tenebracore

import (
	"encoding/json"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
)

func hy2Node(name string) model.Node {
	return model.Node{Protocol: model.Hysteria2, Name: name, Server: name + ".example.test", Port: 8443, Password: "pw"}
}

func TestOrderNodesProtocolPreference(t *testing.T) {
	// Input order puts the Hysteria2 node before the VLESS node; the default
	// anti-DPI order prefers VLESS (REALITY) first, so the result must reorder.
	p := bridgeProfile(t, hy2Node("h"), vlessNode("v"))
	reqJSON, _ := json.Marshal(orderRequest{Profile: p})

	out, err := OrderNodes(string(reqJSON))
	if err != nil {
		t.Fatalf("OrderNodes() error = %v", err)
	}
	var tags []string
	if err := json.Unmarshal([]byte(out), &tags); err != nil {
		t.Fatalf("output is not a JSON array: %v", err)
	}
	if len(tags) != 2 || tags[0] != "v" || tags[1] != "h" {
		t.Errorf("order = %v, want [v h] (VLESS before Hysteria2)", tags)
	}
}

func TestOrderNodesLastGoodLeads(t *testing.T) {
	p := bridgeProfile(t, vlessNode("v"), hy2Node("h"))
	// The Hysteria2 server ranks second by preference; making it last-good must
	// pull it to the front of the walk.
	lastID := p.Servers[1].ID
	reqJSON, _ := json.Marshal(orderRequest{Profile: p, LastGood: lastID})

	out, err := OrderNodes(string(reqJSON))
	if err != nil {
		t.Fatalf("OrderNodes() error = %v", err)
	}
	var tags []string
	if err := json.Unmarshal([]byte(out), &tags); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(tags) == 0 || tags[0] != "h" {
		t.Errorf("order = %v, want last-good tag 'h' first", tags)
	}
}

func TestOrderNodesByLatency(t *testing.T) {
	p := bridgeProfile(t, vlessNode("v"), hy2Node("h"))
	// v is slower than h; fastest-first must put h before v despite the protocol
	// preference that would otherwise lead with v.
	latency := map[string]int64{p.Servers[0].ID: 200, p.Servers[1].ID: 20}
	reqJSON, _ := json.Marshal(orderRequest{Profile: p, Latency: latency})

	out, err := OrderNodes(string(reqJSON))
	if err != nil {
		t.Fatalf("OrderNodes() error = %v", err)
	}
	var tags []string
	if err := json.Unmarshal([]byte(out), &tags); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(tags) != 2 || tags[0] != "h" || tags[1] != "v" {
		t.Errorf("latency order = %v, want [h v] (fastest first)", tags)
	}
}

func TestOrderNodesEmptyAndError(t *testing.T) {
	// An empty profile yields a deterministic empty array — not null, not an error.
	empty, _ := json.Marshal(orderRequest{Profile: bridgeProfile(t)})
	out, err := OrderNodes(string(empty))
	if err != nil {
		t.Fatalf("OrderNodes(empty) error = %v", err)
	}
	if out != "[]" {
		t.Errorf("empty order = %q, want []", out)
	}
	if _, err := OrderNodes("{bad"); err == nil {
		t.Error("malformed JSON: want error")
	}
}
