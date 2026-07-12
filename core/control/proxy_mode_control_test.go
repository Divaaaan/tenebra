package control

import (
	"encoding/json"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
)

// These tests cover the set_proxy_mode command end to end at the protocol level:
// validating the mode/port, reporting and persisting the choice, driving the
// builder to emit the mixed inbound on connect, and hot-swapping a live tunnel
// (arming/clearing the OS proxy in step) — the same record/persist/reapply
// contract the kill switch and tun-stack toggles hold.

// firstInboundType returns the type of the config's single inbound, so a test can
// assert the mode reached the builder (tun vs mixed).
func firstInboundType(t *testing.T, cfgJSON []byte) string {
	t.Helper()
	var cfg struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if len(cfg.Inbounds) != 1 {
		t.Fatalf("inbounds = %d, want 1", len(cfg.Inbounds))
	}
	ty, _ := cfg.Inbounds[0]["type"].(string)
	return ty
}

// TestSetProxyModeValidation: an unknown mode or an out-of-range port is rejected
// whole; the two known modes (with or without a valid port) are accepted.
func TestSetProxyModeValidation(t *testing.T) {
	h := newHarness(t)
	h.useFakeProxy()

	cases := []struct {
		name    string
		req     Request
		wantErr bool
	}{
		{"unknown mode", Request{Cmd: CmdSetProxyMode, ProxyMode: "socks"}, true},
		{"empty mode", Request{Cmd: CmdSetProxyMode, ProxyMode: ""}, true},
		{"port too high", Request{Cmd: CmdSetProxyMode, ProxyMode: "system-proxy", ProxyPort: 70000}, true},
		{"negative port", Request{Cmd: CmdSetProxyMode, ProxyMode: "system-proxy", ProxyPort: -1}, true},
		{"tun", Request{Cmd: CmdSetProxyMode, ProxyMode: "tun"}, false},
		{"system-proxy default port", Request{Cmd: CmdSetProxyMode, ProxyMode: "system-proxy"}, false},
		{"system-proxy custom port", Request{Cmd: CmdSetProxyMode, ProxyMode: "system-proxy", ProxyPort: 8890}, false},
	}
	id := int64(1)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.req.ID = id
			id++
			h.send(c.req)
			r := h.await()
			if c.wantErr && r.Ok {
				t.Errorf("expected an error, got ok with data %s", r.Data)
			}
			if !c.wantErr && !r.Ok {
				t.Errorf("expected ok, got error %q", r.Error)
			}
		})
	}
}

// TestSetProxyModeReportsAndPersists: a valid switch echoes the mode and port in
// State and survives a restart through the settings file.
func TestSetProxyModeReportsAndPersists(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	h.useFakeProxy()
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetProxyMode, ProxyMode: "system-proxy", ProxyPort: 8890})
	var got State
	h.dataInto(h.await(), &got)
	if got.ProxyMode != "system-proxy" || got.ProxyPort != 8890 {
		t.Errorf("state = mode %q port %d, want system-proxy/8890", got.ProxyMode, got.ProxyPort)
	}

	h2 := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)

	loaded := h2.daemon.snapshotState()
	if loaded.ProxyMode != "system-proxy" || loaded.ProxyPort != 8890 {
		t.Errorf("proxy mode did not survive the restart: mode %q port %d", loaded.ProxyMode, loaded.ProxyPort)
	}
}

// TestSetProxyModeConnectBuildsMixedInbound: with system-proxy armed, the next
// connect's config carries a mixed inbound instead of the tun.
func TestSetProxyModeConnectBuildsMixedInbound(t *testing.T) {
	h := newHarness(t)
	h.useFakeProxy()
	p := h.addProfile([]model.Node{vlessNode("A", "a.example.aa")})

	h.send(Request{ID: 1, Cmd: CmdSetProxyMode, ProxyMode: "system-proxy"})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	if got := firstInboundType(t, h.runner.startCfgs()[0]); got != "mixed" {
		t.Errorf("inbound type = %q, want mixed", got)
	}
}

// TestConnectDefaultsToTunInbound: without touching the mode, a connect's config
// is the tun inbound — the mode switch must be opt-in.
func TestConnectDefaultsToTunInbound(t *testing.T) {
	h := newHarness(t)
	p := h.addProfile([]model.Node{vlessNode("A", "a.example.aa")})

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	if got := firstInboundType(t, h.runner.startCfgs()[0]); got != "tun" {
		t.Errorf("default inbound type = %q, want tun", got)
	}
}

// TestSetProxyModeLiveHotSwapArmsAndDisarms: switching modes on a live tunnel
// restarts sing-box with the new inbound and moves the OS proxy in step — armed
// when swapping to system-proxy, cleared when swapping back to tun — matching the
// kill switch's live re-apply.
func TestSetProxyModeLiveHotSwapArmsAndDisarms(t *testing.T) {
	h := newHarness(t)
	f := h.useFakeProxy()
	p := h.addProfile([]model.Node{vlessNode("A", "a.example.aa")})

	// Connect in the default tun mode: no proxy armed, tun inbound.
	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	if f.enables() != 0 {
		t.Fatalf("tun-mode connect armed the proxy (enables=%d)", f.enables())
	}

	// Switch to system-proxy while connected: hot-swap restarts sing-box with a
	// mixed inbound and arms the OS proxy once the swapped tunnel comes up.
	h.send(Request{ID: 2, Cmd: CmdSetProxyMode, ProxyMode: "system-proxy"})
	h.await()
	h.waitStarts(2)
	h.awaitLogContains("system proxy: OS now routing")
	if f.enables() != 1 {
		t.Errorf("enables = %d after swap to system-proxy, want 1", f.enables())
	}
	if got := firstInboundType(t, lastCfg(t, h)); got != "mixed" {
		t.Errorf("hot-swapped inbound type = %q, want mixed", got)
	}

	// Switch back to tun while connected: the teardown clears the OS proxy before
	// the tun tunnel comes up.
	h.send(Request{ID: 3, Cmd: CmdSetProxyMode, ProxyMode: "tun"})
	h.await()
	h.waitStarts(3)
	h.awaitLogContains("system proxy: cleared")
	if f.disables() < 1 {
		t.Errorf("switching back to tun did not clear the proxy (disables=%d)", f.disables())
	}
	if got := firstInboundType(t, lastCfg(t, h)); got != "tun" {
		t.Errorf("swapped-back inbound type = %q, want tun", got)
	}
}

// lastCfg returns the most recent config the fake runner was started with.
func lastCfg(t *testing.T, h *harness) []byte {
	t.Helper()
	cfgs := h.runner.startCfgs()
	if len(cfgs) == 0 {
		t.Fatal("no configs started yet")
	}
	return cfgs[len(cfgs)-1]
}
