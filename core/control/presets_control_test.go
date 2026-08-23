package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Divaaaan/tenebra/core/routing"
)

func boolp(b bool) *bool { return &b }

// TestSetPresetsChangesOnlyWhatItNames: a command that names one preset must
// leave the other two alone, in both directions. Restating all three on every
// change is how a UI that flips one switch silently moves the others.
func TestSetPresetsChangesOnlyWhatItNames(t *testing.T) {
	h := newHarness(t)

	h.send(Request{ID: 1, Cmd: CmdSetPresets, Games: boolp(true)})
	h.await()

	ro := h.daemon.snapshotRouting()
	if !ro.GamesDirect {
		t.Error("games-direct stayed off after being switched on")
	}
	if ro.VoiceDirect {
		t.Error("voice-direct was switched on by a command that did not name it")
	}
	if !ro.UnblockServices {
		t.Error("unblock-services was switched off by a command that did not name it")
	}

	h.send(Request{ID: 2, Cmd: CmdSetPresets, Services: boolp(false)})
	h.await()

	ro = h.daemon.snapshotRouting()
	if ro.UnblockServices {
		t.Error("unblock-services stayed on after being switched off")
	}
	if !ro.GamesDirect {
		t.Error("games-direct was switched off by a command that did not name it")
	}
}

// TestTrafficLeakingPresetsAreOffByDefault: GamesDirect and VoiceDirect each send
// a whole class of traffic around the tunnel — every game client's connections,
// and all UDP above port 50000, which is where browser WebRTC calls and torrents
// live. Shipping them on means a fresh install hands the user's real address to
// whoever is on the other end of a call while the app reports itself connected,
// without the user having chosen anything. UnblockServices is the opposite shape:
// it pins censored domains *to* the tunnel, so it stays on.
func TestTrafficLeakingPresetsAreOffByDefault(t *testing.T) {
	h := newHarness(t)

	ro := h.daemon.snapshotRouting()
	if ro.GamesDirect {
		t.Error("games-direct is on in a fresh daemon")
	}
	if ro.VoiceDirect {
		t.Error("voice-direct is on in a fresh daemon")
	}
	if !ro.UnblockServices {
		t.Error("unblock-services is off in a fresh daemon")
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
// and never written down, so changing one lasted exactly until the next launch —
// a setting that silently reverts is worse than one that is missing.
func TestPresetsSurviveARestart(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetPresets, Games: boolp(true), Services: boolp(false)})
	h.await()

	h2 := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h2.daemon.SetSettings(st2)

	ro := h2.daemon.snapshotRouting()
	if !ro.GamesDirect {
		t.Error("a preset switched on came back off after a restart")
	}
	if ro.UnblockServices {
		t.Error("a preset switched off came back on after a restart")
	}
	if ro.VoiceDirect {
		t.Error("the preset that was never touched came back on")
	}
}

// TestPresetDefaultsForAnOldSettingsFile: a file written before these fields
// existed has none of them, and so does one written by a version that only ever
// wrote the two it had. Absent has to read as the safe value per preset, not one
// blanket answer: off for the two that route traffic out of the tunnel, on for
// the one that routes censored domains into it.
func TestPresetDefaultsForAnOldSettingsFile(t *testing.T) {
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
	if ro.GamesDirect || ro.VoiceDirect {
		t.Errorf("an old settings file left a traffic-leaking preset on: games=%v voice=%v",
			ro.GamesDirect, ro.VoiceDirect)
	}
	if !ro.UnblockServices {
		t.Error("an old settings file turned unblock-services off")
	}
}

// TestStoredPresetChoicesAreKept: 0.5.0 shipped all three presets on and wrote
// that down, so an upgraded install has `true` on disk for presets its owner may
// well have wanted. An explicit stored value is the user's, not a default to
// re-decide: flipping it on upgrade would be the same silent change in the other
// direction. Turning them off is the UI's job now that it has the switches.
func TestStoredPresetChoicesAreKept(t *testing.T) {
	dir := t.TempDir()
	stored := []byte(`{"version":1,"preset_games_direct":true,"preset_voice_direct":true,` +
		`"preset_unblock_services":false}`)
	if err := os.WriteFile(filepath.Join(dir, settingsFile), stored, 0o644); err != nil {
		t.Fatalf("seed settings file: %v", err)
	}

	h := newHarness(t)
	h.daemon.SetSettings(settingsAt(t, dir))

	ro := h.daemon.snapshotRouting()
	if !ro.GamesDirect || !ro.VoiceDirect {
		t.Errorf("an explicit stored true was overridden: games=%v voice=%v",
			ro.GamesDirect, ro.VoiceDirect)
	}
	if ro.UnblockServices {
		t.Error("an explicit stored false was overridden")
	}
}

// TestPresetsAreReportedInState: the presets existed only as a Go-side command —
// no field in the snapshot, so no interface could show them, and the one way to
// change them was to hand-edit the settings file. A switch nobody can see is a
// switch nobody can turn off.
func TestPresetsAreReportedInState(t *testing.T) {
	h := newHarness(t)

	if st := h.daemon.snapshotState(); st.PresetGamesDirect || st.PresetVoiceDirect {
		t.Errorf("state reports a leaking preset on by default: games=%v voice=%v",
			st.PresetGamesDirect, st.PresetVoiceDirect)
	}

	h.send(Request{ID: 1, Cmd: CmdSetPresets, Games: boolp(true), Voice: boolp(true), Services: boolp(false)})
	resp := h.await()
	if resp.Error != "" {
		t.Fatalf("set_presets: %v", resp.Error)
	}

	var got State
	if err := json.Unmarshal(resp.Data, &got); err != nil {
		t.Fatalf("decode set_presets state: %v", err)
	}
	if !got.PresetGamesDirect || !got.PresetVoiceDirect || got.PresetUnblockServices {
		t.Errorf("set_presets answered with the wrong state: games=%v voice=%v services=%v",
			got.PresetGamesDirect, got.PresetVoiceDirect, got.PresetUnblockServices)
	}

	st := h.daemon.snapshotState()
	if !st.PresetGamesDirect || !st.PresetVoiceDirect || st.PresetUnblockServices {
		t.Errorf("status does not report the presets: games=%v voice=%v services=%v",
			st.PresetGamesDirect, st.PresetVoiceDirect, st.PresetUnblockServices)
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
