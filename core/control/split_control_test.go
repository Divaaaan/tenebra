package control

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Divaaaan/tenebra/core/routing"
)

// TestSetSplitExclude: a set_split exclude request is accepted, echoes the
// normalized split in its State, and status reflects it afterwards.
func TestSetSplitExclude(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdSetSplit, Mode: "exclude", Apps: []string{"Chrome.exe", "steam.exe"}})
	r := h.await()
	var st State
	h.dataInto(r, &st)
	if st.Split != "exclude" {
		t.Errorf("split = %q, want exclude", st.Split)
	}
	if !reflect.DeepEqual(st.SplitApps, []string{"chrome.exe", "steam.exe"}) {
		t.Errorf("split apps = %v, want [chrome.exe steam.exe] (lowercased, sorted)", st.SplitApps)
	}

	// Status must reflect the new split.
	h.send(Request{ID: 2, Cmd: CmdStatus})
	var st2 State
	h.dataInto(h.await(), &st2)
	if st2.Split != "exclude" || !reflect.DeepEqual(st2.SplitApps, []string{"chrome.exe", "steam.exe"}) {
		t.Errorf("status split = %q %v, want exclude [chrome.exe steam.exe]", st2.Split, st2.SplitApps)
	}

	// The daemon's live routing options must carry it so the next connect uses it.
	ro := h.daemon.snapshotRouting()
	if ro.SplitMode != routing.SplitExclude || len(ro.SplitApps) != 2 {
		t.Errorf("daemon routing split = %q %v, want exclude with 2 apps", ro.SplitMode, ro.SplitApps)
	}
}

// TestSetSplitInclude: include mode is accepted and reflected likewise.
func TestSetSplitInclude(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdSetSplit, Mode: "include", Apps: []string{"discord.exe"}})
	var st State
	h.dataInto(h.await(), &st)
	if st.Split != "include" || !reflect.DeepEqual(st.SplitApps, []string{"discord.exe"}) {
		t.Errorf("include split = %q %v, want include [discord.exe]", st.Split, st.SplitApps)
	}
}

// TestSetSplitOffClearsState: switching to off (or sending an empty list) clears
// the reported split entirely — a no-op split must be invisible.
func TestSetSplitOffClearsState(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdSetSplit, Mode: "exclude", Apps: []string{"chrome.exe"}})
	h.await()

	h.send(Request{ID: 2, Cmd: CmdSetSplit, Mode: "off"})
	var st State
	h.dataInto(h.await(), &st)
	if st.Split != "" || st.SplitApps != nil {
		t.Errorf("after off, split = %q %v, want empty", st.Split, st.SplitApps)
	}

	// Status agrees.
	h.send(Request{ID: 3, Cmd: CmdStatus})
	var st2 State
	h.dataInto(h.await(), &st2)
	if st2.Split != "" || st2.SplitApps != nil {
		t.Errorf("status after off = %q %v, want empty", st2.Split, st2.SplitApps)
	}
}

// TestSetSplitEmptyListCollapsesToOff: an exclude with no usable apps is a no-op
// and reports as off, matching routing.Normalize.
func TestSetSplitEmptyListCollapsesToOff(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdSetSplit, Mode: "include", Apps: []string{"   ", ""}})
	var st State
	h.dataInto(h.await(), &st)
	if st.Split != "" || st.SplitApps != nil {
		t.Errorf("empty include list split = %q %v, want empty (off)", st.Split, st.SplitApps)
	}
}

// TestSetSplitBadMode: an unknown split mode is rejected.
func TestSetSplitBadMode(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdSetSplit, Mode: "sideways", Apps: []string{"a.exe"}})
	r := h.await()
	if r.Ok {
		t.Error("bad split mode should fail")
	}
}

// TestSetSplitDoesNotDisturbRoutingMode: setting a split leaves the base routing
// mode untouched, and vice versa.
func TestSetSplitDoesNotDisturbRoutingMode(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdSetRouting, Mode: "global"})
	h.await()
	h.send(Request{ID: 2, Cmd: CmdSetSplit, Mode: "exclude", Apps: []string{"chrome.exe"}})
	var st State
	h.dataInto(h.await(), &st)
	if st.Routing != "global" {
		t.Errorf("routing mode = %q, want global (split must not change it)", st.Routing)
	}
	if st.Split != "exclude" {
		t.Errorf("split = %q, want exclude", st.Split)
	}
}

// TestApplySplitToState verifies the State projection directly.
func TestApplySplitToState(t *testing.T) {
	var s State
	applySplitToState(&s, routing.Options{SplitMode: routing.SplitInclude, SplitApps: []string{"a.exe"}}.Normalize())
	if s.Split != "include" || !reflect.DeepEqual(s.SplitApps, []string{"a.exe"}) {
		t.Errorf("apply include = %q %v", s.Split, s.SplitApps)
	}
	// Off clears prior values.
	applySplitToState(&s, routing.Options{SplitMode: routing.SplitOff}.Normalize())
	if s.Split != "" || s.SplitApps != nil {
		t.Errorf("apply off should clear, got %q %v", s.Split, s.SplitApps)
	}
	// The stored slice must not alias the routing options.
	ro := routing.Options{SplitMode: routing.SplitExclude, SplitApps: []string{"x.exe", "y.exe"}}.Normalize()
	applySplitToState(&s, ro)
	ro.SplitApps[0] = "MUTATED"
	if s.SplitApps[0] == "MUTATED" {
		t.Error("State.SplitApps aliases the routing options slice")
	}
}

// --- persistence ---

// TestFileSettingsRoundTrip: split config saved through one store loads from a
// fresh store over the same directory.
func TestFileSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()

	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Save(persistedSettings{SplitMode: "exclude", SplitApps: []string{"chrome.exe", "steam.exe"}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := st2.Load()
	if got.SplitMode != "exclude" || !reflect.DeepEqual(got.SplitApps, []string{"chrome.exe", "steam.exe"}) {
		t.Errorf("loaded = %q %v, want exclude [chrome.exe steam.exe]", got.SplitMode, got.SplitApps)
	}
	if got.Version != settingsVersion {
		t.Errorf("version = %d, want %d", got.Version, settingsVersion)
	}
}

// TestFileSettingsCorruptIsEmpty: a mangled settings file must not stop the
// daemon — Load returns the zero value.
func TestFileSettingsCorruptIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, settingsFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open over corrupt: %v", err)
	}
	if got := st.Load(); got.SplitMode != "" || got.SplitApps != nil {
		t.Errorf("corrupt file should load empty, got %q %v", got.SplitMode, got.SplitApps)
	}
}

// TestFileSettingsUnknownVersionIgnored: a future version is treated as no
// settings so a downgrade can't misread a newer layout.
func TestFileSettingsUnknownVersionIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, settingsFile), []byte(`{"version":999,"split_mode":"exclude","split_apps":["x.exe"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := st.Load(); got.SplitMode != "" {
		t.Errorf("unknown version should be ignored, got %q", got.SplitMode)
	}
}

// TestSetSplitPersistsThroughStore: a set_split on a daemon with the file store
// installed writes a file that a fresh store reads back, normalized.
func TestSetSplitPersistsThroughStore(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t)
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	h.daemon.SetSettings(st)

	h.send(Request{ID: 1, Cmd: CmdSetSplit, Mode: "include", Apps: []string{"Brave.exe", "brave.exe"}})
	h.await()

	reopened, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	got := reopened.Load()
	if got.SplitMode != "include" || !reflect.DeepEqual(got.SplitApps, []string{"brave.exe"}) {
		t.Errorf("persisted split = %q %v, want include [brave.exe] (deduped, lowercased)", got.SplitMode, got.SplitApps)
	}
}

// TestDaemonLoadsPersistedSplitAcrossRestart proves the wiring: a split saved by
// one daemon is loaded into a brand-new daemon's routing options and reported
// state over the same config dir — the choice survives a restart.
func TestDaemonLoadsPersistedSplitAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	// Prior run: persist an exclude split directly through the store.
	st, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	if err := st.Save(persistedSettings{SplitMode: "exclude", SplitApps: []string{"telegram.exe"}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Fresh daemon over the same dir with the store installed.
	h := newHarness(t)
	st2, err := OpenFileSettings(dir)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	h.daemon.SetSettings(st2)

	// Status must already report the persisted split.
	h.send(Request{ID: 1, Cmd: CmdStatus})
	var got State
	h.dataInto(h.await(), &got)
	if got.Split != "exclude" || !reflect.DeepEqual(got.SplitApps, []string{"telegram.exe"}) {
		t.Errorf("restart split = %q %v, want exclude [telegram.exe]", got.Split, got.SplitApps)
	}

	// And the live routing options carry it for the next connect.
	ro := h.daemon.snapshotRouting()
	if ro.SplitMode != routing.SplitExclude || !reflect.DeepEqual(ro.SplitApps, []string{"telegram.exe"}) {
		t.Errorf("restart routing split = %q %v, want exclude [telegram.exe]", ro.SplitMode, ro.SplitApps)
	}
}
