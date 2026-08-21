package control

import "testing"

// The strategy is chosen by a measurement that takes minutes; discarding it at
// every restart would mean the app quietly launches the bundle's alphabetical
// default instead — on the author's ISP a strategy that scored worse than the
// one the probe picked, with nothing on screen to say so.
func TestPickedZapretStrategySurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	d, _ := newTestDaemon(t)
	d.SetSettings(settingsAt(t, dir))
	d.applyZapretState(true, "general (FAKE TLS AUTO)")

	d2, _ := newTestDaemon(t)
	d2.SetSettings(settingsAt(t, dir))

	d2.mu.Lock()
	got := d2.zapretActive
	running := d2.routing.ZapretActive
	d2.mu.Unlock()

	if got != "general (FAKE TLS AUTO)" {
		t.Errorf("restored strategy = %q, want the picked one", got)
	}
	// Loading restores the choice, never the running state: nothing was launched.
	if running {
		t.Error("loading settings marked the bypass as running without starting it")
	}
}

// Stopping the bypass has to move the routing with it. While the flag stays set,
// YouTube and Discord are pinned to the direct path with nothing carrying them
// through the censor — a connected VPN and dead video, which is a worse state
// than either the bypass or the tunnel alone.
func TestStopZapretReturnsRoutingToTheTunnel(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.SetSettings(settingsAt(t, t.TempDir()))

	d.applyZapretState(true, "general")
	d.mu.Lock()
	up := d.routing.ZapretActive
	d.mu.Unlock()
	if !up {
		t.Fatal("routing was not told the bypass came up")
	}

	d.applyZapretState(false, "")

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.routing.ZapretActive {
		t.Error("routing still believes the bypass is running after it stopped")
	}
	if len(d.routing.ZapretCovered) != 0 {
		t.Error("stale bypass coverage survived the stop")
	}
	// The choice is kept: stopping is not un-picking, and a later start with no
	// name must bring back the measured strategy rather than the bundle default.
	if d.zapretActive != "general" {
		t.Errorf("picked strategy = %q, want it kept across a stop", d.zapretActive)
	}
}
