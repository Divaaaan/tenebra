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

// TestPresetDefaultsForAnOldSettingsFile: a v1 file with the preset fields absent
// entirely (a partial or hand-edited file, not the full one 0.5.0 wrote). The two
// outward presets have to end up off and unblock-services on, per preset, not by
// one blanket answer. For a v1 file both routes to "off" meet: the v1->v2
// migration forces games/voice to an explicit false, and the absent-field default
// would read them off anyway; unblock-services, which the migration leaves alone,
// falls to its absent default of on.
func TestPresetDefaultsForAnOldSettingsFile(t *testing.T) {
	dir := t.TempDir()
	// A v1 file with the preset fields absent entirely.
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

// TestV1StoredPresetsAreClearedByMigration: 0.5.0 (the only v1 writer) shipped
// games-direct and voice-direct on and persisted that `true`, but gave no UI,
// wire command or reported state to see or change them — so the stored value was
// a default the user was never shown, not a choice. The v1->v2 migration clears
// exactly those two, closing on an upgraded install the same leak the new
// defaults close on a fresh one. It must not touch anything else the file holds:
// unblock-services (which only pins domains into the tunnel) and every conscious
// setting — here the kill switch and the split config — are the user's and must
// survive verbatim.
func TestV1StoredPresetsAreClearedByMigration(t *testing.T) {
	dir := t.TempDir()
	stored := []byte(`{"version":1,"preset_games_direct":true,"preset_voice_direct":true,` +
		`"preset_unblock_services":false,"kill_switch":true,"split_mode":"exclude",` +
		`"split_apps":["chrome.exe"]}`)
	if err := os.WriteFile(filepath.Join(dir, settingsFile), stored, 0o644); err != nil {
		t.Fatalf("seed settings file: %v", err)
	}

	h := newHarness(t)
	h.daemon.SetSettings(settingsAt(t, dir))

	ro := h.daemon.snapshotRouting()
	if ro.GamesDirect || ro.VoiceDirect {
		t.Errorf("a v1 stored true survived the migration: games=%v voice=%v",
			ro.GamesDirect, ro.VoiceDirect)
	}
	// The migration must leave the rest of the file alone.
	if ro.UnblockServices {
		t.Error("the migration flipped unblock-services, which it must not touch")
	}
	if !ro.KillSwitch {
		t.Error("the migration dropped the user's kill-switch choice")
	}
	if ro.SplitMode != routing.SplitExclude ||
		len(ro.SplitApps) != 1 || ro.SplitApps[0] != "chrome.exe" {
		t.Errorf("the migration mangled the split config: mode=%q apps=%v",
			ro.SplitMode, ro.SplitApps)
	}
}

// TestV2StoredPresetChoicesAreKept: once the file is at v2 the presets are things
// the user could actually see and toggle, so a stored value is a real choice and
// is honoured as written — in both directions. The migration is a one-time reset
// of the v1 non-choice, not a standing override; re-deciding a v2 choice on every
// load would be the same silent change the fix set out to remove, just later.
func TestV2StoredPresetChoicesAreKept(t *testing.T) {
	dir := t.TempDir()
	stored := []byte(`{"version":2,"preset_games_direct":true,"preset_voice_direct":true,` +
		`"preset_unblock_services":false}`)
	if err := os.WriteFile(filepath.Join(dir, settingsFile), stored, 0o644); err != nil {
		t.Fatalf("seed settings file: %v", err)
	}

	h := newHarness(t)
	h.daemon.SetSettings(settingsAt(t, dir))

	ro := h.daemon.snapshotRouting()
	if !ro.GamesDirect || !ro.VoiceDirect {
		t.Errorf("an explicit v2 stored true was overridden: games=%v voice=%v",
			ro.GamesDirect, ro.VoiceDirect)
	}
	if ro.UnblockServices {
		t.Error("an explicit v2 stored false was overridden")
	}
}

// TestUpgradedPresetChoiceSticksAfterMigration: the migration clears the v1
// non-choice once, then gets out of the way. A user who turns a preset back on
// after upgrading writes a v2 file, and that choice must survive the next
// restart rather than being re-cleared — otherwise the switch 0.5.0 never had
// would still not work.
func TestUpgradedPresetChoiceSticksAfterMigration(t *testing.T) {
	dir := t.TempDir()
	stored := []byte(`{"version":1,"preset_games_direct":true,"preset_voice_direct":true}`)
	if err := os.WriteFile(filepath.Join(dir, settingsFile), stored, 0o644); err != nil {
		t.Fatalf("seed settings file: %v", err)
	}

	// First launch after the upgrade: the migration clears both.
	h := newHarness(t)
	h.daemon.SetSettings(settingsAt(t, dir))
	if ro := h.daemon.snapshotRouting(); ro.GamesDirect || ro.VoiceDirect {
		t.Fatalf("migration did not clear the v1 presets: games=%v voice=%v",
			ro.GamesDirect, ro.VoiceDirect)
	}

	// The user turns voice-direct on through the switch the upgrade gave them; this
	// persists a v2 file.
	h.send(Request{ID: 1, Cmd: CmdSetPresets, Voice: boolp(true)})
	if resp := h.await(); resp.Error != "" {
		t.Fatalf("set_presets: %v", resp.Error)
	}

	// Next restart: the v2 choice must be honoured, not re-cleared by the migration.
	h2 := newHarness(t)
	h2.daemon.SetSettings(settingsAt(t, dir))
	ro := h2.daemon.snapshotRouting()
	if !ro.VoiceDirect {
		t.Error("a preset turned on after the upgrade was re-cleared on the next launch")
	}
	if ro.GamesDirect {
		t.Error("a preset left off after the upgrade came back on")
	}
}

// TestPresetsAreReportedInState: the presets existed only as a Go-side command —
// no field in the snapshot, so no interface could show them, and the one way to
// change them was to hand-edit the settings file. A switch nobody can see is a
// switch nobody can turn off.
func TestPresetsAreReportedInState(t *testing.T) {
	h := newHarness(t)
	// Load through the store, the way cmd/tenebra-core/main.go does. The reported
	// state is filled by applySettingsToState from the live routing options; the
	// builder's State literal leaves the preset fields at their zero value. Without
	// this call the default-state check below would pass on that zero value rather
	// than on what a running core actually reports — and would in particular miss
	// unblock-services, which the literal leaves false while the routing default
	// has it on. An empty temp dir gives the pure defaults.
	h.daemon.SetSettings(settingsAt(t, t.TempDir()))

	st := h.daemon.snapshotState()
	if st.PresetGamesDirect || st.PresetVoiceDirect {
		t.Errorf("state reports a leaking preset on by default: games=%v voice=%v",
			st.PresetGamesDirect, st.PresetVoiceDirect)
	}
	// The default-on preset has to be reported on, not merely absent: this is what
	// proves the state came through applySettingsToState and not the zero literal.
	if !st.PresetUnblockServices {
		t.Error("state does not report unblock-services on by default")
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

	st = h.daemon.snapshotState()
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
