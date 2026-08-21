package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Divaaaan/tenebra/core/routing"
)

func boolp(b bool) *bool { return &b }

// TestSetPresetsChangesOnlyWhatItNames: all three presets ship on, so a command
// that names one must leave the other two alone. Restating them on every change
// is how a user who turns off "unblock services" would silently lose the presets
// that keep games and voice off the tunnel — i.e. their ping.
func TestSetPresetsChangesOnlyWhatItNames(t *testing.T) {
	h := newHarness(t)

	h.send(Request{ID: 1, Cmd: CmdSetPresets, Games: boolp(false)})
	h.await()

	ro := h.daemon.snapshotRouting()
	if ro.GamesDirect {
		t.Error("games-direct stayed on after being switched off")
	}
	if !ro.VoiceDirect || !ro.UnblockServices {
		t.Errorf("an unnamed preset was changed: voice=%v services=%v", ro.VoiceDirect, ro.UnblockServices)
	}
}

// TestSetPresetsRejectsAnEmptyCommand: a command that names nothing is a caller
// bug, and answering "fine" to it would hide the bug behind a hot-swap that
// changes nothing.
func TestSetPresetsRejectsAnEmptyCommand(t *testing.T) {
	h := newHarness(t)

	h.send(Request{ID: 1, Cmd: CmdSetPresets})
	resp := h.await()
	if resp.Error == "" {
		t.Error("an empty set_presets was accepted")
	}
}

// TestPresetsSurviveARestart: the presets were previously set in the constructor
// and never written down, so switching one off lasted exactly until the next
// launch — a setting that silently reverts is worse than one that is missing.
func TestPresetsSurviveARestart(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetPresets, Games: boolp(false), Voice: boolp(false)})
	h.await()

	h2 := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)

	ro := h2.daemon.snapshotRouting()
	if ro.GamesDirect || ro.VoiceDirect {
		t.Errorf("presets came back on after a restart: games=%v voice=%v", ro.GamesDirect, ro.VoiceDirect)
	}
	if !ro.UnblockServices {
		t.Error("the preset that was never touched came back off")
	}
}

// TestPresetsDefaultOnForAnOldSettingsFile: a file written before these fields
// existed has none of them. Reading that as "off" would move games and voice
// back into the tunnel on the first launch after an upgrade — a silent
// regression in exactly the thing the app is for.
func TestPresetsDefaultOnForAnOldSettingsFile(t *testing.T) {
	dir := t.TempDir()
	// An old file as it actually looks on disk: version stamped, preset fields
	// absent entirely.
	old := []byte(`{"version":1,"split_mode":"off","kill_switch":true}`)
	if err := os.WriteFile(filepath.Join(dir, settingsFile), old, 0o644); err != nil {
		t.Fatalf("seed settings file: %v", err)
	}

	h := newHarness(t)
	h.daemon.SetSettings(settingsAt(t, dir))

	ro := h.daemon.snapshotRouting()
	if !ro.GamesDirect || !ro.VoiceDirect || !ro.UnblockServices {
		t.Errorf("an old settings file turned presets off: games=%v voice=%v services=%v",
			ro.GamesDirect, ro.VoiceDirect, ro.UnblockServices)
	}
}

// TestRoutingModeSurvivesARestart: set_routing used to record the mode in memory
// only. A user who chose global got smart back at the next launch, with the UI
// reporting the mode the daemon had reset to rather than the one they picked.
func TestRoutingModeSurvivesARestart(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetRouting, Mode: "global"})
	h.await()

	h2 := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)

	if got := h2.daemon.snapshotRouting().Mode; got != routing.ModeGlobal {
		t.Errorf("routing mode after restart = %q, want global", got)
	}
	if got := h2.daemon.snapshotState().Routing; got != string(routing.ModeGlobal) {
		t.Errorf("reported routing after restart = %q, want global", got)
	}
}

// TestSetRoutingRetunesALiveTunnel: the mode used to apply only on the next
// connect, which from the outside is a control that does nothing — the user
// flips it, nothing changes, and there is no way to tell that a reconnect was
// required.
func TestSetRoutingRetunesALiveTunnel(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	before := h.runner.starts()

	h.send(Request{ID: 2, Cmd: CmdSetRouting, Mode: "global"})
	h.await()
	// The swap dips through connecting and lands connected again on the same node.
	h.awaitState(StateConnected)

	if got := h.runner.starts(); got != before+1 {
		t.Errorf("sing-box started %d times, want %d (one hot swap)", got, before+1)
	}
}

// TestSetSplitRetunesALiveTunnel: same for the per-app split. Excluding an app
// while connected has to take that app out of the tunnel now.
func TestSetSplitRetunesALiveTunnel(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	before := h.runner.starts()

	h.send(Request{ID: 2, Cmd: CmdSetSplit, Mode: "exclude", Apps: []string{"chrome.exe"}})
	h.await()
	h.awaitState(StateConnected)

	if got := h.runner.starts(); got != before+1 {
		t.Errorf("sing-box started %d times, want %d (one hot swap)", got, before+1)
	}
}

// TestSetSplitDoesNotRetuneWhenNothingChanged: an idempotent re-send must not
// hot-swap sing-box. The swap dips the connection through connecting, so doing it
// for no reason is a visible stutter with nothing to show for it.
func TestSetSplitDoesNotRetuneWhenNothingChanged(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.send(Request{ID: 2, Cmd: CmdSetSplit, Mode: "exclude", Apps: []string{"chrome.exe"}})
	h.await()
	h.awaitState(StateConnected)

	// The same config again: nothing changed, so nothing may be swapped.
	h.send(Request{ID: 3, Cmd: CmdSetSplit, Mode: "exclude", Apps: []string{"chrome.exe"}})
	h.await()

	// A swap is asynchronous, so "no swap" cannot be observed by looking straight
	// away. Follow with a change that must swap and wait for it: if the duplicate
	// had swapped too, the count lands one higher than the three starts a connect
	// plus two real changes account for.
	h.send(Request{ID: 4, Cmd: CmdSetSplit, Mode: "exclude", Apps: []string{"chrome.exe", "steam.exe"}})
	h.await()
	h.awaitState(StateConnected)

	if got := h.runner.starts(); got != 3 {
		t.Errorf("sing-box started %d times, want 3 (connect + two real changes)", got)
	}
}
