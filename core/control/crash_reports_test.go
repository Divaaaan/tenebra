package control

import "testing"

// These tests cover core-owned crash-report consent end to end at the protocol
// level: the set_crash_reports command records, reports and persists the choice
// as a tri-state (absent until asked, then an explicit on/off that survives a
// restart), it coexists with the other persisted preferences, and — because the
// consent is a daemon-wide setting like autoconnect — it survives the state
// rebuild that a connect/disconnect drives.

// TestSetCrashReportsReportsAndPersists: the choice round-trips through the
// response, the status command, and — across a daemon restart — the settings
// file, in both directions, with "declined" (false) staying distinct from
// "not asked".
func TestSetCrashReportsReportsAndPersists(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	h.daemon.SetSettings(settingsAt(t, dir))

	h.send(Request{ID: 1, Cmd: CmdSetCrashReports, On: true})
	var st State
	h.dataInto(h.await(), &st)
	if st.CrashReports == nil || !*st.CrashReports || !st.CrashReportsAsked {
		t.Fatalf("enable: crash_reports=%v asked=%v, want (true, asked)", st.CrashReports, st.CrashReportsAsked)
	}

	h.send(Request{ID: 2, Cmd: CmdStatus})
	h.dataInto(h.await(), &st)
	if st.CrashReports == nil || !*st.CrashReports || !st.CrashReportsAsked {
		t.Fatalf("status after enable: crash_reports=%v asked=%v, want (true, asked)", st.CrashReports, st.CrashReportsAsked)
	}

	// A "restarted" daemon over the same directory loads the choice back.
	h2 := newHarness(t)
	h2.daemon.SetSettings(settingsAt(t, dir))
	if got := h2.daemon.snapshotState(); got.CrashReports == nil || !*got.CrashReports || !got.CrashReportsAsked {
		t.Errorf("enable did not survive the restart: crash_reports=%v asked=%v", got.CrashReports, got.CrashReportsAsked)
	}

	// Declining round-trips the same way and stays distinct from "not asked":
	// the value is present and false, and the asked bit stays set.
	h.send(Request{ID: 3, Cmd: CmdSetCrashReports, On: false})
	var off State
	h.dataInto(h.await(), &off)
	if off.CrashReports == nil || *off.CrashReports || !off.CrashReportsAsked {
		t.Fatalf("decline: crash_reports=%v asked=%v, want (false, asked)", off.CrashReports, off.CrashReportsAsked)
	}

	h3 := newHarness(t)
	h3.daemon.SetSettings(settingsAt(t, dir))
	if got := h3.daemon.snapshotState(); got.CrashReports == nil || *got.CrashReports || !got.CrashReportsAsked {
		t.Errorf("decline did not survive the restart: crash_reports=%v asked=%v", got.CrashReports, got.CrashReportsAsked)
	}
}

// TestCrashReportsUnaskedByDefault: a daemon that was never asked reports the
// consent as absent — nil value, asked=false — so the wire form omits both
// fields and the GUI shows its first-run prompt.
func TestCrashReportsUnaskedByDefault(t *testing.T) {
	h := newHarness(t)
	h.daemon.SetSettings(settingsAt(t, t.TempDir()))

	if got := h.daemon.snapshotState(); got.CrashReports != nil || got.CrashReportsAsked {
		t.Fatalf("fresh daemon: crash_reports=%v asked=%v, want (nil, not asked)", got.CrashReports, got.CrashReportsAsked)
	}

	h.send(Request{ID: 1, Cmd: CmdStatus})
	var st State
	h.dataInto(h.await(), &st)
	if st.CrashReports != nil || st.CrashReportsAsked {
		t.Errorf("status omits nothing: crash_reports=%v asked=%v, want (nil, not asked)", st.CrashReports, st.CrashReportsAsked)
	}
}

// TestSetCrashReportsSurvivesOtherSettingWrites: every settings write snapshots
// the full preference set, so a later kill-switch toggle must not clobber the
// stored crash-report choice (or vice versa).
func TestSetCrashReportsSurvivesOtherSettingWrites(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st := settingsAt(t, dir)
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetCrashReports, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: true})
	h.await()

	ps := st.Load()
	if ps.CrashReports == nil || !*ps.CrashReports {
		t.Errorf("set_kill_switch write clobbered the persisted crash-report choice: %v", ps.CrashReports)
	}
	if !ps.KillSwitch {
		t.Error("kill switch missing from the persisted settings")
	}
}

// TestCrashReportsSurviveConnectCycle: the consent is a daemon-wide preference,
// so the State rebuild that a connect (and the following disconnect) performs
// must re-project it rather than drop it — the reason it threads through
// applySettingsToState alongside autoconnect.
func TestCrashReportsSurviveConnectCycle(t *testing.T) {
	h := newHarness(t)
	h.daemon.SetSettings(settingsAt(t, t.TempDir()))
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetCrashReports, On: true})
	h.await()

	// Connect then disconnect: both transitions rebuild the reported State via
	// setState, which re-projects the daemon-wide preferences. The state event
	// itself carries only state/node/error (like autoconnect, the consent is a
	// status field, not an event one), so the check is that a status after the
	// round trip still reports the consent rather than having dropped it in a
	// rebuild.
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	h.send(Request{ID: 3, Cmd: CmdDisconnect})
	h.await()

	h.send(Request{ID: 4, Cmd: CmdStatus})
	var st State
	h.dataInto(h.await(), &st)
	if st.CrashReports == nil || !*st.CrashReports || !st.CrashReportsAsked {
		t.Errorf("crash consent lost across a connect/disconnect: crash_reports=%v asked=%v", st.CrashReports, st.CrashReportsAsked)
	}
}
