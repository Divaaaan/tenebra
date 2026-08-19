package control

import "testing"

// TestConnectDecidesTheVoiceBlockForTheTunnelItIsRaising is an ordering bug that
// cost a live session.
//
// The bypass is raised on the way to the tunnel, before it exists. Asking the
// live state whether a tunnel is up therefore always answers "no" on that path,
// so the bundle's real-time-UDP block stayed in — and once the tunnel came up,
// WinDivert kept taking Discord's voice packets before the tunnel's router saw
// them and reinjecting them direct, where the voice servers answer nothing. The
// machine connected and then could not hold a call, with nothing in the log to
// say why.
//
// The connect path has to answer for the session it is about to create.
func TestConnectDecidesTheVoiceBlockForTheTunnelItIsRaising(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()

	if got := h.daemon.snapshotState().State; got == StateConnected {
		t.Fatalf("precondition: daemon should not be connected, got %q", got)
	}

	// What the connect path builds: a runner told the tunnel is coming.
	if r := h.daemon.newZapretRunnerFor(dir, true); !r.KeepVoiceInTunnel {
		t.Error("the connect path left the voice block in for a session that will have a tunnel")
	}
	// What every other caller builds: the live answer, which is "no tunnel".
	if r := h.daemon.newZapretRunner(dir); r.KeepVoiceInTunnel {
		t.Error("a bypass raised with no tunnel dropped the block that makes voice work direct")
	}
}
