package profile

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIdentityPublicIDNotCredentialVerifier(t *testing.T) {
	a, err := NewProfile("a", SourceManual, "", sampleNodes()[:1])
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewProfile("b", SourceManual, "", sampleNodes()[:1])
	if err != nil {
		t.Fatal(err)
	}
	id := a.Servers[0].ID
	if len(id) != 35 || !strings.HasPrefix(id, "n2_") || id == b.Servers[0].ID {
		t.Fatalf("fresh imports expose a deterministic credential-derived identity: opaque=%v independent=%v", strings.HasPrefix(id, "n2_"), id != b.Servers[0].ID)
	}
}

func TestIdentityFlatJSONRetainsUnknownNestedFields(t *testing.T) {
	raw := []byte(`{"id":"profile-kept","name":"legacy","source":"manual","updatedAt":"2026-11-07T00:00:00Z","custom_profile":{"future":[1,"x"]},"nodes":[{"id":"node-kept","protocol":"trojan","name":"display","server":"example.test","port":443,"password":"SYNTHETIC_ONLY","insecure":true,"tls":{"enabled":true,"insecure":true,"server_name":"sni.test","future_tls":{"z":2,"a":[false,"x"]},"reality":{"public_key":"TEST_KEY","future_reality":[1,2]}},"transport":{"type":"ws","future_transport":{"x":3}},"obfs":{"type":"salamander","future_obfs":true},"wireguard":{"private_key":"INERT","peer_public_key":"INERT","future_wg":"kept"},"custom_node":{"nested":[1,2,3]}}]}`)
	var p Profile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var expected, actual map[string]any
	json.Unmarshal(raw, &expected)
	json.Unmarshal(data, &actual)
	if !reflect.DeepEqual(expected, actual) {
		t.Fatal("flat profile/node unknown fields or wrapper IDs were lost on JSON roundtrip")
	}
	node := actual["nodes"].([]any)[0].(map[string]any)
	if _, ok := node["Node"]; ok {
		t.Fatal("embedded node became nested")
	}
	if _, ok := node["AdditionalFields"]; ok {
		t.Fatal("unknown JSON was wrapped in a map field")
	}
}
