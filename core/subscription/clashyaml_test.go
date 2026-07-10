package subscription

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitYAMLKey(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		key, val string
		ok       bool
	}{
		{"simple", "name: value", "name", "value", true},
		{"integer value", "port: 443", "port", "443", true},
		{"colon inside value", "path: /a:b/c", "path", "/a:b/c", true},
		{"url value", "url: https://example.com", "url", "https://example.com", true},
		{"empty value", "network:", "network", "", true},
		{"bare scalar", "just-a-scalar", "", "", false},
		{"rule scalar", "DOMAIN,example.com,PROXY", "", "", false},
		{"url scalar no space", "https://host:443", "", "", false},
		{"quoted key with space", "'quoted key': val", "quoted key", "val", true},
		{"quoted value with colon", `server: "a: b"`, "server", `"a: b"`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, val, ok := splitYAMLKey(tc.in)
			if ok != tc.ok || key != tc.key || val != tc.val {
				t.Errorf("splitYAMLKey(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.in, key, val, ok, tc.key, tc.val, tc.ok)
			}
		})
	}
}

func TestStripYAMLComment(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"trailing comment", "name: value # a comment", "name: value"},
		{"hash in value no space", "password: ab#cd", "password: ab#cd"},
		{"whole-line comment", "# just a comment", ""},
		{"hash in single quotes", "path: '/a#b'", "path: '/a#b'"},
		{"hash in double quotes", `host: "a #b"`, `host: "a #b"`},
		{"no comment", "plain: value", "plain: value"},
		{"indented comment", "   # note", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.TrimRight(stripYAMLComment(tc.in), " \t")
			if got != tc.want {
				t.Errorf("stripYAMLComment(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSplitTopLevel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", "a,b,c", []string{"a", "b", "c"}},
		{"nested braces", "a,{x,y},b", []string{"a", "{x,y}", "b"}},
		{"nested brackets", "a,[x,y],b", []string{"a", "[x,y]", "b"}},
		{"single quotes", "'a,b',c", []string{"'a,b'", "c"}},
		{"double quotes", `"a,b",c`, []string{`"a,b"`, "c"}},
		{"empty", "", []string{""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitTopLevel(tc.in, ',')
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitTopLevel(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseYAMLScalar(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "plain", "plain"},
		{"single quoted", "'single'", "single"},
		{"double quoted", `"double"`, "double"},
		{"escaped newline", `"a\nb"`, "a\nb"},
		{"escaped slash", `"a\/b"`, "a/b"},
		{"empty single", "''", ""},
		{"empty double", `""`, ""},
		{"apostrophe in plain", "can't", "can't"},
		{"single quote escape", "'it''s'", "it's"},
		{"integer stays string", "123", "123"},
		{"bool stays string", "true", "true"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseYAMLScalar(tc.in); got != tc.want {
				t.Errorf("parseYAMLScalar(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestDecodeYAMLBlockNested checks the block forms the mapper depends on: a
// sequence of mappings, a nested option map (ws-opts), a doubly-nested map
// (headers.Host) and a block sequence value (alpn).
func TestDecodeYAMLBlockNested(t *testing.T) {
	in := `proxies:
  - name: n
    type: vmess
    port: 443
    ws-opts:
      path: /p
      headers:
        Host: h.com
    alpn:
      - h2
      - http/1.1
`
	want := map[string]any{
		"proxies": []any{
			map[string]any{
				"name": "n",
				"type": "vmess",
				"port": "443",
				"ws-opts": map[string]any{
					"path":    "/p",
					"headers": map[string]any{"Host": "h.com"},
				},
				"alpn": []any{"h2", "http/1.1"},
			},
		},
	}
	got := decodeYAML([]byte(in))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decodeYAML() =\n%#v\nwant\n%#v", got, want)
	}
}

// TestDecodeYAMLFlow checks flow mappings and flow sequences, including a nested
// flow map and a flow alpn list, plus a quoted scalar carrying a colon.
func TestDecodeYAMLFlow(t *testing.T) {
	in := `proxies:
  - { name: a, type: vless, port: 443, reality-opts: { public-key: PK, short-id: ab }, alpn: [h2, http/1.1], password: "p:w" }
`
	want := map[string]any{
		"proxies": []any{
			map[string]any{
				"name":         "a",
				"type":         "vless",
				"port":         "443",
				"reality-opts": map[string]any{"public-key": "PK", "short-id": "ab"},
				"alpn":         []any{"h2", "http/1.1"},
				"password":     "p:w",
			},
		},
	}
	got := decodeYAML([]byte(in))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decodeYAML() =\n%#v\nwant\n%#v", got, want)
	}
}

// TestDecodeYAMLInlineProxiesSequence checks the proxies value given as an inline
// flow sequence on the key line.
func TestDecodeYAMLInlineProxiesSequence(t *testing.T) {
	in := "proxies: [{ name: a, type: ss }, { name: b, type: ss }]\n"
	got := decodeYAML([]byte(in))
	seq, ok := got["proxies"].([]any)
	if !ok || len(seq) != 2 {
		t.Fatalf("proxies = %#v, want a 2-element sequence", got["proxies"])
	}
	if m, ok := seq[1].(map[string]any); !ok || m["name"] != "b" {
		t.Errorf("second proxy = %#v, want name b", seq[1])
	}
}

// TestDecodeYAMLIgnoresOtherSections proves the parser resynchronizes at each
// top-level key: rules (a same-indent block sequence), a nested dns mapping and a
// proxy-groups list with its own nested proxies must not disturb the top-level
// proxies extraction.
func TestDecodeYAMLIgnoresOtherSections(t *testing.T) {
	in := `mixed-port: 7890
dns:
  enable: true
  nameserver:
    - 1.1.1.1
    - 8.8.8.8
proxies:
  - name: real
    type: ss
    server: s.example.com
    port: 8388
    cipher: aes-256-gcm
    password: pw
proxy-groups:
  - name: G
    type: select
    proxies:
      - real
      - DIRECT
rules:
- DOMAIN-SUFFIX,example.com,G
- MATCH,DIRECT
`
	proxies, present := clashProxyMaps([]byte(in))
	if !present {
		t.Fatal("clashProxyMaps() present = false, want true")
	}
	if len(proxies) != 1 {
		t.Fatalf("len(proxies) = %d, want 1", len(proxies))
	}
	if proxies[0]["name"] != "real" || proxies[0]["cipher"] != "aes-256-gcm" {
		t.Errorf("proxy = %#v, want the top-level ss node", proxies[0])
	}
}

// TestClashProxyMapsNonMapEntry keeps a malformed (non-mapping) proxies entry as
// a nil placeholder so the caller still counts it.
func TestClashProxyMapsNonMapEntry(t *testing.T) {
	in := "proxies:\n  - just-a-scalar\n  - name: ok\n    type: ss\n"
	proxies, present := clashProxyMaps([]byte(in))
	if !present || len(proxies) != 2 {
		t.Fatalf("got present=%v len=%d, want true/2", present, len(proxies))
	}
	if proxies[0] != nil {
		t.Errorf("first entry = %#v, want nil placeholder", proxies[0])
	}
	if proxies[1]["name"] != "ok" {
		t.Errorf("second entry = %#v, want name ok", proxies[1])
	}
}

func TestClashProxyMapsNoProxiesKey(t *testing.T) {
	if _, present := clashProxyMaps([]byte("rules:\n- MATCH,DIRECT\n")); present {
		t.Error("present = true, want false when there is no proxies key")
	}
}
