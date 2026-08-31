package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Divaaaan/tenebra/core/zapret"
)

// The bypass switch has to be obeyed, and on the connect path it was not.
//
// Connecting raised the bypass unconditionally — the "one button does the whole
// job" rule, applied to a user who had already answered the question by hand. The
// switch read off, the connect started the packet filter anyway, and so did the
// next one and the one after that. A missing bundle was all that had been hiding
// it: the raise needs one, and a machine that had never fetched a bundle had
// nothing to start. 0.5.10 put a bundle inside the binary and lays it down at
// every daemon start, which turned that into what a user actually reported — a
// packet filter that switches itself on at every connection, with the switch off.
//
// The rule these pin down is one sentence with three cases: nobody has touched
// the switch (nil) and the button decides, the user turned it on (true) and it
// comes back, the user turned it off (false) and nothing raises it until they say
// otherwise. The start-up side of the same matrix lives in zapret_persist_test.go;
// this file is the connect side, plus the switch going off while a filter runs.

// turnTheBypassOff presses the switch the other way, the way the UI does it:
// stop_zapret, which is what the toggle sends when it goes off.
func turnTheBypassOff(t *testing.T, d *Daemon) {
	t.Helper()
	if resp := d.handleStopZapret(context.Background(), Request{ID: 2, Cmd: CmdStopZapret}); !resp.Ok {
		t.Fatalf("stop_zapret: %s", resp.Error)
	}
}

// storedBypassChoice reads the switch back out of the settings file as the
// three-state answer it is: nil when the file records none. Read from disk rather
// than from the daemon's field, because the file is what the next launch obeys.
func storedBypassChoice(t *testing.T, settingsDir string) *bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(settingsDir, settingsFile))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var stored struct {
		ZapretEnabled *bool `json:"zapret_enabled"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	return stored.ZapretEnabled
}

// describeChoice renders the switch for a failure message, where the pointer
// itself would print an address.
func describeChoice(c *bool) string {
	switch {
	case c == nil:
		return "never touched"
	case *c:
		return "on"
	default:
		return "off"
	}
}

// Nobody has touched the switch, so the button still decides: a connect raises
// the bypass, exactly as it always has. This is the case the one-button product
// rests on, and the fix must not cost it.
func TestAConnectRaisesTheBypassForAUserWhoNeverTouchedTheSwitch(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")

	d, raised := launchBypassDaemon(t, store, settings)
	if !d.raiseZapretForConnect(context.Background()) {
		t.Fatal("a connect raised no bypass on a machine whose owner never touched the switch: the one button no longer does the whole job")
	}
	if got := raised.names(); len(got) != 1 {
		t.Fatalf("the connect launched %v, want one strategy", got)
	}
	if !d.snapshotRouting().ZapretActive {
		t.Error("the bypass came up and routing was not told, so the censored services stay in the tunnel it is carrying")
	}
	// And still nothing written down: a connect is not an answer to the switch.
	if got := storedBypassChoice(t, settings); got != nil {
		t.Errorf("the connect recorded the switch as %s; nobody pressed it", describeChoice(got))
	}
}

// The user turned it on, so a connect raises it — on the strategy they chose,
// read back off disk by a daemon that has just started.
func TestAConnectRaisesTheBypassTheUserSwitchedOn(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")

	d, _ := launchBypassDaemon(t, store, settings)
	turnTheBypassOn(t, d, "general (FAKE TLS AUTO)")

	// A fresh daemon over the same machine, with no background job run: the connect
	// is then the only thing that can have raised anything.
	d2, raised := launchBypassDaemon(t, store, settings)
	if !d2.raiseZapretForConnect(context.Background()) {
		t.Fatal("a connect raised no bypass for a user who had switched it on")
	}
	got := raised.names()
	if len(got) != 1 || got[0] != "general (FAKE TLS AUTO)" {
		t.Fatalf("the connect launched %v, want the strategy the user switched on", got)
	}
	if !d2.snapshotRouting().ZapretActive {
		t.Error("the bypass came up and routing was not told")
	}
	if stored := storedBypassChoice(t, settings); stored == nil || !*stored {
		t.Errorf("the stored switch = %s, want the on the user pressed", describeChoice(stored))
	}
}

// The bug: the user switched the bypass off, and the next connect started it
// again. Nothing may raise a packet filter the user has switched off — not the
// connect, and not the routing that follows it.
func TestAConnectDoesNotRaiseTheBypassTheUserSwitchedOff(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")

	d, started := launchBypassDaemon(t, store, settings)
	turnTheBypassOn(t, d, "general (FAKE TLS AUTO)")
	turnTheBypassOff(t, d)

	if d.raiseZapretForConnect(context.Background()) {
		t.Fatal("the connect raised the bypass the user had switched off")
	}
	// One launch for the whole test: the one the user asked for by hand.
	if got := started.names(); len(got) != 1 {
		t.Errorf("the bypass was launched %v, want only the one the user pressed", got)
	}
	r := d.snapshotRouting()
	if r.ZapretActive {
		t.Error("routing believes a bypass is running that the user switched off")
	}
	if len(r.ZapretCovered) != 0 {
		t.Error("stale coverage survived the connect: the censored services are pinned to a direct path with nothing carrying them")
	}
	if st := d.snapshotState(); st.ZapretActive {
		t.Error("the reported state claims a bypass the user switched off, so the switch would spring back on in the UI")
	}
}

// The report itself, end to end: switched off, then a restart with the bundle
// already on the machine, then connecting again and again. The filter must stay
// down through all of it.
func TestTheBypassStaysOffAcrossARestartAndEveryConnect(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")

	d, _ := launchBypassDaemon(t, store, settings)
	turnTheBypassOn(t, d, "general (FAKE TLS AUTO)")
	turnTheBypassOff(t, d)

	d2, restarted := launchBypassDaemon(t, store, settings)
	d2.RunZapretAutoUpdate(daemonStart())
	if got := restarted.names(); len(got) != 0 {
		t.Fatalf("the restart started %v after the user switched the bypass off", got)
	}

	// Every connect, not just the first: what was reported is a filter that came
	// back on every single connection.
	for i := 1; i <= 3; i++ {
		if d2.raiseZapretForConnect(context.Background()) {
			t.Fatalf("connect %d raised the bypass the user switched off", i)
		}
	}
	if got := restarted.names(); len(got) != 0 {
		t.Errorf("three connects launched %v on a machine whose owner switched the bypass off", got)
	}
	if d2.snapshotRouting().ZapretActive {
		t.Error("routing believes a bypass is running that the user switched off")
	}
	if stored := storedBypassChoice(t, settings); stored == nil || *stored {
		t.Errorf("the stored switch = %s after a restart and three connects, want the off the user pressed", describeChoice(stored))
	}
}

// Switching the bypass off while it is running has to do all three things:
// actually stop the filter, take the censored services back into the tunnel, and
// write the answer down. The third is what the next connect and the next launch
// read — an off that is not recorded is a switch that springs back on.
func TestSwitchingTheBypassOffStopsItAndRecordsTheChoice(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")

	d, _ := launchBypassDaemon(t, store, settings)
	turnTheBypassOn(t, d, "general (FAKE TLS AUTO)")
	if !d.snapshotRouting().ZapretActive {
		t.Fatal("the switch did not bring the bypass up; the test proves nothing")
	}

	turnTheBypassOff(t, d)

	r := d.snapshotRouting()
	if r.ZapretActive {
		t.Error("the switch went off and routing still believes a filter is carrying the censored services")
	}
	if len(r.ZapretCovered) != 0 {
		t.Error("stale coverage survived the stop")
	}
	if stored := storedBypassChoice(t, settings); stored == nil || *stored {
		t.Errorf("the stored switch = %s, want off written down", describeChoice(stored))
	}
	// The measured strategy is kept: switching off is not un-picking, and a later
	// start with no name has to bring back the one that was chosen.
	d.mu.Lock()
	strategy := d.zapretActive
	d.mu.Unlock()
	if strategy != "general (FAKE TLS AUTO)" {
		t.Errorf("picked strategy = %q, want it kept across the switch going off", strategy)
	}
}

// A bundle update must not raise a bypass the user switched off either. It puts
// back whatever was RUNNING across the swap, which is the right question to ask —
// but it is worth pinning, because the update is the one path that starts the
// filter without anybody having connected or pressed anything.
func TestABundleUpdateDoesNotRaiseTheBypassTheUserSwitchedOff(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")
	dir := filepath.Join(store, zapretDirName)
	// Older than the pinned release the stub publishes below, so there is an update
	// to install at all.
	if err := zapret.WriteVersion(dir, "1.10.0"); err != nil {
		t.Fatalf("write bundle version: %v", err)
	}

	d, started := launchBypassDaemon(t, store, settings)
	turnTheBypassOn(t, d, "general (FAKE TLS AUTO)")
	turnTheBypassOff(t, d)

	d.zapretLatest = func(context.Context) (zapret.Release, error) {
		return zapret.Release{Version: "1.10.2", ArchiveURL: "https://example.invalid/b.zip"}, nil
	}
	d.zapretApply = func(_ context.Context, target string, rel zapret.Release) error {
		seedBundleAt(t, target, rel.Version)
		return nil
	}

	if _, _, updated, err := d.updateZapret(context.Background()); err != nil || !updated {
		t.Fatalf("updateZapret: updated=%v err=%v; the test proves nothing without an update", updated, err)
	}

	if got := started.names(); len(got) != 1 {
		t.Errorf("the bypass was launched %v across the update, want only the one the user pressed before switching it off", got)
	}
	if d.snapshotRouting().ZapretActive {
		t.Error("the update left routing believing a bypass is running that the user switched off")
	}
}

// A stop that fails is still the user answering off. The one way Runner.Stop
// fails on Windows is a dead context — the client tore the connection down
// mid-command — and before this was pinned, the error return skipped recording
// the wish, so the very next connect raised the filter the user had just
// switched off. The wish must survive the failure; the error must still reach
// the caller, because the filter itself may well be alive.
func TestAFailedStopStillRecordsTheSwitchGoingOff(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")

	d, _ := launchBypassDaemon(t, store, settings)
	turnTheBypassOn(t, d, "general (FAKE TLS AUTO)")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	resp := d.handleStopZapret(cancelled, Request{ID: 3, Cmd: CmdStopZapret})
	if resp.Ok {
		// A stub runner that shrugs at a dead context would leave this test
		// asserting nothing about the error path.
		t.Skip("the runner stopped despite the cancelled context; the failure path is not reachable here")
	}

	if stored := storedBypassChoice(t, settings); stored == nil || *stored {
		t.Errorf("the stored switch = %s after a failed stop, want off written down", describeChoice(stored))
	}
	// And the next automatic raise obeys it: the recorded off is the whole point.
	if d.autoStartZapret(context.Background(), true) {
		t.Error("a connect after the failed stop raised the bypass the user switched off")
	}
}
