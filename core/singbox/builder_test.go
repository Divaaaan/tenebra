package singbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/routing"
)

// fakeNodes returns one node of every protocol with obviously fake data.
func fakeNodes() []model.Node {
	return []model.Node{
		{
			Protocol: model.VLESS,
			Name:     "vless-reality",
			Server:   "example.test",
			Port:     443,
			UUID:     "11111111-1111-1111-1111-111111111111",
			Flow:     "xtls-rprx-vision",
			TLS: &model.TLS{
				Enabled:     true,
				ServerName:  "example.test",
				Fingerprint: "chrome",
				Reality:     &model.Reality{PublicKey: "PUBKEYFAKE", ShortID: "ab12"},
			},
		},
		{
			Protocol: model.VLESS,
			Name:     "vless-ws",
			Server:   "ws.example.test",
			Port:     443,
			UUID:     "22222222-2222-2222-2222-222222222222",
			TLS:      &model.TLS{Enabled: true, ServerName: "ws.example.test"},
			Transport: &model.Transport{
				Type: "ws",
				Path: "/path",
				Host: "ws.example.test",
			},
		},
		{
			Protocol: model.Hysteria2,
			Name:     "hy2",
			Server:   "hy.example.test",
			Port:     8443,
			Password: "hy2pass",
			Obfs:     &model.Obfs{Type: "salamander", Password: "obfspass"},
			TLS:      &model.TLS{Enabled: true, ServerName: "hy.example.test", Insecure: true, ALPN: []string{"h3"}},
		},
		{
			Protocol: model.Shadowsocks,
			Name:     "ss",
			Server:   "ss.example.test",
			Port:     8388,
			Method:   "aes-256-gcm",
			Password: "sspass",
		},
		{
			Protocol: model.Trojan,
			Name:     "trojan",
			Server:   "tr.example.test",
			Port:     443,
			Password: "trpass",
			TLS:      &model.TLS{Enabled: true, ServerName: "tr.example.test"},
			Transport: &model.Transport{
				Type:        "grpc",
				ServiceName: "grpcsvc",
			},
		},
		{
			Protocol: model.VMess,
			Name:     "vmess",
			Server:   "vm.example.test",
			Port:     443,
			UUID:     "33333333-3333-3333-3333-333333333333",
			Security: "auto",
			AlterID:  0,
		},
		{
			Protocol: model.AmneziaWG,
			Name:     "awg",
			Server:   "wg.example.test",
			Port:     51820,
			WireGuard: &model.WireGuard{
				PrivateKey:    "PRIVKEYFAKE",
				PeerPublicKey: "PEERPUBFAKE",
				PreSharedKey:  "PSKFAKE",
				LocalAddress:  []string{"10.0.0.2/32"},
				Jc:            4,
				Jmin:          40,
				Jmax:          70,
				S1:            15,
				S2:            25,
				H1:            1234567890,
			},
		},
	}
}

func buildFake(t *testing.T) map[string]any {
	t.Helper()
	cfg, err := Build(fakeNodes(), "hy2", routing.Options{Mode: routing.ModeSmart}, TunOptions{})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	return cfg
}

// outboundsByTag indexes the outbounds array by tag for assertions.
func outboundsByTag(t *testing.T, cfg map[string]any) map[string]map[string]any {
	t.Helper()
	outs, ok := cfg["outbounds"].([]map[string]any)
	if !ok {
		t.Fatalf("outbounds wrong type: %T", cfg["outbounds"])
	}
	m := map[string]map[string]any{}
	for _, o := range outs {
		m[o["tag"].(string)] = o
	}
	return m
}

func TestBuildMarshalsToValidJSON(t *testing.T) {
	cfg := buildFake(t)
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("config does not marshal: %v", err)
	}
	// Round-trip to ensure it is well-formed JSON.
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
}

func TestLogBlock(t *testing.T) {
	cfg := buildFake(t)
	log, ok := cfg["log"].(map[string]any)
	if !ok {
		t.Fatal("missing log block")
	}
	if log["level"] != "info" || log["timestamp"] != true {
		t.Errorf("log block = %v", log)
	}
}

func TestTunInboundPresent(t *testing.T) {
	cfg := buildFake(t)
	inbounds, ok := cfg["inbounds"].([]map[string]any)
	if !ok || len(inbounds) != 1 {
		t.Fatalf("inbounds = %v, want 1 tun", cfg["inbounds"])
	}
	in := inbounds[0]
	if in["type"] != "tun" || in["tag"] != tunTag {
		t.Errorf("inbound type/tag = %v/%v", in["type"], in["tag"])
	}
	if in["auto_route"] != true || in["strict_route"] != true {
		t.Errorf("tun must have auto_route+strict_route, got %v", in)
	}
	addr, ok := in["address"].([]string)
	if !ok || len(addr) != 1 || addr[0] != tunAddr {
		t.Errorf("tun address = %v, want [%s]", in["address"], tunAddr)
	}
	if in["interface_name"] != defaultInterfaceName {
		t.Errorf("interface_name = %v, want %v", in["interface_name"], defaultInterfaceName)
	}
	if in["mtu"] != defaultMTU || in["stack"] != defaultStack {
		t.Errorf("mtu/stack = %v/%v", in["mtu"], in["stack"])
	}
}

func TestTunOptionsOverride(t *testing.T) {
	cfg, err := Build(fakeNodes(), "", routing.Options{Mode: routing.ModeGlobal},
		TunOptions{InterfaceName: "wg0", MTU: 1500, Stack: "gvisor", ClashAPIPort: 9999})
	if err != nil {
		t.Fatal(err)
	}
	in := cfg["inbounds"].([]map[string]any)[0]
	if in["interface_name"] != "wg0" || in["mtu"] != 1500 || in["stack"] != "gvisor" {
		t.Errorf("tun overrides not applied: %v", in)
	}
	exp := cfg["experimental"].(map[string]any)
	clash := exp["clash_api"].(map[string]any)
	if clash["external_controller"] != "127.0.0.1:9999" {
		t.Errorf("clash api port not applied: %v", clash)
	}
}

func TestSelectorDefaultAndMembers(t *testing.T) {
	cfg := buildFake(t)
	by := outboundsByTag(t, cfg)
	sel, ok := by[proxyTag]
	if !ok {
		t.Fatal("missing proxy selector")
	}
	if sel["type"] != "selector" {
		t.Errorf("proxy type = %v, want selector", sel["type"])
	}
	if sel["default"] != "hy2" {
		t.Errorf("selector default = %v, want hy2", sel["default"])
	}
	members, ok := sel["outbounds"].([]string)
	if !ok {
		t.Fatalf("selector outbounds type %T", sel["outbounds"])
	}
	// All 7 node tags should be present (including the WG endpoint tag).
	want := []string{"vless-reality", "vless-ws", "hy2", "ss", "trojan", "vmess", "awg"}
	for _, w := range want {
		if !contains(members, w) {
			t.Errorf("selector missing member %q (got %v)", w, members)
		}
	}
}

func TestSelectorDefaultFallback(t *testing.T) {
	// Unknown selectedTag should fall back to first node tag.
	cfg, err := Build(fakeNodes(), "does-not-exist", routing.Options{Mode: routing.ModeSmart}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sel := outboundsByTag(t, cfg)[proxyTag]
	if sel["default"] != "vless-reality" {
		t.Errorf("fallback default = %v, want vless-reality", sel["default"])
	}
}

func TestDirectAndBlockOutbounds(t *testing.T) {
	by := outboundsByTag(t, buildFake(t))
	if by[directTag]["type"] != "direct" {
		t.Errorf("direct outbound missing/wrong: %v", by[directTag])
	}
	if by[blockTag]["type"] != "block" {
		t.Errorf("block outbound missing/wrong: %v", by[blockTag])
	}
}

func TestVLESSRealityOutbound(t *testing.T) {
	by := outboundsByTag(t, buildFake(t))
	o := by["vless-reality"]
	if o["type"] != "vless" {
		t.Fatalf("type = %v", o["type"])
	}
	if o["uuid"] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("uuid = %v", o["uuid"])
	}
	if o["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow = %v", o["flow"])
	}
	tls := o["tls"].(map[string]any)
	if tls["enabled"] != true || tls["server_name"] != "example.test" {
		t.Errorf("tls = %v", tls)
	}
	utls := tls["utls"].(map[string]any)
	if utls["enabled"] != true || utls["fingerprint"] != "chrome" {
		t.Errorf("utls = %v", utls)
	}
	reality := tls["reality"].(map[string]any)
	if reality["enabled"] != true || reality["public_key"] != "PUBKEYFAKE" || reality["short_id"] != "ab12" {
		t.Errorf("reality = %v", reality)
	}
}

func TestVLESSNoRealityWhenAbsent(t *testing.T) {
	by := outboundsByTag(t, buildFake(t))
	o := by["vless-ws"]
	tls := o["tls"].(map[string]any)
	if _, has := tls["reality"]; has {
		t.Error("vless-ws should not have reality")
	}
	if _, has := tls["utls"]; has {
		t.Error("vless-ws should not have utls (no fingerprint)")
	}
	tr := o["transport"].(map[string]any)
	if tr["type"] != "ws" || tr["path"] != "/path" {
		t.Errorf("ws transport = %v", tr)
	}
	headers := tr["headers"].(map[string]any)
	if headers["Host"] != "ws.example.test" {
		t.Errorf("ws Host header = %v", headers)
	}
}

func TestHysteria2Outbound(t *testing.T) {
	o := outboundsByTag(t, buildFake(t))["hy2"]
	if o["type"] != "hysteria2" || o["password"] != "hy2pass" {
		t.Errorf("hy2 = %v", o)
	}
	obfs := o["obfs"].(map[string]any)
	if obfs["type"] != "salamander" || obfs["password"] != "obfspass" {
		t.Errorf("obfs = %v", obfs)
	}
	tls := o["tls"].(map[string]any)
	if tls["insecure"] != true {
		t.Errorf("hy2 tls insecure = %v", tls)
	}
	alpn, ok := tls["alpn"].([]string)
	if !ok || len(alpn) != 1 || alpn[0] != "h3" {
		t.Errorf("hy2 alpn = %v", tls["alpn"])
	}
}

func TestShadowsocksOutbound(t *testing.T) {
	o := outboundsByTag(t, buildFake(t))["ss"]
	if o["type"] != "shadowsocks" || o["method"] != "aes-256-gcm" || o["password"] != "sspass" {
		t.Errorf("ss = %v", o)
	}
	if o["server_port"] != 8388 {
		t.Errorf("ss port = %v", o["server_port"])
	}
}

func TestTrojanOutbound(t *testing.T) {
	o := outboundsByTag(t, buildFake(t))["trojan"]
	if o["type"] != "trojan" || o["password"] != "trpass" {
		t.Errorf("trojan = %v", o)
	}
	tls := o["tls"].(map[string]any)
	if tls["enabled"] != true {
		t.Errorf("trojan tls = %v", tls)
	}
	tr := o["transport"].(map[string]any)
	if tr["type"] != "grpc" || tr["service_name"] != "grpcsvc" {
		t.Errorf("grpc transport = %v", tr)
	}
}

func TestVMessOutbound(t *testing.T) {
	o := outboundsByTag(t, buildFake(t))["vmess"]
	if o["type"] != "vmess" || o["security"] != "auto" {
		t.Errorf("vmess = %v", o)
	}
	if o["alter_id"] != 0 {
		t.Errorf("vmess alter_id = %v", o["alter_id"])
	}
	if o["uuid"] != "33333333-3333-3333-3333-333333333333" {
		t.Errorf("vmess uuid = %v", o["uuid"])
	}
}

func TestWireGuardEndpoint(t *testing.T) {
	cfg := buildFake(t)
	eps, ok := cfg["endpoints"].([]map[string]any)
	if !ok || len(eps) != 1 {
		t.Fatalf("endpoints = %v, want 1", cfg["endpoints"])
	}
	ep := eps[0]
	if ep["type"] != "wireguard" || ep["tag"] != "awg" {
		t.Errorf("endpoint type/tag = %v/%v", ep["type"], ep["tag"])
	}
	if ep["private_key"] != "PRIVKEYFAKE" {
		t.Errorf("private_key = %v", ep["private_key"])
	}
	addr := ep["address"].([]string)
	if len(addr) != 1 || addr[0] != "10.0.0.2/32" {
		t.Errorf("local address = %v", addr)
	}
	peers := ep["peers"].([]map[string]any)
	if len(peers) != 1 {
		t.Fatalf("peers = %v", peers)
	}
	p := peers[0]
	if p["public_key"] != "PEERPUBFAKE" || p["address"] != "wg.example.test" || p["port"] != 51820 {
		t.Errorf("peer = %v", p)
	}
	if p["pre_shared_key"] != "PSKFAKE" {
		t.Errorf("peer psk = %v", p["pre_shared_key"])
	}
	// AmneziaWG knobs present on the endpoint.
	if ep["jc"] != 4 || ep["s1"] != 15 || ep["h1"] != int64(1234567890) {
		t.Errorf("amnezia knobs = jc:%v s1:%v h1:%v", ep["jc"], ep["s1"], ep["h1"])
	}
}

func TestRouteBlock(t *testing.T) {
	cfg := buildFake(t)
	route := cfg["route"].(map[string]any)
	if route["final"] != proxyTag {
		t.Errorf("route final = %v, want proxy", route["final"])
	}
	if route["auto_detect_interface"] != true {
		t.Errorf("auto_detect_interface = %v", route["auto_detect_interface"])
	}
	rs, ok := route["rule_set"].([]map[string]any)
	if !ok || len(rs) != 2 {
		t.Fatalf("route rule_set = %v, want 2", route["rule_set"])
	}
	// Confirm the official RU rule-set URLs are wired through.
	var urls []string
	for _, s := range rs {
		urls = append(urls, s["url"].(string))
	}
	joined := strings.Join(urls, " ")
	if !strings.Contains(joined, "sing-geoip/rule-set/geoip-ru.srs") {
		t.Errorf("missing geoip-ru url: %v", urls)
	}
	if !strings.Contains(joined, "sing-geosite/rule-set/geosite-category-ru.srs") {
		t.Errorf("missing geosite-category-ru url: %v", urls)
	}
}

func TestExperimentalBlock(t *testing.T) {
	exp := buildFake(t)["experimental"].(map[string]any)
	clash := exp["clash_api"].(map[string]any)
	if clash["external_controller"] != "127.0.0.1:9090" {
		t.Errorf("default clash api = %v", clash["external_controller"])
	}
	cache := exp["cache_file"].(map[string]any)
	if cache["enabled"] != true {
		t.Errorf("cache_file = %v", cache)
	}
}

func TestNoUsableNodes(t *testing.T) {
	_, err := Build(nil, "", routing.Options{Mode: routing.ModeSmart}, TunOptions{})
	if err == nil {
		t.Fatal("expected error for no nodes")
	}
	if !strings.Contains(err.Error(), "no usable nodes") {
		t.Errorf("error = %v", err)
	}
}

func TestUnknownProtocolSkipped(t *testing.T) {
	nodes := []model.Node{
		{Protocol: "carrier-pigeon", Name: "weird", Server: "x.test", Port: 1},
		{Protocol: model.Shadowsocks, Name: "ss", Server: "ss.test", Port: 8388, Method: "aes-256-gcm", Password: "p"},
	}
	cfg, err := Build(nodes, "", routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
	if err != nil {
		t.Fatalf("unknown protocol should be skipped, not error: %v", err)
	}
	by := outboundsByTag(t, cfg)
	if _, has := by["weird"]; has {
		t.Error("unknown protocol should not appear in outbounds")
	}
	if _, has := by["ss"]; !has {
		t.Error("valid node should survive alongside skipped one")
	}
}

func TestZeroProtocolSkipped(t *testing.T) {
	nodes := []model.Node{
		{Name: "empty", Server: "x.test", Port: 1}, // zero protocol
		{Protocol: model.Shadowsocks, Name: "ss", Server: "ss.test", Port: 8388, Method: "aes-256-gcm", Password: "p"},
	}
	cfg, err := Build(nodes, "", routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := outboundsByTag(t, cfg)["empty"]; has {
		t.Error("zero-protocol node should be skipped")
	}
}

func TestTagDedup(t *testing.T) {
	nodes := []model.Node{
		{Protocol: model.Shadowsocks, Name: "dup", Server: "a.test", Port: 1, Method: "aes-256-gcm", Password: "p"},
		{Protocol: model.Shadowsocks, Name: "dup", Server: "b.test", Port: 2, Method: "aes-256-gcm", Password: "p"},
		{Protocol: model.Shadowsocks, Name: "dup", Server: "c.test", Port: 3, Method: "aes-256-gcm", Password: "p"},
	}
	cfg, err := Build(nodes, "", routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	by := outboundsByTag(t, cfg)
	for _, want := range []string{"dup", "dup-2", "dup-3"} {
		if _, has := by[want]; !has {
			t.Errorf("missing deduped tag %q; got tags %v", want, keys(by))
		}
	}
}

func TestSanitizeTag(t *testing.T) {
	tests := []struct{ in, want string }{
		{"clean", "clean"},
		{"  spaces  ", "spaces"},
		{"", "node"},
		{"   ", "node"},
		{"with\"quote", "with-quote"},
		{"tab\there", "tab-here"},
		{"emoji-ok-привет", "emoji-ok-привет"},
	}
	for _, tt := range tests {
		if got := sanitizeTag(tt.in); got != tt.want {
			t.Errorf("sanitizeTag(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDirectModeFinalIsDirect(t *testing.T) {
	cfg, err := Build(fakeNodes(), "", routing.Options{Mode: routing.ModeDirect}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	route := cfg["route"].(map[string]any)
	if route["final"] != directTag {
		t.Errorf("direct mode route final = %v, want direct", route["final"])
	}
	// No rule_set downloads in direct mode.
	if _, has := route["rule_set"]; has {
		t.Errorf("direct mode should not emit rule_set, got %v", route["rule_set"])
	}
}

// routeProcessRule finds the first route rule carrying a process_name match in a
// built config's route block.
func routeProcessRule(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	route := cfg["route"].(map[string]any)
	rules, ok := route["rules"].([]map[string]any)
	if !ok {
		t.Fatalf("route rules wrong type: %T", route["rules"])
	}
	for _, r := range rules {
		if _, has := r["process_name"]; has {
			return r
		}
	}
	return nil
}

func TestBuildSplitExcludeEmitsProcessNameDirect(t *testing.T) {
	cfg, err := Build(fakeNodes(), "hy2", routing.Options{
		Mode:      routing.ModeSmart,
		SplitMode: routing.SplitExclude,
		SplitApps: []string{"chrome.exe"},
	}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pr := routeProcessRule(t, cfg)
	if pr == nil {
		t.Fatal("exclude build is missing a process_name route rule")
	}
	if pr["outbound"] != directTag || pr["action"] != "route" {
		t.Errorf("exclude process rule = %v, want action route outbound direct", pr)
	}
	// Exclude leaves the final on the proxy.
	if route := cfg["route"].(map[string]any); route["final"] != proxyTag {
		t.Errorf("exclude final = %v, want proxy", route["final"])
	}
}

func TestBuildSplitIncludeFinalDirectAppToProxy(t *testing.T) {
	cfg, err := Build(fakeNodes(), "hy2", routing.Options{
		Mode:      routing.ModeGlobal,
		SplitMode: routing.SplitInclude,
		SplitApps: []string{"chrome.exe"},
	}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pr := routeProcessRule(t, cfg)
	if pr == nil {
		t.Fatal("include build is missing a process_name route rule")
	}
	if pr["outbound"] != proxyTag {
		t.Errorf("include process rule outbound = %v, want proxy", pr["outbound"])
	}
	// Include forces the final to direct so only the listed app reaches the proxy.
	route := cfg["route"].(map[string]any)
	if route["final"] != directTag {
		t.Errorf("include final = %v, want direct", route["final"])
	}
}

func keys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
