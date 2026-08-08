package singbox

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/routing"
)

// dpiGoldenRouting and dpiGoldenTun are the inputs behind dpiGoldenConfig: a
// loaded smart-mode setup (LAN bypass, kill switch, split apps, both custom rule
// directions) that touches every block the bypass could disturb. The interface
// name is pinned because its default is platform-dependent, and the rule-set/cache
// directories are left empty so no absolute path leaks into the snapshot.
func dpiGoldenRouting(bypass bool) routing.Options {
	return routing.Options{
		Mode:        routing.ModeSmart,
		BypassLAN:   true,
		KillSwitch:  true,
		IPv4Only:    true,
		SplitMode:   routing.SplitExclude,
		SplitApps:   []string{"chrome.exe", "steam.exe"},
		RulesDirect: []string{"example.ru"},
		RulesProxy:  []string{"example.com"},
		DPIBypass:   bypass,
	}
}

func dpiGoldenTun() TunOptions { return TunOptions{InterfaceName: "tenebra"} }

// canonicalConfig renders a built config the way the golden stores it: generic
// JSON (so map keys sort), indented, with the per-run clash secret replaced —
// everything else must be byte-stable.
func canonicalConfig(t *testing.T, cfg map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	generic["experimental"].(map[string]any)["clash_api"].(map[string]any)["secret"] = "REDACTED"
	out, err := json.MarshalIndent(generic, "", "\t")
	if err != nil {
		t.Fatalf("re-marshal config: %v", err)
	}
	return string(out)
}

// dpiHelperBinary mirrors the executable name the routing package matches on, so
// the built config can be checked without exporting it.
func dpiHelperBinary() string {
	if runtime.GOOS == "windows" {
		return "ciadpi.exe"
	}
	return "ciadpi"
}

// TestConfigUnchangedWithoutDPIBypass is the regression gate for the whole
// feature: with the bypass off, Build must produce exactly the config it produced
// before the bypass existed — same outbounds, same rule order, same everything.
func TestConfigUnchangedWithoutDPIBypass(t *testing.T) {
	cfg, err := Build(fakeNodes(), "hy2", dpiGoldenRouting(false), dpiGoldenTun())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	got := strings.TrimSpace(canonicalConfig(t, cfg))
	want := strings.TrimSpace(dpiGoldenConfig)
	if got != want {
		t.Errorf("config drifted with the bypass off\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestDPIBypassOffEmitsNoHelper walks the modes to confirm nothing DPI-shaped
// leaks into a config built with the toggle off — no outbound, no route rule.
func TestDPIBypassOffEmitsNoHelper(t *testing.T) {
	for _, m := range []routing.Mode{routing.ModeSmart, routing.ModeGlobal, routing.ModeDirect} {
		cfg, err := Build(fakeNodes(), "hy2", routing.Options{Mode: m}, TunOptions{})
		if err != nil {
			t.Fatalf("Build(%s) error: %v", m, err)
		}
		if _, has := outboundsByTag(t, cfg)[dpiTag]; has {
			t.Errorf("%s: helper outbound emitted with the bypass off", m)
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(raw), `"`+dpiTag+`"`) {
			t.Errorf("%s: config mentions the helper tag with the bypass off: %s", m, raw)
		}
	}
}

// TestDPIOutboundShape pins the outbound the helper is reached through: a plain
// SOCKS5 client aimed at the loopback port the runner binds.
func TestDPIOutboundShape(t *testing.T) {
	cfg, err := Build(fakeNodes(), "hy2", dpiGoldenRouting(true), dpiGoldenTun())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	o, has := outboundsByTag(t, cfg)[dpiTag]
	if !has {
		t.Fatalf("no %q outbound with the bypass on", dpiTag)
	}
	want := map[string]any{
		"type":        "socks",
		"tag":         dpiTag,
		"server":      "127.0.0.1",
		"server_port": DefaultDPIPort,
		"version":     "5",
	}
	for k, v := range want {
		if o[k] != v {
			t.Errorf("helper outbound %q = %v, want %v", k, o[k], v)
		}
	}
	if len(o) != len(want) {
		t.Errorf("helper outbound carries extra fields: %v", o)
	}
}

// TestDPIPortReachesOutbound: the port the control layer picked for the helper is
// the port the config dials, and zero falls back to the shared default so the two
// sides cannot drift.
func TestDPIPortReachesOutbound(t *testing.T) {
	cfg, err := Build(fakeNodes(), "hy2", dpiGoldenRouting(true), TunOptions{DPIPort: 3128})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if got := outboundsByTag(t, cfg)[dpiTag]["server_port"]; got != 3128 {
		t.Errorf("helper server_port = %v, want 3128", got)
	}

	cfg, err = Build(fakeNodes(), "hy2", dpiGoldenRouting(true), TunOptions{})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if got := outboundsByTag(t, cfg)[dpiTag]["server_port"]; got != DefaultDPIPort {
		t.Errorf("helper server_port = %v, want the default %d", got, DefaultDPIPort)
	}
}

// TestDPIPortNormalizes covers the normalize step on its own and pins the default
// away from the other loopback listeners the builder hands out.
func TestDPIPortNormalizes(t *testing.T) {
	if got := (TunOptions{}).normalize().DPIPort; got != DefaultDPIPort {
		t.Errorf("normalized DPIPort = %d, want %d", got, DefaultDPIPort)
	}
	if got := (TunOptions{DPIPort: 4444}).normalize().DPIPort; got != 4444 {
		t.Errorf("normalize overwrote an explicit DPIPort: %d", got)
	}
	if DefaultDPIPort == DefaultMixedPort || DefaultDPIPort == defaultClashAPIPort {
		t.Errorf("DefaultDPIPort %d collides with another loopback default", DefaultDPIPort)
	}
}

// TestDPIHelperRuleFirstInBuiltConfig checks the loop guard survives the trip
// through Build: the helper's own sockets are pinned to direct before any rule
// that could route them back into it.
func TestDPIHelperRuleFirstInBuiltConfig(t *testing.T) {
	cfg, err := Build(fakeNodes(), "hy2", dpiGoldenRouting(true), dpiGoldenTun())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	rules := cfg["route"].(map[string]any)["rules"].([]map[string]any)
	if len(rules) < 3 {
		t.Fatalf("route rules = %d, want at least 3", len(rules))
	}
	guard := rules[2]
	names, ok := guard["process_name"].([]string)
	if !ok || len(names) != 1 || names[0] != dpiHelperBinary() {
		t.Fatalf("third rule = %v, want a process_name match on %q", guard, dpiHelperBinary())
	}
	if guard["outbound"] != directTag {
		t.Errorf("helper loop guard outbound = %v, want %q", guard["outbound"], directTag)
	}
}

// TestDPIRouteReferencesResolve is the dangling-tag guard: every outbound a route
// rule or the final names must actually exist in the config. sing-box accepts a
// dangling reference at check time and then misroutes silently, so the config
// itself has to be self-consistent in both bypass states.
func TestDPIRouteReferencesResolve(t *testing.T) {
	for _, m := range []routing.Mode{routing.ModeSmart, routing.ModeGlobal, routing.ModeDirect} {
		for _, sm := range []routing.SplitMode{routing.SplitOff, routing.SplitExclude, routing.SplitInclude} {
			for _, on := range []bool{false, true} {
				ro := routing.Options{
					Mode:        m,
					BypassLAN:   true,
					SplitMode:   sm,
					SplitApps:   []string{"chrome.exe"},
					RulesDirect: []string{"example.ru"},
					RulesProxy:  []string{"example.com"},
					DPIBypass:   on,
				}
				cfg, err := Build(fakeNodes(), "hy2", ro, TunOptions{})
				if err != nil {
					t.Fatalf("Build(%s/%s bypass=%v) error: %v", m, sm, on, err)
				}
				defined := map[string]bool{}
				for tag := range outboundsByTag(t, cfg) {
					defined[tag] = true
				}
				for _, ep := range cfg["endpoints"].([]map[string]any) {
					defined[ep["tag"].(string)] = true
				}
				route := cfg["route"].(map[string]any)
				refs := []string{route["final"].(string)}
				for _, r := range route["rules"].([]map[string]any) {
					if out, has := r["outbound"].(string); has {
						refs = append(refs, out)
					}
				}
				for _, ref := range refs {
					if !defined[ref] {
						t.Errorf("%s/%s bypass=%v: route references undefined outbound %q", m, sm, on, ref)
					}
				}
			}
		}
	}
}

// TestDPIConfigPassesSingBoxCheck feeds a real `sing-box check` a bypass config.
// An offline map assertion cannot tell whether the socks outbound matches the
// bundled 1.13 schema; this can. Skipped when no sing-box binary is available,
// like the sibling e2e checks.
func TestDPIConfigPassesSingBoxCheck(t *testing.T) {
	bin, _, ok := findSingBox()
	if !ok {
		t.Skip("sing-box binary not found (resources/ or bin/ or PATH); skipping real config check")
	}
	// Global mode needs no geo rule-set files, so it always runs; direct mode is the
	// case where the route final itself points at the helper.
	for _, m := range []routing.Mode{routing.ModeGlobal, routing.ModeDirect} {
		cfg, err := Build(checkNodes(), "", routing.Options{Mode: m, DPIBypass: true}, TunOptions{})
		if err != nil {
			t.Fatalf("build %s bypass config: %v", m, err)
		}
		t.Run(string(m), func(t *testing.T) { singBoxCheck(t, bin, cfg) })
	}
}

// dpiGoldenConfig is the config Build produces for dpiGoldenRouting(false) —
// captured from the build before the DPI bypass existed. It is compared verbatim,
// so any accidental change to an outbound, a rule or their order fails loudly
// rather than shipping.
const dpiGoldenConfig = `
{
	"dns": {
		"final": "dns-remote",
		"rules": [
			{
				"action": "route",
				"domain_suffix": [
					"example.ru"
				],
				"server": "dns-direct"
			},
			{
				"action": "route",
				"domain_suffix": [
					"example.com"
				],
				"server": "dns-remote"
			},
			{
				"action": "route",
				"rule_set": [
					"geosite-ru"
				],
				"server": "dns-direct"
			}
		],
		"servers": [
			{
				"detour": "proxy",
				"server": "1.1.1.1",
				"tag": "dns-remote",
				"type": "tls"
			},
			{
				"path": "/dns-query",
				"server": "77.88.8.8",
				"tag": "dns-direct",
				"type": "https"
			}
		],
		"strategy": "ipv4_only"
	},
	"endpoints": [
		{
			"address": [
				"10.0.0.2/32"
			],
			"peers": [
				{
					"address": "wg.example.test",
					"allowed_ips": [
						"0.0.0.0/0",
						"::/0"
					],
					"port": 51820,
					"pre_shared_key": "PSKFAKE",
					"public_key": "PEERPUBFAKE"
				}
			],
			"private_key": "PRIVKEYFAKE",
			"tag": "awg",
			"type": "wireguard"
		}
	],
	"experimental": {
		"cache_file": {
			"enabled": true
		},
		"clash_api": {
			"external_controller": "127.0.0.1:9090",
			"secret": "REDACTED"
		}
	},
	"inbounds": [
		{
			"address": [
				"172.19.0.1/30",
				"fdfe:dcba:9876::1/126"
			],
			"auto_route": true,
			"interface_name": "tenebra",
			"mtu": 9000,
			"stack": "system",
			"strict_route": true,
			"tag": "tun-in",
			"type": "tun"
		}
	],
	"log": {
		"level": "warn",
		"timestamp": true
	},
	"outbounds": [
		{
			"default": "hy2",
			"outbounds": [
				"vless-reality",
				"vless-ws",
				"hy2",
				"ss",
				"trojan",
				"vmess",
				"awg"
			],
			"tag": "proxy",
			"type": "selector"
		},
		{
			"flow": "xtls-rprx-vision",
			"server": "example.test",
			"server_port": 443,
			"tag": "vless-reality",
			"tls": {
				"enabled": true,
				"reality": {
					"enabled": true,
					"public_key": "PUBKEYFAKE",
					"short_id": "ab12"
				},
				"server_name": "example.test",
				"utls": {
					"enabled": true,
					"fingerprint": "chrome"
				}
			},
			"type": "vless",
			"uuid": "11111111-1111-1111-1111-111111111111"
		},
		{
			"server": "ws.example.test",
			"server_port": 443,
			"tag": "vless-ws",
			"tls": {
				"enabled": true,
				"server_name": "ws.example.test"
			},
			"transport": {
				"headers": {
					"Host": "ws.example.test"
				},
				"path": "/path",
				"type": "ws"
			},
			"type": "vless",
			"uuid": "22222222-2222-2222-2222-222222222222"
		},
		{
			"obfs": {
				"password": "obfspass",
				"type": "salamander"
			},
			"password": "hy2pass",
			"server": "hy.example.test",
			"server_port": 8443,
			"tag": "hy2",
			"tls": {
				"alpn": [
					"h3"
				],
				"enabled": true,
				"insecure": true,
				"server_name": "hy.example.test"
			},
			"type": "hysteria2"
		},
		{
			"method": "aes-256-gcm",
			"password": "sspass",
			"server": "ss.example.test",
			"server_port": 8388,
			"tag": "ss",
			"type": "shadowsocks"
		},
		{
			"password": "trpass",
			"server": "tr.example.test",
			"server_port": 443,
			"tag": "trojan",
			"tls": {
				"enabled": true,
				"server_name": "tr.example.test"
			},
			"transport": {
				"service_name": "grpcsvc",
				"type": "grpc"
			},
			"type": "trojan"
		},
		{
			"alter_id": 0,
			"security": "auto",
			"server": "vm.example.test",
			"server_port": 443,
			"tag": "vmess",
			"type": "vmess",
			"uuid": "33333333-3333-3333-3333-333333333333"
		},
		{
			"tag": "direct",
			"type": "direct"
		},
		{
			"tag": "block",
			"type": "block"
		}
	],
	"route": {
		"auto_detect_interface": true,
		"default_domain_resolver": {
			"server": "dns-direct"
		},
		"final": "proxy",
		"rule_set": [
			{
				"download_detour": "direct",
				"format": "binary",
				"tag": "geoip-ru",
				"type": "remote",
				"update_interval": "168h",
				"url": "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-ru.srs"
			},
			{
				"download_detour": "direct",
				"format": "binary",
				"tag": "geosite-ru",
				"type": "remote",
				"update_interval": "168h",
				"url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ru.srs"
			}
		],
		"rules": [
			{
				"action": "sniff"
			},
			{
				"action": "hijack-dns",
				"protocol": "dns"
			},
			{
				"action": "route",
				"outbound": "direct",
				"process_name": [
					"chrome.exe",
					"steam.exe"
				]
			},
			{
				"action": "route",
				"domain_suffix": [
					"example.ru"
				],
				"outbound": "direct"
			},
			{
				"action": "route",
				"domain_suffix": [
					"example.com"
				],
				"outbound": "proxy"
			},
			{
				"action": "route",
				"outbound": "direct",
				"rule_set": [
					"geoip-ru",
					"geosite-ru"
				]
			},
			{
				"action": "route",
				"ip_is_private": true,
				"outbound": "direct"
			}
		]
	}
}
`
