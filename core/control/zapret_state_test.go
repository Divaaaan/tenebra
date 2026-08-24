package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Divaaaan/tenebra/core/zapret"
)

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

// A state transition must not blank the bypass. setState replaces the whole
// State value, and every connect ends with one — after the bypass has already
// been raised for that same connect. The answer the app files away is the
// snapshot taken there, so without the bypass fields being carried onto it, the
// app was told no filter existed on a machine that had just started one, and
// nothing refreshed the answer for the rest of the session: status re-reads the
// same snapshot, state events carry phase/node/error only, and the verifier
// refreshes on the failure paths alone.
func TestZapretStateSurvivesStateTransition(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.SetSettings(settingsAt(t, t.TempDir()))

	dir := filepath.Join(d.store.Dir(), zapretDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make bundle dir: %v", err)
	}
	if err := zapret.WriteVersion(dir, "1.10.2"); err != nil {
		t.Fatalf("write bundle version: %v", err)
	}

	d.applyZapretState(true, "general (FAKE TLS AUTO)")

	before := d.snapshotState()
	if !before.ZapretActive || before.ZapretStrategy != "general (FAKE TLS AUTO)" {
		t.Fatalf("bypass not reported after it came up: %+v", before)
	}
	if before.ZapretVersion != "1.10.2" || !before.ZapretAutoUpdate {
		t.Fatalf("bundle version or updater not reported after it came up: %+v", before)
	}

	d.setState(State{State: StateConnecting})

	got := d.snapshotState()
	if got.State != StateConnecting {
		t.Fatalf("state = %q, want %q", got.State, StateConnecting)
	}
	if !got.ZapretActive {
		t.Error("the transition reported the running bypass as stopped")
	}
	if got.ZapretStrategy != before.ZapretStrategy {
		t.Errorf("strategy = %q, want %q", got.ZapretStrategy, before.ZapretStrategy)
	}
	if got.ZapretVersion != before.ZapretVersion {
		t.Errorf("bundle version = %q, want %q", got.ZapretVersion, before.ZapretVersion)
	}
	if got.ZapretAutoUpdate != before.ZapretAutoUpdate {
		t.Errorf("auto-update = %v, want %v", got.ZapretAutoUpdate, before.ZapretAutoUpdate)
	}
}
