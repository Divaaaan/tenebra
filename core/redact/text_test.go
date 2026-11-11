package redact

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
)

func TestSharedRedactionVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Input    string `json:"input"`
		Expected string `json:"expected"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for index, test := range cases {
		if Text(test.Input) != test.Expected {
			t.Fatalf("shared redaction vector %d failed", index)
		}
		if got := Text(Text(test.Input)); got != test.Expected {
			t.Fatalf("boundary redaction is not idempotent for vector %d: %q", index, got)
		}
	}
}

func TestRepeatedShareRedactionPreservesOnlyEndpointContext(t *testing.T) {
	for _, input := range []string{"vless://private@edge.example:443?secret=canary", "ss://c3ludGhldGljX2NyZWRlbnRpYWw=", "vmess://c3ludGhldGljX2NyZWRlbnRpYWw=", "ssr://c3ludGhldGljX2NyZWRlbnRpYWw=", "trojan://private@[2001:db8::1]:443#canary"} {
		first := Text(input)
		if next := Text(first); first != next {
			t.Errorf("redacting an already sanitized share changed it: %q to %q", first, next)
		}
		if strings.Contains(first, "private") || strings.Contains(first, "canary") || strings.Contains(first, "c3ludGhldGlj") {
			t.Error("opaque share credential escaped sanitization")
		}
	}
}

func TestGenericURLAliasSanitizer(t *testing.T) {
	raw := "https://u:PASS_CANARY@provider.invalid/sub/PATH_CANARY?token=QUERY_CANARY#FRAGMENT_CANARY"
	for _, name := range []string{"", raw, "QR " + raw} {
		if got := ProfileName(name, raw); got != "provider.invalid" {
			t.Fatalf("unsafe or unhelpful alias: %q", got)
		}
	}
	if got := ProfileName("Домашний VPN", raw); got != "Домашний VPN" {
		t.Fatal("ordinary label changed")
	}
	for _, value := range []string{raw, "error: " + raw, "vmess://SYNTHETIC_BASE64_CANARY", "trojan://PASS_CANARY@host.test:443?x=QUERY_CANARY"} {
		got := Text(value)
		for _, secret := range []string{"PASS_CANARY", "PATH_CANARY", "QUERY_CANARY", "FRAGMENT_CANARY", "SYNTHETIC_BASE64_CANARY"} {
			if strings.Contains(got, secret) {
				t.Fatal("generic sanitizer exposed a URL secret")
			}
		}
	}
}

func TestNodeNameMasksItsOwnTypedCredentials(t *testing.T) {
	node := model.Node{Name: "Home PASSWORD_CANARY UUID_CANARY KEY_CANARY PSK_CANARY OBFUSCATION_CANARY", Password: "PASSWORD_CANARY", UUID: "UUID_CANARY", Obfs: &model.Obfs{Password: "OBFUSCATION_CANARY"}, WireGuard: &model.WireGuard{PrivateKey: "KEY_CANARY", PreSharedKey: "PSK_CANARY"}}
	got := NodeName(node)
	if strings.Contains(got, "CANARY") || !strings.HasPrefix(got, "Home ") {
		t.Fatal("typed credential in display label escaped sanitizer")
	}
	node.Name = "Frankfurt 🇩🇪"
	if got := NodeName(node); got != node.Name {
		t.Fatal("ordinary node label changed")
	}
}
