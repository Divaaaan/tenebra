package control

import (
	"context"
	"testing"
	"time"
)

// TestFailedBypassCheckLeavesTheTunnelAlone is the wreck this prevents.
//
// The check used to rebuild the live tunnel when the bypass looked broken:
// stop sing-box, start a replacement with the new routing. On a real machine
// that replacement did not come up — twenty seconds after a healthy connect the
// adapter was gone, the process was gone, and the app still reported connected.
// The bypass verdict is an optimisation (direct versus through the node, i.e.
// latency and ads); the tunnel is the product. An optimisation must never be
// allowed to take the product down with it.
func TestFailedBypassCheckLeavesTheTunnelAlone(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	startsBefore := h.runner.starts()
	stopsBefore := h.runner.stops()

	// The state a connect leaves behind when the bypass came up, plus a check
	// that always says "video does not survive".
	h.daemon.mu.Lock()
	h.daemon.routing.ZapretActive = true
	h.daemon.routing.ZapretCovered = []string{"youtube.com", "discord.com"}
	h.daemon.mu.Unlock()
	h.daemon.bypassVerifyDelay = time.Millisecond
	h.daemon.bypassProbe = func(context.Context) bool { return false }

	h.daemon.verifyBypass(context.Background(), h.daemon.currentGeneration())

	// The verdict is recorded — that part is the point of the check.
	if h.daemon.snapshotRouting().ZapretActive {
		t.Error("a bypass that carries nothing is still credited with these services")
	}
	// But the tunnel is untouched: no restart, no teardown, still connected.
	if got := h.runner.starts(); got != startsBefore {
		t.Errorf("the tunnel was restarted (%d -> %d starts) for a routing decision", startsBefore, got)
	}
	if got := h.runner.stops(); got != stopsBefore {
		t.Errorf("the tunnel was stopped (%d -> %d stops) for a routing decision", stopsBefore, got)
	}
	if got := h.daemon.snapshotState().State; got != StateConnected {
		t.Errorf("state = %q after a failed bypass check, want connected", got)
	}
}
