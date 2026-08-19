package control

import (
	"encoding/json"
	"errors"
	"net"
	"testing"
)

// TestFailedReapplyReportsTheTunnelDown is the lie this fixes.
//
// A live re-apply stops the running sing-box before starting the replacement, so
// when the replacement fails the tunnel is gone. The daemon used to log "the
// change applies on the next connect" and leave the state at connected: the app
// showed a healthy connection, the user's traffic went nowhere, and every
// diagnosis after that started from a false premise. Observed on a real machine,
// where it held a green light for hours over an adapter that no longer existed.
func TestFailedReapplyReportsTheTunnelDown(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	// The next start fails — the shape of a tun that cannot configure itself.
	h.runner.mu.Lock()
	h.runner.startErr = errors.New("configure tun interface: The object already exists")
	h.runner.mu.Unlock()

	// Any setting that re-applies in place will do; the kill switch is the oldest.
	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: true})
	h.await()

	ev := h.awaitState(StateError)
	msg, _ := ev["error"].(string)
	if msg == "" {
		t.Fatal("error state carries no reason")
	}
	if got := h.daemon.snapshotState().State; got != StateError {
		t.Errorf("state = %q after a failed re-apply, want error", got)
	}
}

// TestReapplyMovesToAFreeTunAddress guards the twenty-second death.
//
// A hot-swap starts the replacement while the outgoing adapter is still tearing
// down and still holding its address. Reusing that address makes the new process
// fail to configure its interface, which takes the tunnel with it — a connection
// that came up, worked, and vanished twenty seconds later with no explanation.
func TestReapplyMovesToAFreeTunAddress(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	first := tunAddressOf(t, h.runner.startCfgs()[0])
	if first == "" {
		t.Fatal("the first config carries no tun address")
	}

	// The dying adapter still holds it, which is exactly the state a swap runs in.
	h.daemon.localAddrs = func() []net.Addr { return []net.Addr{addr(t, first)} }

	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.awaitState(StateConnected)

	cfgs := h.runner.startCfgs()
	second := tunAddressOf(t, cfgs[len(cfgs)-1])
	if second == first {
		t.Errorf("the swap reused %s while it was still held", first)
	}
}

// tunAddressOf digs the IPv4 tun address out of a rendered config.
func tunAddressOf(t *testing.T, cfg []byte) string {
	t.Helper()
	var c struct {
		Inbounds []struct {
			Type    string   `json:"type"`
			Address []string `json:"address"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(cfg, &c); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	for _, in := range c.Inbounds {
		if in.Type == "tun" && len(in.Address) > 0 {
			return in.Address[0]
		}
	}
	return ""
}

// TestSuccessfulReapplyStaysConnected: the ordinary path must not be disturbed by
// the above — a swap that works ends connected, on the same node.
func TestSuccessfulReapplyStaysConnected(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: true})
	h.await()

	again := h.awaitState(StateConnected)
	if again["node"] != connected["node"] {
		t.Errorf("re-apply moved the session to %v, want the same node %v", again["node"], connected["node"])
	}
}
