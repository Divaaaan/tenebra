package singbox

import (
	"testing"

	"github.com/Divaaaan/tenebra/core/routing"
)

// TestSystemProxyInboundReplacesTun pins the core of system-proxy mode: with
// TunOptions.Mode == ModeSystemProxy the single inbound is a loopback mixed
// listener (HTTP + SOCKS on one port), and none of the tun/auto_route fields are
// emitted — there is no tun to route into, so a stray auto_route/strict_route
// would be a schema lie.
func TestSystemProxyInboundReplacesTun(t *testing.T) {
	cfg, err := Build(fakeNodes(), "hy2",
		routing.Options{Mode: routing.ModeSmart},
		TunOptions{Mode: ModeSystemProxy})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	inbounds, ok := cfg["inbounds"].([]map[string]any)
	if !ok || len(inbounds) != 1 {
		t.Fatalf("inbounds = %v, want 1 mixed", cfg["inbounds"])
	}
	in := inbounds[0]
	if in["type"] != "mixed" || in["tag"] != mixedTag {
		t.Errorf("inbound type/tag = %v/%v, want mixed/%s", in["type"], in["tag"], mixedTag)
	}
	if in["listen"] != mixedListen {
		t.Errorf("mixed must bind loopback, listen = %v, want %v", in["listen"], mixedListen)
	}
	if in["listen_port"] != DefaultMixedPort {
		t.Errorf("mixed listen_port = %v, want default %d", in["listen_port"], DefaultMixedPort)
	}
	// None of the tun/auto_route knobs may leak into a mixed inbound.
	for _, k := range []string{"auto_route", "strict_route", "address", "interface_name", "mtu", "stack"} {
		if _, present := in[k]; present {
			t.Errorf("mixed inbound must not carry tun field %q, got %v", k, in[k])
		}
	}
}

// TestSystemProxyIgnoresKillSwitch confirms the kill switch (strict_route) has no
// footprint in system-proxy mode: strict_route is a tun/auto_route firewall
// concept, and the mixed inbound carries no such field even when the option is
// armed. The mode swap must not silently drop a leak guarantee onto a listener
// that cannot honour it.
func TestSystemProxyIgnoresKillSwitch(t *testing.T) {
	cfg, err := Build(fakeNodes(), "hy2",
		routing.Options{Mode: routing.ModeSmart, KillSwitch: true},
		TunOptions{Mode: ModeSystemProxy})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	in := cfg["inbounds"].([]map[string]any)[0]
	if _, present := in["strict_route"]; present {
		t.Errorf("mixed inbound must not carry strict_route, got %v", in["strict_route"])
	}
}

// TestSystemProxyPortOverride pins the configurable port: an explicit MixedPort
// rides through to listen_port, while the tun-only fields (Stack/MTU) stay inert.
func TestSystemProxyPortOverride(t *testing.T) {
	cfg, err := Build(fakeNodes(), "", routing.Options{Mode: routing.ModeGlobal},
		TunOptions{Mode: ModeSystemProxy, MixedPort: 8890})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	in := cfg["inbounds"].([]map[string]any)[0]
	if in["type"] != "mixed" || in["listen_port"] != 8890 {
		t.Errorf("mixed listen_port = %v, want 8890 (type %v)", in["listen_port"], in["type"])
	}
}

// TestDefaultModeIsTun guards the default: an unset Mode normalizes to the tun
// inbound, so every existing caller (which passes no Mode) keeps its behaviour
// byte-for-byte.
func TestDefaultModeIsTun(t *testing.T) {
	cfg := buildFake(t)
	in := cfg["inbounds"].([]map[string]any)[0]
	if in["type"] != "tun" {
		t.Errorf("default inbound type = %v, want tun", in["type"])
	}
}

// TestValidMode pins the accepted mode names: the two known modes, and only
// those (the empty string is "use default", not a valid explicit value).
func TestValidMode(t *testing.T) {
	for _, m := range []string{ModeTun, ModeSystemProxy} {
		if !ValidMode(m) {
			t.Errorf("ValidMode(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"", "tunnel", "socks", "proxy"} {
		if ValidMode(m) {
			t.Errorf("ValidMode(%q) = true, want false", m)
		}
	}
}

// TestSystemProxyConfigPassesSingBoxCheck feeds a real `sing-box check` a full
// system-proxy config (mixed inbound + selector + real outbounds + the standard
// DNS/route/experimental blocks). This is the authoritative guard that the mixed
// inbound syntax matches the bundled sing-box 1.13 schema — an offline map
// assertion cannot catch a field the checker rejects. Skipped when no sing-box
// binary is available, like the sibling e2e checks.
func TestSystemProxyConfigPassesSingBoxCheck(t *testing.T) {
	bin, _, ok := findSingBox()
	if !ok {
		t.Skip("sing-box binary not found (resources/ or bin/ or PATH); skipping real config check")
	}

	// Global mode needs no geo rule-set files, so it always runs. The checker-clean
	// nodes avoid the fake-key REALITY/AmneziaWG entries the checker would reject.
	cfg, err := Build(checkNodes(), "", routing.Options{Mode: routing.ModeGlobal},
		TunOptions{Mode: ModeSystemProxy})
	if err != nil {
		t.Fatalf("build system-proxy config: %v", err)
	}
	t.Run("default_port", func(t *testing.T) { singBoxCheck(t, bin, cfg) })

	// An explicit non-default port must also decode cleanly.
	cfgPort, err := Build(checkNodes(), "", routing.Options{Mode: routing.ModeGlobal},
		TunOptions{Mode: ModeSystemProxy, MixedPort: 8890})
	if err != nil {
		t.Fatalf("build system-proxy config (custom port): %v", err)
	}
	t.Run("custom_port", func(t *testing.T) { singBoxCheck(t, bin, cfgPort) })
}
