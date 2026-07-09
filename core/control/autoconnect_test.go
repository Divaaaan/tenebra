package control

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// These tests cover core-owned autoconnect end to end at the protocol level:
// the set_autoconnect command records, reports and persists the preference; a
// successful user connect records its (profile, explicit node) intent; and
// AutoconnectOnStart — the daemon-start hook — re-issues exactly that intent,
// yields to a user command that lands first, stays honestly idle when the
// persisted target is gone, and is never re-triggered by relaunches or
// hot-swaps (which must not rewrite the intent either).

// settingsAt opens a file-backed settings store over dir, failing the test on
// error.
func settingsAt(t *testing.T, dir string) *fileSettings {
	t.Helper()
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	return st
}

// TestSetAutoconnectReportsAndPersists: the toggle round-trips through the
// response, the status command, and — across a daemon restart — the settings
// file, in both directions.
func TestSetAutoconnectReportsAndPersists(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	h.daemon.SetSettings(settingsAt(t, dir))

	h.send(Request{ID: 1, Cmd: CmdSetAutoconnect, On: true})
	var st State
	h.dataInto(h.await(), &st)
	if !st.Autoconnect {
		t.Fatal("set_autoconnect response autoconnect = false, want true")
	}

	h.send(Request{ID: 2, Cmd: CmdStatus})
	h.dataInto(h.await(), &st)
	if !st.Autoconnect {
		t.Fatal("status does not report autoconnect after arming")
	}

	// A "restarted" daemon over the same directory loads the preference back.
	h2 := newHarness(t)
	h2.daemon.SetSettings(settingsAt(t, dir))
	if !h2.daemon.snapshotState().Autoconnect {
		t.Error("autoconnect did not survive the restart")
	}

	// Disarming round-trips the same way. Decode into a fresh State: the wire
	// form omits autoconnect=false entirely, so reusing the armed struct would
	// keep the stale true.
	h.send(Request{ID: 3, Cmd: CmdSetAutoconnect, On: false})
	var off State
	h.dataInto(h.await(), &off)
	if off.Autoconnect {
		t.Fatal("set_autoconnect off still reports true")
	}
	h3 := newHarness(t)
	h3.daemon.SetSettings(settingsAt(t, dir))
	if h3.daemon.snapshotState().Autoconnect {
		t.Error("disarmed autoconnect did not survive the restart")
	}
}

// TestSetAutoconnectSurvivesOtherSettingWrites: every settings write snapshots
// the full preference set, so a later kill-switch toggle must not clobber the
// stored autoconnect flag (or vice versa).
func TestSetAutoconnectSurvivesOtherSettingWrites(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st := settingsAt(t, dir)
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetAutoconnect, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: true})
	h.await()

	ps := st.Load()
	if !ps.Autoconnect {
		t.Error("set_kill_switch write clobbered the persisted autoconnect flag")
	}
	if !ps.KillSwitch {
		t.Error("kill switch missing from the persisted settings")
	}
}

// TestConnectRecordsLastConnIntent: a successful user connect persists the
// profile — and the node only when the request pinned one — as the intent
// autoconnect re-issues later.
func TestConnectRecordsLastConnIntent(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st := settingsAt(t, dir)
	h.daemon.SetSettings(st)
	p := seedMultiProto(t, h)

	// No explicit node: the walk chooses, but the recorded intent is "whole
	// profile", not whichever exit happened to win.
	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	ps := st.Load()
	if ps.LastProfile != p.ID {
		t.Errorf("last_profile = %q, want %q", ps.LastProfile, p.ID)
	}
	if ps.LastNode != "" {
		t.Errorf("last_node = %q, want empty for a connect without an explicit node", ps.LastNode)
	}

	// An explicit node is recorded as such.
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID, Node: "hy2-id"})
	h.await()
	h.awaitState(StateConnected)
	ps = st.Load()
	if ps.LastProfile != p.ID || ps.LastNode != "hy2-id" {
		t.Errorf("last conn = (%q, %q), want (%q, %q)", ps.LastProfile, ps.LastNode, p.ID, "hy2-id")
	}
}

// TestHotSwapDoesNotRewriteLastConn: the live re-apply of a kill-switch toggle
// pins the node the tunnel happens to be on; recording that would silently turn
// a "whole profile" intent into an explicit-node one, so the persisted intent
// must survive the swap untouched.
func TestHotSwapDoesNotRewriteLastConn(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st := settingsAt(t, dir)
	h.daemon.SetSettings(st)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)

	// Arming the kill switch hot-swaps onto the current node (explicitly pinned
	// inside startConnect); the intent must stay "whole profile" — regardless of
	// whether the toggle also raced the connecting-window reconcile into an
	// extra swap, since every off-command connect carries remember=false.
	h.send(Request{ID: 2, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.awaitState(StateConnected)
	if ps := st.Load(); ps.LastProfile != p.ID || ps.LastNode != "" {
		t.Errorf("after hot-swap, last conn = (%q, %q), want (%q, \"\")", ps.LastProfile, ps.LastNode, p.ID)
	}
}

// TestRelaunchDoesNotRewriteLastConn: the kill-switch relaunch of a dead tunnel
// pins the node that was up; like the hot-swap it must re-issue the recorded
// intent, never rewrite it. The switch is armed while idle so the connect that
// follows carries it from the start — no live re-apply races the exit.
func TestRelaunchDoesNotRewriteLastConn(t *testing.T) {
	dir := t.TempDir()

	h := newHarness(t)
	st := settingsAt(t, dir)
	h.daemon.SetSettings(st)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdSetKillSwitch, On: true})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	h.awaitState(StateConnected)
	if ps := st.Load(); ps.LastProfile != p.ID || ps.LastNode != "" {
		t.Fatalf("connect recorded (%q, %q), want (%q, \"\")", ps.LastProfile, ps.LastNode, p.ID)
	}

	h.runner.exit(errors.New("boom"))
	h.awaitLogContains("kill switch: tunnel process died")
	h.awaitState(StateConnected)
	if ps := st.Load(); ps.LastProfile != p.ID || ps.LastNode != "" {
		t.Errorf("after relaunch, last conn = (%q, %q), want (%q, \"\")", ps.LastProfile, ps.LastNode, p.ID)
	}
}

// TestAutoconnectOnStartReconnectsLastProfile: with the preference armed and a
// last profile persisted, the daemon-start hook brings the tunnel up through
// the ordinary fallback walk.
func TestAutoconnectOnStartReconnectsLastProfile(t *testing.T) {
	dir := t.TempDir()
	if err := settingsAt(t, dir).Save(persistedSettings{Autoconnect: true, LastProfile: "multi"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	h := newHarness(t)
	seedMultiProto(t, h)
	h.daemon.SetSettings(settingsAt(t, dir))

	if !h.daemon.AutoconnectOnStart() {
		t.Fatal("AutoconnectOnStart did not launch an attempt")
	}
	h.awaitState(StateConnected)
	if h.runner.starts() != 1 {
		t.Errorf("starts = %d, want 1", h.runner.starts())
	}
}

// TestAutoconnectOnStartHonoursExplicitNode: a persisted explicit node is
// reconnected exactly, not re-chosen by the walk.
func TestAutoconnectOnStartHonoursExplicitNode(t *testing.T) {
	dir := t.TempDir()
	if err := settingsAt(t, dir).Save(persistedSettings{
		Autoconnect: true, LastProfile: "multi", LastNode: "hy2-id",
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	h := newHarness(t)
	seedMultiProto(t, h)
	h.daemon.SetSettings(settingsAt(t, dir))

	if !h.daemon.AutoconnectOnStart() {
		t.Fatal("AutoconnectOnStart did not launch an attempt")
	}
	connected := h.awaitState(StateConnected)
	if connected["node"] != "hy2-id" {
		t.Errorf("autoconnected node = %v, want the persisted explicit hy2-id", connected["node"])
	}
}

// TestAutoconnectOnStartDoesNothingWhenOff: with the preference disarmed (or
// nothing recorded) the start hook must not touch the runner or the state.
func TestAutoconnectOnStartDoesNothingWhenOff(t *testing.T) {
	dir := t.TempDir()
	// Off but with a last profile recorded: still a no-op.
	if err := settingsAt(t, dir).Save(persistedSettings{LastProfile: "multi"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	h := newHarness(t)
	seedMultiProto(t, h)
	h.daemon.SetSettings(settingsAt(t, dir))

	if h.daemon.AutoconnectOnStart() {
		t.Fatal("AutoconnectOnStart launched with the preference off")
	}

	// Armed but with no last profile: equally a no-op.
	h2 := newHarness(t)
	dir2 := t.TempDir()
	if err := settingsAt(t, dir2).Save(persistedSettings{Autoconnect: true}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	h2.daemon.SetSettings(settingsAt(t, dir2))
	if h2.daemon.AutoconnectOnStart() {
		t.Fatal("AutoconnectOnStart launched with no last profile recorded")
	}

	if h.runner.starts() != 0 || h2.runner.starts() != 0 {
		t.Errorf("starts = %d/%d, want 0/0", h.runner.starts(), h2.runner.starts())
	}
	if got := h.daemon.snapshotState().State; got != StateIdle {
		t.Errorf("state = %q, want idle", got)
	}
}

// TestAutoconnectOnStartProfileGoneStaysIdle: a last profile that no longer
// exists is logged and skipped — no error state, no process.
func TestAutoconnectOnStartProfileGoneStaysIdle(t *testing.T) {
	dir := t.TempDir()
	if err := settingsAt(t, dir).Save(persistedSettings{Autoconnect: true, LastProfile: "ghost"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	h := newHarness(t)
	h.daemon.SetSettings(settingsAt(t, dir))

	if h.daemon.AutoconnectOnStart() {
		t.Fatal("AutoconnectOnStart claimed to launch for a missing profile")
	}
	h.awaitLogContains("autoconnect: last profile no longer stored")
	if h.runner.starts() != 0 {
		t.Errorf("starts = %d, want 0", h.runner.starts())
	}
	if got := h.daemon.snapshotState().State; got != StateIdle {
		t.Errorf("state = %q, want idle", got)
	}
}

// TestAutoconnectOnStartNodeGoneStaysIdle: a persisted explicit node that has
// vanished from the profile must not be second-guessed with a different exit;
// the attempt fails before any teardown and the daemon stays idle.
func TestAutoconnectOnStartNodeGoneStaysIdle(t *testing.T) {
	dir := t.TempDir()
	if err := settingsAt(t, dir).Save(persistedSettings{
		Autoconnect: true, LastProfile: "multi", LastNode: "vanished-id",
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	h := newHarness(t)
	seedMultiProto(t, h)
	h.daemon.SetSettings(settingsAt(t, dir))

	if !h.daemon.AutoconnectOnStart() {
		t.Fatal("AutoconnectOnStart did not launch an attempt")
	}
	h.awaitLogContains("staying idle")
	if h.runner.starts() != 0 {
		t.Errorf("starts = %d, want 0 (no process for a vanished node)", h.runner.starts())
	}
	if got := h.daemon.snapshotState().State; got != StateIdle {
		t.Errorf("state = %q, want idle — never an error for a connect nobody asked for", got)
	}
}

// TestAutoconnectYieldsToUserCommand: a user connect that lands while the
// autoconnect goroutine is still on its way to connMu wins; the parked
// autoconnect observes the moved generation and aborts instead of stomping the
// user's choice.
func TestAutoconnectYieldsToUserCommand(t *testing.T) {
	dir := t.TempDir()
	if err := settingsAt(t, dir).Save(persistedSettings{Autoconnect: true, LastProfile: "multi"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	h := newHarness(t)
	p := seedMultiProto(t, h)
	h.daemon.SetSettings(settingsAt(t, dir))

	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.daemon.beforeReconnect = func() {
		once.Do(func() { close(parked) })
		<-release
	}

	if !h.daemon.AutoconnectOnStart() {
		t.Fatal("AutoconnectOnStart did not launch an attempt")
	}
	<-parked

	// The user connects to an explicit node while autoconnect is parked.
	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID, Node: "hy2-id"})
	h.await()
	connected := h.awaitState(StateConnected)
	if connected["node"] != "hy2-id" {
		t.Fatalf("user connect landed on %v, want hy2-id", connected["node"])
	}
	starts := h.runner.starts()

	// Release the autoconnect: it must observe the bumped generation and yield.
	close(release)
	time.Sleep(50 * time.Millisecond) // give the goroutine a chance to (wrongly) act
	if got := h.runner.starts(); got != starts {
		t.Errorf("autoconnect started a tunnel over the user's (starts %d -> %d)", starts, got)
	}
	if got := h.daemon.snapshotState(); got.State != StateConnected || got.Node != "hy2-id" {
		t.Errorf("state = %q on %q, want connected on hy2-id", got.State, got.Node)
	}
}
