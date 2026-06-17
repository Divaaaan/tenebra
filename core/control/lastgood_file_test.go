package control

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFileLastGoodRoundTrip: a value Set on one store is visible to a freshly
// opened store over the same directory — the persistence that makes the last
// working node lead the fallback walk on the next launch.
func TestFileLastGoodRoundTrip(t *testing.T) {
	dir := t.TempDir()

	lg, err := OpenFileLastGood(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	lg.Set("profA", "node-1")
	lg.Set("profB", "node-2")
	lg.Set("profA", "node-3") // overwrite

	// A fresh store over the same dir must load what was written.
	lg2, err := OpenFileLastGood(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if id, ok := lg2.Get("profA"); !ok || id != "node-3" {
		t.Errorf("profA = %q,%v, want node-3,true", id, ok)
	}
	if id, ok := lg2.Get("profB"); !ok || id != "node-2" {
		t.Errorf("profB = %q,%v, want node-2,true", id, ok)
	}
	if _, ok := lg2.Get("ghost"); ok {
		t.Error("ghost profile should have no last-good")
	}
}

// TestFileLastGoodCorruptFileIsEmpty: a mangled cache file must not stop the
// daemon — the store opens empty and relearns, rather than erroring.
func TestFileLastGoodCorruptFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, lastGoodFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	lg, err := OpenFileLastGood(dir)
	if err != nil {
		t.Fatalf("open over corrupt file: %v", err)
	}
	if _, ok := lg.Get("anything"); ok {
		t.Error("corrupt file should yield an empty store")
	}
	// And it must be usable: a Set after a corrupt load persists cleanly.
	lg.Set("p", "n")
	lg2, err := OpenFileLastGood(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if id, ok := lg2.Get("p"); !ok || id != "n" {
		t.Errorf("after recovery, p = %q,%v, want n,true", id, ok)
	}
}

// TestFileLastGoodMissingDirCreated: opening in a not-yet-existing nested dir
// creates it, matching the profile store's behaviour.
func TestFileLastGoodMissingDirCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cfg")
	lg, err := OpenFileLastGood(dir)
	if err != nil {
		t.Fatalf("open nested: %v", err)
	}
	lg.Set("p", "n")
	if _, err := os.Stat(filepath.Join(dir, lastGoodFile)); err != nil {
		t.Errorf("last-good file not written: %v", err)
	}
}

// TestDaemonHonoursPersistedLastGoodAcrossRestart proves the wiring: a node
// recorded as last-good through one daemon (with the file store installed) leads
// the fallback walk for a brand-new daemon over the same config dir — the
// last-working node survives a restart and is tried first.
func TestDaemonHonoursPersistedLastGoodAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	lg, err := OpenFileLastGood(dir)
	if err != nil {
		t.Fatalf("open last-good: %v", err)
	}
	// Simulate a prior run that learned Hysteria2 works for this profile.
	lg.Set("multi", "hy2-id")

	// A fresh daemon over the same store with the persisted last-good installed.
	h := newHarness(t)
	lg2, err := OpenFileLastGood(dir)
	if err != nil {
		t.Fatalf("reopen last-good: %v", err)
	}
	h.daemon.SetLastGood(lg2)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	connected := h.awaitState(StateConnected)
	if connected["node"] != "hy2-id" {
		t.Errorf("connected node = %v, want hy2-id (persisted last-good leads)", connected["node"])
	}
	// The first config started must select the persisted node's tag.
	if got := connectedNodeTag(t, h.runner.startCfgs()[0]); got != serverTags(p)["hy2-id"] {
		t.Errorf("first config selected %q, want hy2 tag %q", got, serverTags(p)["hy2-id"])
	}
}
