package singbox

import (
	"encoding/json"
	"path/filepath"
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

// TestKillSwitchEnablesStrictRoute pins the kill-switch wiring: with the option
// set, the tun must turn strict_route on (the hard leak guarantee); without it,
// off (the gentle default verified in TestTunInboundPresent).
func TestKillSwitchEnablesStrictRoute(t *testing.T) {
	cfg, err := Build(fakeNodes(), "hy2",
		routing.Options{Mode: routing.ModeSmart, KillSwitch: true}, TunOptions{})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	in := cfg["inbounds"].([]map[string]any)[0]
	if in["strict_route"] != true {
		t.Errorf("kill-switch should set strict_route=true, got %v", in["strict_route"])
	}
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
	if log["level"] != "warn" || log["timestamp"] != true {
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
	if in["auto_route"] != true {
		t.Errorf("tun must have auto_route, got %v", in)
	}
	// strict_route follows the kill-switch option, which defaults off, so the
	// default config leaves it off to keep the connect transition gentle.
	if in["strict_route"] != false {
		t.Errorf("tun strict_route should default to false, got %v", in["strict_route"])
	}
	addr, ok := in["address"].([]string)
	if !ok || len(addr) != 1 || addr[0] != tunAddr {
		t.Errorf("tun address = %v, want [%s]", in["address"], tunAddr)
	}
	// The default interface name is platform-dependent: a branded "tenebra" where
	// the OS allows an arbitrary name, and omitted on macOS so sing-box claims a
	// utun device itself. An empty default must not appear as interface_name: "".
	if platformTUNName == "" {
		if _, ok := in["interface_name"]; ok {
			t.Errorf("interface_name should be omitted on this platform, got %v", in["interface_name"])
		}
	} else if in["interface_name"] != platformTUNName {
		t.Errorf("interface_name = %v, want %v", in["interface_name"], platformTUNName)
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

func TestCacheFileDefaultsToNoPath(t *testing.T) {
	// With no CacheDir the cache is enabled but carries no path, so sing-box
	// resolves it against its working directory — the GUI-sidecar default.
	cache := buildFake(t)["experimental"].(map[string]any)["cache_file"].(map[string]any)
	if cache["enabled"] != true {
		t.Errorf("cache_file should be enabled, got %v", cache)
	}
	if _, hasPath := cache["path"]; hasPath {
		t.Errorf("cache_file must omit path when CacheDir is empty, got %v", cache)
	}
}

func TestCacheFilePinnedToCacheDir(t *testing.T) {
	// A CacheDir (the daemon's writable store dir) pins cache.db to an absolute
	// path so the root launchd daemon, whose cwd "/" is read-only, can start.
	cfg, err := Build(fakeNodes(), "", routing.Options{Mode: routing.ModeSmart},
		TunOptions{CacheDir: "/Library/Application Support/Tenebra/data"})
	if err != nil {
		t.Fatal(err)
	}
	cache := cfg["experimental"].(map[string]any)["cache_file"].(map[string]any)
	// Build the expected path with filepath.Join too, so the separator matches
	// the host: the builder joins with the OS separator (backslash on Windows),
	// and a hard-coded forward-slash literal would only match on Unix.
	dir := "/Library/Application Support/Tenebra/data"
	want := filepath.Join(dir, "cache.db")
	if cache["path"] != want {
		t.Errorf("cache_file path = %v, want %v", cache["path"], want)
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

// TestVLESSRealityDefaultsFingerprint covers the secondary bug: a REALITY node
// with no uTLS fingerprint makes sing-box FATAL ("uTLS is required by reality
// client"). The builder must default the fingerprint to chrome so one such node
// can't sink the whole config.
func TestVLESSRealityDefaultsFingerprint(t *testing.T) {
	nodes := []model.Node{{
		Protocol: model.VLESS,
		Name:     "reality-nofp",
		Server:   "r.example.test",
		Port:     443,
		UUID:     "44444444-4444-4444-4444-444444444444",
		TLS: &model.TLS{
			Enabled:    true,
			ServerName: "r.example.test",
			Reality:    &model.Reality{PublicKey: "PUBKEYFAKE"},
			// Fingerprint deliberately empty.
		},
	}}
	cfg, err := Build(nodes, "", routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tls := outboundsByTag(t, cfg)["reality-nofp"]["tls"].(map[string]any)
	utls, ok := tls["utls"].(map[string]any)
	if !ok {
		t.Fatalf("reality node without fingerprint must still emit utls; tls=%v", tls)
	}
	if utls["enabled"] != true || utls["fingerprint"] != "chrome" {
		t.Errorf("utls = %v, want enabled chrome", utls)
	}
	if _, has := tls["reality"]; !has {
		t.Error("reality object missing")
	}
}

// TestNoRealityNoFingerprintStaysBare guards that the chrome default is scoped
// to REALITY: a plain-TLS node with no fingerprint must not gain a uTLS object.
func TestNoRealityNoFingerprintStaysBare(t *testing.T) {
	nodes := []model.Node{{
		Protocol: model.VLESS,
		Name:     "plain-tls",
		Server:   "p.example.test",
		Port:     443,
		UUID:     "55555555-5555-5555-5555-555555555555",
		TLS:      &model.TLS{Enabled: true, ServerName: "p.example.test"},
	}}
	cfg, err := Build(nodes, "", routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tls := outboundsByTag(t, cfg)["plain-tls"]["tls"].(map[string]any)
	if _, has := tls["utls"]; has {
		t.Errorf("plain TLS node should not get utls, got %v", tls["utls"])
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
	// The bundled binary is stock sing-box, which has no AmneziaWG support and
	// FATALs on these keys at decode. The endpoint must be plain WireGuard: none
	// of the amnezia obfuscation knobs may be serialized even though the node
	// carries them, or the whole config would be rejected.
	for _, k := range []string{"jc", "jmin", "jmax", "s1", "s2", "h1", "h2", "h3", "h4"} {
		if _, has := ep[k]; has {
			t.Errorf("plain WG endpoint must not carry amnezia knob %q, got %v", k, ep[k])
		}
		if _, has := p[k]; has {
			t.Errorf("peer must not carry amnezia knob %q, got %v", k, p[k])
		}
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

// TestRouteBlockLocalRuleSets confirms RuleSetDir threads through Build into the
// route block as local rule-sets with on-disk paths and no download URLs — the
// core of the freeze fix.
func TestRouteBlockLocalRuleSets(t *testing.T) {
	dir := "C:\\res"
	cfg, err := Build(fakeNodes(), "", routing.Options{Mode: routing.ModeSmart, RuleSetDir: dir}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	route := cfg["route"].(map[string]any)
	rs, ok := route["rule_set"].([]map[string]any)
	if !ok || len(rs) != 2 {
		t.Fatalf("route rule_set = %v, want 2", route["rule_set"])
	}
	for _, s := range rs {
		if s["type"] != "local" {
			t.Errorf("rule_set %v type = %v, want local", s["tag"], s["type"])
		}
		if _, has := s["url"]; has {
			t.Errorf("local rule_set %v must not have a url", s["tag"])
		}
		path, _ := s["path"].(string)
		if !strings.HasPrefix(path, dir) || !strings.HasSuffix(path, ".srs") {
			t.Errorf("rule_set %v path = %q, want under %q ending .srs", s["tag"], path, dir)
		}
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

// TestClashAPISecretPresentAndRandom is the config half of the clash API auth
// fix: every build must carry a non-empty clash_api secret, and two builds must
// not share one (crypto-random per run), so another local process can't reach
// the external controller by guessing a fixed token.
func TestClashAPISecretPresentAndRandom(t *testing.T) {
	clashOf := func() map[string]any {
		return buildFake(t)["experimental"].(map[string]any)["clash_api"].(map[string]any)
	}
	secret, ok := clashOf()["secret"].(string)
	if !ok || secret == "" {
		t.Fatalf("clash_api secret missing or empty: %v", clashOf()["secret"])
	}
	if other := clashOf()["secret"].(string); secret == other {
		t.Errorf("two builds shared clash_api secret %q; it must be per-run random", secret)
	}
}

// TestShadowsocksPluginSkipped covers fix #1: a Shadowsocks node carrying a
// transport plugin (v2ray-plugin / obfs / shadow-tls) can't be rendered — the
// outbound emits no plugin/plugin_opts — so it must be dropped like any other
// unsupported node rather than built into a plain outbound that fails the
// handshake in silence. The healthy node it shares a profile with must survive.
func TestShadowsocksPluginSkipped(t *testing.T) {
	plugin := goodSSNode("plugin-ss")
	plugin.Extra = map[string]string{"plugin": "v2ray-plugin;tls"}
	cfg, err := Build([]model.Node{plugin, goodSSNode("plain")}, "",
		routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
	if err != nil {
		t.Fatalf("plugin node should be skipped, not error: %v", err)
	}
	by := outboundsByTag(t, cfg)
	if _, has := by["plugin-ss"]; has {
		t.Error("shadowsocks node with a plugin must not appear in outbounds")
	}
	if _, has := by["plain"]; !has {
		t.Errorf("plain shadowsocks node must survive; tags %v", keys(by))
	}
	// validateNode must name the plugin so the skip reason is explicit.
	if err := validateNode(plugin); err == nil || !strings.Contains(err.Error(), "plugin") {
		t.Errorf("validateNode(plugin ss) = %v, want an error mentioning the plugin", err)
	}
	// The canonical exported predicate the control walks use must agree.
	if ValidateNode(plugin) {
		t.Error("ValidateNode should reject a shadowsocks node with a plugin")
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

// goodSSNode is a minimal valid Shadowsocks node used as the surviving member in
// the invalid-node-skip tests.
func goodSSNode(name string) model.Node {
	return model.Node{
		Protocol: model.Shadowsocks,
		Name:     name,
		Server:   "good.example.test",
		Port:     8388,
		Method:   "aes-256-gcm",
		Password: "p",
	}
}

// TestInvalidNodeSkippedNotFatal is the core of fix #1: a structurally-present
// but semantically-invalid node must be skipped, not abort the whole config, so
// the healthy node it shares a profile with still builds. Each case pairs one
// poisoning node with one good node and asserts the good one survives and the
// bad one is absent.
func TestInvalidNodeSkippedNotFatal(t *testing.T) {
	cases := []struct {
		name string
		bad  model.Node
	}{
		{
			name: "ss port out of uint16 range",
			bad:  model.Node{Protocol: model.Shadowsocks, Name: "bad", Server: "b.test", Port: 99999, Method: "aes-256-gcm", Password: "p"},
		},
		{
			name: "ss empty method",
			bad:  model.Node{Protocol: model.Shadowsocks, Name: "bad", Server: "b.test", Port: 8388, Method: "", Password: "p"},
		},
		{
			name: "ss empty password",
			bad:  model.Node{Protocol: model.Shadowsocks, Name: "bad", Server: "b.test", Port: 8388, Method: "aes-256-gcm", Password: ""},
		},
		{
			name: "trojan no password",
			bad:  model.Node{Protocol: model.Trojan, Name: "bad", Server: "b.test", Port: 443},
		},
		{
			name: "vless no uuid",
			bad:  model.Node{Protocol: model.VLESS, Name: "bad", Server: "b.test", Port: 443},
		},
		{
			name: "vmess no uuid",
			bad:  model.Node{Protocol: model.VMess, Name: "bad", Server: "b.test", Port: 443},
		},
		{
			name: "hysteria2 no password",
			bad:  model.Node{Protocol: model.Hysteria2, Name: "bad", Server: "b.test", Port: 8443},
		},
		{
			name: "reality empty public_key",
			bad: model.Node{
				Protocol: model.VLESS, Name: "bad", Server: "b.test", Port: 443,
				UUID: "66666666-6666-6666-6666-666666666666",
				TLS:  &model.TLS{Enabled: true, Reality: &model.Reality{}},
			},
		},
		{
			name: "amneziawg missing key material",
			bad:  model.Node{Protocol: model.AmneziaWG, Name: "bad", Server: "b.test", Port: 51820, WireGuard: &model.WireGuard{}},
		},
		{
			name: "port zero",
			bad:  model.Node{Protocol: model.Trojan, Name: "bad", Server: "b.test", Port: 0, Password: "x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes := []model.Node{tc.bad, goodSSNode("good")}
			cfg, err := Build(nodes, "", routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
			if err != nil {
				t.Fatalf("invalid node should be skipped, not error: %v", err)
			}
			by := outboundsByTag(t, cfg)
			if _, has := by["bad"]; has {
				t.Error("invalid node must not appear in outbounds")
			}
			if _, has := by["good"]; !has {
				t.Errorf("healthy node must survive alongside the skipped one; tags %v", keys(by))
			}
			// The selector must list the good node and never the bad one.
			sel := by[proxyTag]
			members := sel["outbounds"].([]string)
			if !contains(members, "good") || contains(members, "bad") {
				t.Errorf("selector members = %v, want good present and bad absent", members)
			}
		})
	}
}

// TestAllNodesInvalidStillErrors guards that the skip path doesn't paper over a
// profile where nothing is usable: with every node invalid, Build must still
// return the "no usable nodes" error.
func TestAllNodesInvalidStillErrors(t *testing.T) {
	nodes := []model.Node{
		{Protocol: model.Shadowsocks, Name: "a", Server: "a.test", Port: 99999, Method: "aes-256-gcm", Password: "p"},
		{Protocol: model.Trojan, Name: "b", Server: "b.test", Port: 443}, // no password
	}
	_, err := Build(nodes, "", routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
	if err == nil {
		t.Fatal("expected error when no node is usable")
	}
	if !strings.Contains(err.Error(), "no usable nodes") {
		t.Errorf("error = %v, want no usable nodes", err)
	}
}

// TestRealityEmptyPubKeyNeverEmitted is the builder-level guard for fix #3: even
// if a reality node reached tlsObject, no reality block with an empty public_key
// may be emitted. Here the node is skipped entirely (validateNode), so it must be
// absent; the assertion focuses on the good node carrying a proper reality block.
func TestRealityEmptyPubKeyNeverEmitted(t *testing.T) {
	good := model.Node{
		Protocol: model.VLESS, Name: "rgood", Server: "r.test", Port: 443,
		UUID: "77777777-7777-7777-7777-777777777777",
		TLS:  &model.TLS{Enabled: true, Reality: &model.Reality{PublicKey: "REALKEY", ShortID: "aa"}},
	}
	bad := model.Node{
		Protocol: model.VLESS, Name: "rbad", Server: "r.test", Port: 443,
		UUID: "88888888-8888-8888-8888-888888888888",
		TLS:  &model.TLS{Enabled: true, Reality: &model.Reality{}},
	}
	cfg, err := Build([]model.Node{bad, good}, "", routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	by := outboundsByTag(t, cfg)
	if _, has := by["rbad"]; has {
		t.Error("keyless reality node must be skipped")
	}
	tls := by["rgood"]["tls"].(map[string]any)
	reality := tls["reality"].(map[string]any)
	if reality["public_key"] != "REALKEY" {
		t.Errorf("good reality public_key = %v, want REALKEY", reality["public_key"])
	}
}

// TestTLSObjectDropsEmptyRealityKey unit-tests tlsObject directly: a reality
// sub-object with an empty public_key must never be serialized, and the chrome
// uTLS default must not kick in for it either.
func TestTLSObjectDropsEmptyRealityKey(t *testing.T) {
	o := tlsObject(&model.TLS{Enabled: true, Reality: &model.Reality{}})
	if o == nil {
		t.Fatal("tlsObject returned nil for enabled TLS")
	}
	if _, has := o["reality"]; has {
		t.Errorf("empty-key reality must not be emitted, got %v", o["reality"])
	}
	if _, has := o["utls"]; has {
		t.Errorf("no chrome uTLS default for keyless reality, got %v", o["utls"])
	}
	// A populated key still emits the block.
	o2 := tlsObject(&model.TLS{Enabled: true, Reality: &model.Reality{PublicKey: "K"}})
	if _, has := o2["reality"]; !has {
		t.Error("reality with a key must still be emitted")
	}
}

// TestWireGuardPlainEndpointNoAmneziaKeys is the fix #2 builder check: a node
// carrying amnezia knobs still produces a plain WireGuard endpoint with none of
// them, so stock sing-box accepts it.
func TestWireGuardPlainEndpointNoAmneziaKeys(t *testing.T) {
	node := model.Node{
		Protocol: model.AmneziaWG, Name: "awg2", Server: "wg.test", Port: 51820,
		WireGuard: &model.WireGuard{
			PrivateKey: "PRIV", PeerPublicKey: "PEER", LocalAddress: []string{"10.0.0.9/32"},
			Jc: 5, Jmin: 50, Jmax: 1000, S1: 1, S2: 2, H1: 1, H2: 2, H3: 3, H4: 4,
		},
	}
	cfg, err := Build([]model.Node{node}, "", routing.Options{Mode: routing.ModeGlobal}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	eps := cfg["endpoints"].([]map[string]any)
	if len(eps) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(eps))
	}
	ep := eps[0]
	if ep["type"] != "wireguard" || ep["private_key"] != "PRIV" {
		t.Errorf("endpoint = %v", ep)
	}
	for _, k := range []string{"jc", "jmin", "jmax", "s1", "s2", "h1", "h2", "h3", "h4"} {
		if _, has := ep[k]; has {
			t.Errorf("amnezia knob %q leaked into endpoint: %v", k, ep[k])
		}
	}
}

func keys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
