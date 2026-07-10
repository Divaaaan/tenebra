package subscription

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
)

// oneProxy decodes a single-proxy Clash config and maps it, returning the node
// and whether it was mappable. It exercises the whole path (decode + map) the
// way ParseSubscription does.
func oneProxy(t *testing.T, yaml string) (model.Node, bool) {
	t.Helper()
	proxies, present := clashProxyMaps([]byte(yaml))
	if !present {
		t.Fatalf("clashProxyMaps() present = false for:\n%s", yaml)
	}
	if len(proxies) != 1 {
		t.Fatalf("len(proxies) = %d, want 1", len(proxies))
	}
	return clashProxyToNode(proxies[0])
}

// TestClashProxyToNodePerProtocol asserts the exact Node produced for each
// supported Clash proxy type, checking the field-name translations.
func TestClashProxyToNodePerProtocol(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want model.Node
	}{
		{
			name: "shadowsocks with extra udp",
			yaml: `proxies:
  - name: ss-node
    type: ss
    server: ss.example.com
    port: 8388
    cipher: aes-256-gcm
    password: sspass
    udp: true
`,
			want: model.Node{
				Protocol: model.Shadowsocks,
				Name:     "ss-node",
				Server:   "ss.example.com",
				Port:     8388,
				Method:   "aes-256-gcm",
				Password: "sspass",
				Extra:    map[string]string{"udp": "true"},
			},
		},
		{
			name: "vmess ws over tls with block alpn",
			yaml: `proxies:
  - name: vmess-node
    type: vmess
    server: v.example.com
    port: 443
    uuid: vmess-uuid
    alterId: 0
    cipher: auto
    network: ws
    tls: true
    servername: v.example.com
    skip-cert-verify: true
    ws-opts:
      path: /vm
      headers:
        Host: cdn.example.com
    alpn:
      - h2
      - http/1.1
`,
			want: model.Node{
				Protocol:  model.VMess,
				Name:      "vmess-node",
				Server:    "v.example.com",
				Port:      443,
				UUID:      "vmess-uuid",
				Security:  "auto",
				Transport: &model.Transport{Type: "ws", Path: "/vm", Host: "cdn.example.com"},
				TLS: &model.TLS{
					Enabled:    true,
					ServerName: "v.example.com",
					Insecure:   true,
					ALPN:       []string{"h2", "http/1.1"},
				},
			},
		},
		{
			name: "vless reality with vision flow",
			yaml: `proxies:
  - name: vless-node
    type: vless
    server: r.example.com
    port: 443
    uuid: vless-uuid
    flow: xtls-rprx-vision
    network: tcp
    tls: true
    servername: www.microsoft.com
    client-fingerprint: chrome
    reality-opts:
      public-key: PUBKEY
      short-id: sid123
`,
			want: model.Node{
				Protocol: model.VLESS,
				Name:     "vless-node",
				Server:   "r.example.com",
				Port:     443,
				UUID:     "vless-uuid",
				Flow:     "xtls-rprx-vision",
				TLS: &model.TLS{
					Enabled:     true,
					ServerName:  "www.microsoft.com",
					Fingerprint: "chrome",
					Reality:     &model.Reality{PublicKey: "PUBKEY", ShortID: "sid123"},
				},
			},
		},
		{
			name: "trojan ws forced tls",
			yaml: `proxies:
  - name: trojan-node
    type: trojan
    server: t.example.com
    port: 443
    password: trojanpass
    sni: t.example.com
    skip-cert-verify: false
    network: ws
    ws-opts:
      path: /tj
      headers:
        Host: t.example.com
`,
			want: model.Node{
				Protocol:  model.Trojan,
				Name:      "trojan-node",
				Server:    "t.example.com",
				Port:      443,
				Password:  "trojanpass",
				Transport: &model.Transport{Type: "ws", Path: "/tj", Host: "t.example.com"},
				TLS:       &model.TLS{Enabled: true, ServerName: "t.example.com"},
			},
		},
		{
			name: "hysteria2 with salamander obfs",
			yaml: `proxies:
  - name: hy2-node
    type: hysteria2
    server: h.example.com
    port: 8443
    password: hypass
    obfs: salamander
    obfs-password: obfspw
    sni: h.example.com
    skip-cert-verify: true
    alpn:
      - h3
`,
			want: model.Node{
				Protocol: model.Hysteria2,
				Name:     "hy2-node",
				Server:   "h.example.com",
				Port:     8443,
				Password: "hypass",
				TLS:      &model.TLS{Enabled: true, ServerName: "h.example.com", Insecure: true, ALPN: []string{"h3"}},
				Obfs:     &model.Obfs{Type: "salamander", Password: "obfspw"},
			},
		},
		{
			name: "wireguard with amnezia knobs",
			yaml: `proxies:
  - name: wg-node
    type: wireguard
    server: w.example.com
    port: 51820
    private-key: PRIVKEY
    public-key: PEERKEY
    pre-shared-key: PSK
    ip: 10.0.0.2
    ipv6: fd00::2
    jc: 4
    jmin: 40
    jmax: 70
    s1: 15
    s2: 20
    h1: 1234567890
    h2: 2345678901
    h3: 3456789012
    h4: 987654321
`,
			want: model.Node{
				Protocol: model.AmneziaWG,
				Name:     "wg-node",
				Server:   "w.example.com",
				Port:     51820,
				WireGuard: &model.WireGuard{
					PrivateKey:    "PRIVKEY",
					PeerPublicKey: "PEERKEY",
					PreSharedKey:  "PSK",
					LocalAddress:  []string{"10.0.0.2/32", "fd00::2/128"},
					Jc:            4,
					Jmin:          40,
					Jmax:          70,
					S1:            15,
					S2:            20,
					H1:            1234567890,
					H2:            2345678901,
					H3:            3456789012,
					H4:            987654321,
				},
			},
		},
		{
			name: "vmess grpc",
			yaml: `proxies:
  - name: vmess-grpc
    type: vmess
    server: g.example.com
    port: 443
    uuid: g-uuid
    cipher: auto
    network: grpc
    tls: true
    grpc-opts:
      grpc-service-name: mygrpc
`,
			want: model.Node{
				Protocol:  model.VMess,
				Name:      "vmess-grpc",
				Server:    "g.example.com",
				Port:      443,
				UUID:      "g-uuid",
				Security:  "auto",
				Transport: &model.Transport{Type: "grpc", ServiceName: "mygrpc"},
				TLS:       &model.TLS{Enabled: true},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := oneProxy(t, tc.yaml)
			if !ok {
				t.Fatalf("clashProxyToNode() ok = false, want a mapped node")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("node =\n%#v\nwant\n%#v", got, tc.want)
			}
		})
	}
}

// TestClashFlowStyleProxies maps flow-style ({ … }) proxy entries, including a
// quoted password carrying '@' and ':' and a nested flow reality-opts.
func TestClashFlowStyleProxies(t *testing.T) {
	yaml := `proxies:
  - { name: flow-ss, type: ss, server: f.example.com, port: 8388, cipher: chacha20-ietf-poly1305, password: "p@ss:word" }
  - { name: flow-vless, type: vless, server: fv.example.com, port: 443, uuid: u-1, network: tcp, tls: true, servername: sni.example.com, reality-opts: { public-key: PK, short-id: ab } }
`
	nodes, skipped, err := ParseSubscription([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	if skipped != 0 || len(nodes) != 2 {
		t.Fatalf("got %d nodes, %d skipped; want 2/0", len(nodes), skipped)
	}
	wantSS := model.Node{
		Protocol: model.Shadowsocks,
		Name:     "flow-ss",
		Server:   "f.example.com",
		Port:     8388,
		Method:   "chacha20-ietf-poly1305",
		Password: "p@ss:word",
	}
	if !reflect.DeepEqual(nodes[0], wantSS) {
		t.Errorf("ss node =\n%#v\nwant\n%#v", nodes[0], wantSS)
	}
	wantVLESS := model.Node{
		Protocol: model.VLESS,
		Name:     "flow-vless",
		Server:   "fv.example.com",
		Port:     443,
		UUID:     "u-1",
		TLS: &model.TLS{
			Enabled:    true,
			ServerName: "sni.example.com",
			Reality:    &model.Reality{PublicKey: "PK", ShortID: "ab"},
		},
	}
	if !reflect.DeepEqual(nodes[1], wantVLESS) {
		t.Errorf("vless node =\n%#v\nwant\n%#v", nodes[1], wantVLESS)
	}
}

// TestClashUnmappableSkipped covers types Tenebra does not model and proxies
// missing required fields: each yields no node and is counted as skipped.
func TestClashUnmappableSkipped(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"tuic", "proxies:\n  - {name: n, type: tuic, server: s, port: 443, uuid: u, password: p}\n"},
		{"snell", "proxies:\n  - {name: n, type: snell, server: s, port: 443, psk: p}\n"},
		{"ssr", "proxies:\n  - {name: n, type: ssr, server: s, port: 443, cipher: aes-256-cfb, password: p}\n"},
		{"http", "proxies:\n  - {name: n, type: http, server: s, port: 8080}\n"},
		{"ss with unsupported plugin", "proxies:\n  - {name: n, type: ss, server: s, port: 8388, cipher: aes-256-gcm, password: p, plugin: obfs}\n"},
		{"ss missing cipher", "proxies:\n  - {name: n, type: ss, server: s, port: 8388, password: p}\n"},
		{"vless missing uuid", "proxies:\n  - {name: n, type: vless, server: s, port: 443}\n"},
		{"vmess missing uuid", "proxies:\n  - {name: n, type: vmess, server: s, port: 443}\n"},
		{"trojan missing password", "proxies:\n  - {name: n, type: trojan, server: s, port: 443}\n"},
		{"hysteria2 missing password", "proxies:\n  - {name: n, type: hysteria2, server: s, port: 443}\n"},
		{"wireguard missing keys", "proxies:\n  - {name: n, type: wireguard, server: s, port: 51820}\n"},
		{"missing server", "proxies:\n  - {name: n, type: ss, port: 8388, cipher: aes-256-gcm, password: p}\n"},
		{"invalid port", "proxies:\n  - {name: n, type: ss, server: s, port: 70000, cipher: aes-256-gcm, password: p}\n"},
		{"missing type", "proxies:\n  - {name: n, server: s, port: 8388}\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nodes, skipped, err := ParseSubscription([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("ParseSubscription() error = %v", err)
			}
			if len(nodes) != 0 || skipped != 1 {
				t.Errorf("got %d nodes, %d skipped; want 0/1", len(nodes), skipped)
			}
		})
	}
}

// TestParseSubscriptionClashMixed runs a realistic multi-proxy block with a mix
// of supported types and unmappable ones, asserting both the node set and the
// skipped count.
func TestParseSubscriptionClashMixed(t *testing.T) {
	yaml := `# provider config
port: 7890
proxies:
  - name: A
    type: ss
    server: a.example.com
    port: 8388
    cipher: aes-256-gcm
    password: pw
  - name: B
    type: vmess
    server: b.example.com
    port: 443
    uuid: b-uuid
    cipher: auto
  - name: C
    type: vless
    server: c.example.com
    port: 443
    uuid: c-uuid
    tls: true
    reality-opts:
      public-key: PK
  - name: D
    type: trojan
    server: d.example.com
    port: 443
    password: dpw
  - name: E
    type: hy2
    server: e.example.com
    port: 8443
    password: epw
  - name: skip-tuic
    type: tuic
    server: x.example.com
    port: 443
    uuid: u
  - name: skip-plugin
    type: ss
    server: y.example.com
    port: 8388
    cipher: aes-256-gcm
    password: pw
    plugin: v2ray-plugin
rules:
  - MATCH,DIRECT
`
	nodes, skipped, err := ParseSubscription([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	if len(nodes) != 5 {
		t.Fatalf("len(nodes) = %d, want 5", len(nodes))
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	gotNames := make([]string, len(nodes))
	gotProto := map[string]model.Protocol{}
	for i, n := range nodes {
		gotNames[i] = n.Name
		gotProto[n.Name] = n.Protocol
	}
	wantNames := []string{"A", "B", "C", "D", "E"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Errorf("names = %v, want %v", gotNames, wantNames)
	}
	if gotProto["E"] != model.Hysteria2 {
		t.Errorf("proxy E protocol = %q, want hysteria2 (hy2 alias)", gotProto["E"])
	}
	if gotProto["C"] != model.VLESS {
		t.Errorf("proxy C protocol = %q, want vless", gotProto["C"])
	}
}

// TestClashQuotedAndUnicodeScalars checks quoted names, an emoji name and a
// quoted numeric port.
func TestClashQuotedAndUnicodeScalars(t *testing.T) {
	yaml := "proxies:\n" +
		"  - name: \"\U0001F1FA\U0001F1F8 US #1\"\n" +
		"    type: ss\n" +
		"    server: u.example.com\n" +
		"    port: \"8388\"\n" +
		"    cipher: aes-256-gcm\n" +
		"    password: 'p a s s'\n"
	node, ok := oneProxy(t, yaml)
	if !ok {
		t.Fatal("clashProxyToNode() ok = false")
	}
	if node.Name != "\U0001F1FA\U0001F1F8 US #1" {
		t.Errorf("name = %q, want the emoji name (with # preserved inside quotes)", node.Name)
	}
	if node.Port != 8388 {
		t.Errorf("port = %d, want 8388 (from a quoted string)", node.Port)
	}
	if node.Password != "p a s s" {
		t.Errorf("password = %q, want 'p a s s'", node.Password)
	}
}

// TestClashHeadersHostNesting checks the doubly-nested ws-opts.headers.Host path
// reaches Transport.Host, in both block and flow forms.
func TestClashHeadersHostNesting(t *testing.T) {
	block := `proxies:
  - name: n
    type: vmess
    server: s
    port: 443
    uuid: u
    network: ws
    ws-opts:
      path: /ws
      headers:
        Host: block.example.com
`
	flow := "proxies:\n  - { name: n, type: vmess, server: s, port: 443, uuid: u, network: ws, ws-opts: { path: /ws, headers: { Host: flow.example.com } } }\n"
	for _, tc := range []struct{ name, yaml, host string }{
		{"block", block, "block.example.com"},
		{"flow", flow, "flow.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, ok := oneProxy(t, tc.yaml)
			if !ok {
				t.Fatal("clashProxyToNode() ok = false")
			}
			if node.Transport == nil || node.Transport.Host != tc.host {
				t.Errorf("Transport = %#v, want Host %q", node.Transport, tc.host)
			}
		})
	}
}

// TestClashAlpnFlowVsBlock checks the alpn list is read identically whether it is
// written as a block sequence or a flow sequence.
func TestClashAlpnFlowVsBlock(t *testing.T) {
	block := `proxies:
  - name: n
    type: trojan
    server: s
    port: 443
    password: p
    alpn:
      - h2
      - http/1.1
`
	flow := "proxies:\n  - { name: n, type: trojan, server: s, port: 443, password: p, alpn: [h2, http/1.1] }\n"
	want := []string{"h2", "http/1.1"}
	for _, tc := range []struct{ name, yaml string }{{"block", block}, {"flow", flow}} {
		t.Run(tc.name, func(t *testing.T) {
			node, ok := oneProxy(t, tc.yaml)
			if !ok {
				t.Fatal("clashProxyToNode() ok = false")
			}
			if node.TLS == nil || !reflect.DeepEqual(node.TLS.ALPN, want) {
				t.Errorf("alpn = %#v, want %v", node.TLS, want)
			}
		})
	}
}

func TestLooksLikeClashConfig(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"top-level proxies", "proxies:\n  - name: a\n", true},
		{"proxies with empty flow list", "proxies: []\n", true},
		{"proxies after other keys", "port: 7890\nproxies:\n  - name: a\n", true},
		{"indented proxies only", "proxy-providers:\n  main:\n    proxies:\n      - a\n", false},
		{"commented proxies", "# proxies: fake\nvless://abc@e.com:443#a\n", false},
		{"proxy-providers not proxies", "proxy-providers:\n  x: {}\n", false},
		{"plaintext link list", "vless://abc@e.com:443#a\nhysteria2://p@e.com:443#b\n", false},
		{"base64 blob", base64.StdEncoding.EncodeToString([]byte("vless://abc@e.com:443#a")), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeClashConfig(strings.TrimSpace(tc.in)); got != tc.want {
				t.Errorf("looksLikeClashConfig(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseSubscriptionNonClashFallthrough ensures a YAML body without a
// top-level proxies key is not treated as Clash and falls through to the existing
// link path (here yielding nothing, but crucially not panicking or erroring).
func TestParseSubscriptionNonClashFallthrough(t *testing.T) {
	yaml := "proxy-providers:\n  main:\n    type: http\n    url: https://example.com/x\nrules:\n  - MATCH,DIRECT\n"
	nodes, skipped, err := ParseSubscription([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("len(nodes) = %d, want 0 (non-Clash YAML has no share links)", len(nodes))
	}
	_ = skipped
}

// TestParseSubscriptionClashWithBOM confirms a config prefixed with a UTF-8 BOM
// (which some servers emit and TrimSpace does not remove) is still detected and
// decoded.
func TestParseSubscriptionClashWithBOM(t *testing.T) {
	body := append([]byte{0xEF, 0xBB, 0xBF},
		[]byte("proxies:\n  - {name: n, type: ss, server: s.example.com, port: 8388, cipher: aes-256-gcm, password: pw}\n")...)
	nodes, skipped, err := ParseSubscription(body)
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	if len(nodes) != 1 || skipped != 0 {
		t.Fatalf("got %d nodes, %d skipped; want 1/0", len(nodes), skipped)
	}
	if nodes[0].Name != "n" || nodes[0].Protocol != model.Shadowsocks {
		t.Errorf("node = %#v, want ss node named n", nodes[0])
	}
}

// TestParseSubscriptionBase64Unaffected confirms Clash detection does not
// misfire on a base64 link list, which still parses exactly as before.
func TestParseSubscriptionBase64Unaffected(t *testing.T) {
	links := strings.Join([]string{
		"vless://abc@example.com:443?security=tls#a",
		"trojan://pw@example.com:443#b",
	}, "\n")
	body := []byte(base64.StdEncoding.EncodeToString([]byte(links)))
	nodes, skipped, err := ParseSubscription(body)
	if err != nil {
		t.Fatalf("ParseSubscription() error = %v", err)
	}
	if len(nodes) != 2 || skipped != 0 {
		t.Fatalf("got %d nodes, %d skipped; want 2/0", len(nodes), skipped)
	}
	if nodes[0].Name != "a" || nodes[1].Name != "b" {
		t.Errorf("names = %q, %q; want a, b", nodes[0].Name, nodes[1].Name)
	}
}
