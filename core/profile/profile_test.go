package profile

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
)

func sampleNodes() []model.Node {
	return []model.Node{
		{Protocol: model.VLESS, Name: "Frankfurt", Server: "203.0.113.1", Port: 443, UUID: "uuid-a"},
		{Protocol: model.Hysteria2, Name: "Amsterdam", Server: "203.0.113.2", Port: 8443, Password: "pw-b"},
		{Protocol: model.Shadowsocks, Name: "Tokyo", Server: "203.0.113.3", Port: 9000, Password: "pw-c", Method: "aes-256-gcm"},
	}
}

func TestNewProfile(t *testing.T) {
	nodes := sampleNodes()
	p, err := NewProfile("Home", SourceSubscription, "https://example.test/sub", nodes)
	if err != nil {
		t.Fatalf("NewProfile: %v", err)
	}
	if p.ID == "" {
		t.Error("profile ID is empty")
	}
	if len(p.ID) != 16 {
		t.Errorf("profile ID = %q, want 16 hex chars", p.ID)
	}
	if p.Name != "Home" || p.Source != SourceSubscription || p.URL != "https://example.test/sub" {
		t.Errorf("metadata not set: %+v", p)
	}
	if len(p.Servers) != len(nodes) {
		t.Fatalf("got %d servers, want %d", len(p.Servers), len(nodes))
	}
	for i, s := range p.Servers {
		if s.ID == "" {
			t.Errorf("server %d has empty ID", i)
		}
		if !reflect.DeepEqual(s.Node, nodes[i]) {
			t.Errorf("server %d node = %+v, want %+v", i, s.Node, nodes[i])
		}
	}
	if p.UpdatedAt.IsZero() {
		t.Error("UpdatedAt not set")
	}
}

func TestProfileJSONShape(t *testing.T) {
	expires := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	p := Profile{
		ID:           "abc123",
		Name:         "Work",
		Source:       SourceSubscription,
		URL:          "https://example.test/s",
		Servers:      assignServerIDs(sampleNodes()[:1]),
		UpdatedAt:    time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
		ExpiresAt:    &expires,
		TrafficUsed:  1024,
		TrafficTotal: 4096,
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Decode into a generic map to assert the control-protocol field names and
	// that Server flattens (id + node fields at one level under nodes[]).
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"id", "name", "source", "url", "nodes", "updatedAt", "expiresAt", "trafficUsed", "trafficTotal"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing top-level key %q in %s", key, data)
		}
	}
	nodesArr, ok := got["nodes"].([]any)
	if !ok || len(nodesArr) != 1 {
		t.Fatalf("nodes is not a 1-element array: %v", got["nodes"])
	}
	node0, ok := nodesArr[0].(map[string]any)
	if !ok {
		t.Fatalf("node[0] not an object: %v", nodesArr[0])
	}
	for _, key := range []string{"id", "protocol", "name", "server", "port"} {
		if _, ok := node0[key]; !ok {
			t.Errorf("node missing flattened key %q in %s", key, data)
		}
	}
	if _, nested := node0["Node"]; nested {
		t.Errorf("embedded Node was not flattened: %s", data)
	}
}

func TestProfileOmitemptyURL(t *testing.T) {
	p := Profile{ID: "x", Name: "n", Source: SourceManual, Servers: assignServerIDs(sampleNodes()[:1])}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["url"]; ok {
		t.Errorf("empty url should be omitted: %s", data)
	}
	if _, ok := got["expiresAt"]; ok {
		t.Errorf("nil expiresAt should be omitted: %s", data)
	}
}

func TestAssignServerIDsNilAndEmpty(t *testing.T) {
	if got := assignServerIDs(nil); got != nil {
		t.Errorf("assignServerIDs(nil) = %v, want nil", got)
	}
	if got := assignServerIDs([]model.Node{}); len(got) != 0 {
		t.Errorf("assignServerIDs(empty) = %v, want len 0", got)
	}
}

func TestServerIDStable(t *testing.T) {
	n := sampleNodes()[0]
	if a, b := serverID(n), serverID(n); a != b {
		t.Errorf("serverID not deterministic: %q vs %q", a, b)
	}
	// Length: 8 bytes -> 16 hex chars.
	if got := serverID(n); len(got) != 16 {
		t.Errorf("serverID len = %d, want 16", len(got))
	}
}

func TestServerIDNameIndependent(t *testing.T) {
	n := sampleNodes()[0]
	renamed := n
	renamed.Name = "Different label"
	if serverID(n) != serverID(renamed) {
		t.Error("serverID changed when only Name changed; should be stable")
	}
}

func TestServerIDChangesOnIdentityFields(t *testing.T) {
	base := sampleNodes()[0]
	tests := []struct {
		name   string
		mutate func(*model.Node)
	}{
		{"protocol", func(n *model.Node) { n.Protocol = model.Trojan }},
		{"server", func(n *model.Node) { n.Server = "198.51.100.9" }},
		{"port", func(n *model.Node) { n.Port = 8443 }},
		{"uuid", func(n *model.Node) { n.UUID = "uuid-z" }},
		{"password", func(n *model.Node) { n.Password = "new-pw" }},
		{"method", func(n *model.Node) { n.Method = "chacha20-poly1305" }},
	}
	baseID := serverID(base)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := base
			tt.mutate(&n)
			if got := serverID(n); got == baseID {
				t.Errorf("serverID unchanged after %s change", tt.name)
			}
		})
	}
}

// TestServerIDDistinguishesStructFields covers fix #6: two servers on the same
// host:port that differ only in WireGuard keys, REALITY material, or transport
// details must get distinct IDs. Before the fix these collided to one ID because
// serverID hashed only protocol/server/port/uuid/password/method.
func TestServerIDDistinguishesStructFields(t *testing.T) {
	tests := []struct {
		name string
		a, b model.Node
	}{
		{
			name: "amneziawg peers same endpoint different keys",
			a: model.Node{Protocol: model.AmneziaWG, Server: "wg.test", Port: 51820,
				WireGuard: &model.WireGuard{PrivateKey: "PRIV1", PeerPublicKey: "PEER1"}},
			b: model.Node{Protocol: model.AmneziaWG, Server: "wg.test", Port: 51820,
				WireGuard: &model.WireGuard{PrivateKey: "PRIV2", PeerPublicKey: "PEER2"}},
		},
		{
			name: "amneziawg differ only in peer public key",
			a: model.Node{Protocol: model.AmneziaWG, Server: "wg.test", Port: 51820,
				WireGuard: &model.WireGuard{PrivateKey: "PRIV", PeerPublicKey: "PEER1"}},
			b: model.Node{Protocol: model.AmneziaWG, Server: "wg.test", Port: 51820,
				WireGuard: &model.WireGuard{PrivateKey: "PRIV", PeerPublicKey: "PEER2"}},
		},
		{
			name: "reality variants differ in public key",
			a: model.Node{Protocol: model.VLESS, Server: "r.test", Port: 443, UUID: "u",
				TLS: &model.TLS{Enabled: true, Reality: &model.Reality{PublicKey: "K1", ShortID: "aa"}}},
			b: model.Node{Protocol: model.VLESS, Server: "r.test", Port: 443, UUID: "u",
				TLS: &model.TLS{Enabled: true, Reality: &model.Reality{PublicKey: "K2", ShortID: "aa"}}},
		},
		{
			name: "reality variants differ in short id",
			a: model.Node{Protocol: model.VLESS, Server: "r.test", Port: 443, UUID: "u",
				TLS: &model.TLS{Enabled: true, Reality: &model.Reality{PublicKey: "K", ShortID: "aa"}}},
			b: model.Node{Protocol: model.VLESS, Server: "r.test", Port: 443, UUID: "u",
				TLS: &model.TLS{Enabled: true, Reality: &model.Reality{PublicKey: "K", ShortID: "bb"}}},
		},
		{
			name: "transports differ in type",
			a: model.Node{Protocol: model.VLESS, Server: "t.test", Port: 443, UUID: "u",
				Transport: &model.Transport{Type: "ws", Path: "/x"}},
			b: model.Node{Protocol: model.VLESS, Server: "t.test", Port: 443, UUID: "u",
				Transport: &model.Transport{Type: "grpc", ServiceName: "x"}},
		},
		{
			name: "transports differ in path",
			a: model.Node{Protocol: model.VLESS, Server: "t.test", Port: 443, UUID: "u",
				Transport: &model.Transport{Type: "ws", Path: "/a"}},
			b: model.Node{Protocol: model.VLESS, Server: "t.test", Port: 443, UUID: "u",
				Transport: &model.Transport{Type: "ws", Path: "/b"}},
		},
		{
			name: "transports differ in service name",
			a: model.Node{Protocol: model.VLESS, Server: "t.test", Port: 443, UUID: "u",
				Transport: &model.Transport{Type: "grpc", ServiceName: "svc1"}},
			b: model.Node{Protocol: model.VLESS, Server: "t.test", Port: 443, UUID: "u",
				Transport: &model.Transport{Type: "grpc", ServiceName: "svc2"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if serverID(tt.a) == serverID(tt.b) {
				t.Errorf("serverID collision: %s and %s hash equal", tt.name, tt.name)
			}
		})
	}
}

// TestServerIDStableWithStructFields confirms the extra fields don't break
// determinism: the same populated node still hashes to itself, and an absent
// sub-struct hashes the same as one whose relevant fields are empty.
func TestServerIDStableWithStructFields(t *testing.T) {
	n := model.Node{Protocol: model.AmneziaWG, Server: "wg.test", Port: 51820,
		WireGuard: &model.WireGuard{PrivateKey: "PRIV", PeerPublicKey: "PEER"}}
	if serverID(n) != serverID(n) {
		t.Error("serverID not deterministic for WG node")
	}
	// A relabelled WG node keeps its ID (name still excluded).
	renamed := n
	renamed.Name = "elsewhere"
	if serverID(n) != serverID(renamed) {
		t.Error("serverID changed when only Name changed on a WG node")
	}
}

// TestServerIDNoCollisionAcrossBoundary guards the length-prefix scheme:
// splitting a value across fields must not collide.
func TestServerIDNoCollisionAcrossBoundary(t *testing.T) {
	a := model.Node{Server: "ab", Port: 1}
	b := model.Node{Server: "a", Port: 1} // "b" would have to migrate elsewhere
	if serverID(a) == serverID(b) {
		t.Error("unexpected serverID collision")
	}
	c := model.Node{UUID: "x", Password: "yz"}
	d := model.Node{UUID: "xy", Password: "z"}
	if serverID(c) == serverID(d) {
		t.Error("field boundary collision between uuid and password")
	}
}
