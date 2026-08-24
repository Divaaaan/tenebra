package control

import (
	"testing"
	"time"
)

// collectLevels drives every level through emitLog and returns the ones that
// reached the emitter, in order.
func collectLevels(t *testing.T, d *Daemon) []string {
	t.Helper()
	var got []string
	d.SetEmitter(func(name string, body any) {
		if name != EventLog {
			return
		}
		ev, ok := body.(LogEvent)
		if !ok {
			t.Fatalf("log event body = %T, want LogEvent", body)
		}
		got = append(got, ev.Level)
	})
	for _, lvl := range []string{LogDebug, LogInfo, LogWarn, LogError} {
		d.emitLog(lvl, "line at "+lvl)
	}
	return got
}

// TestDefaultLevelDropsDebug is the point of the whole exercise: a shipped build
// must not narrate itself. Debug is what a support session turns on, never what
// a machine runs at for months.
func TestDefaultLevelDropsDebug(t *testing.T) {
	d, _ := newTestDaemon(t)
	if got := d.LogLevel(); got != LogInfo {
		t.Fatalf("default level = %q, want %q", got, LogInfo)
	}
	got := collectLevels(t, d)
	want := []string{LogInfo, LogWarn, LogError}
	if len(got) != len(want) {
		t.Fatalf("levels through the filter = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("levels through the filter = %v, want %v", got, want)
		}
	}
}

// TestRaisingTheLevelLetsDebugThrough: the knob has to actually do something,
// which is the half that was missing — the level constants existed and nothing
// consulted them.
func TestRaisingTheLevelLetsDebugThrough(t *testing.T) {
	d, _ := newTestDaemon(t)
	if !d.SetLogLevel("debug") {
		t.Fatal("SetLogLevel(debug) was refused")
	}
	if got := len(collectLevels(t, d)); got != 4 {
		t.Fatalf("%d levels through the filter at debug, want 4", got)
	}
}

// TestLoweringTheLevelSilencesInfo: a support case that only wants failures can
// pin the log to warnings and above.
func TestLoweringTheLevelSilencesInfo(t *testing.T) {
	d, _ := newTestDaemon(t)
	if !d.SetLogLevel("warn") {
		t.Fatal("SetLogLevel(warn) was refused")
	}
	got := collectLevels(t, d)
	if len(got) != 2 || got[0] != LogWarn || got[1] != LogError {
		t.Fatalf("levels through the filter = %v, want [warn error]", got)
	}
}

// TestUnknownLevelIsRefusedAndChangesNothing: a typo in the environment must not
// silence the daemon, which is what treating an unparsed value as "highest"
// would do.
func TestUnknownLevelIsRefusedAndChangesNothing(t *testing.T) {
	d, _ := newTestDaemon(t)
	if d.SetLogLevel("chatty") {
		t.Fatal("SetLogLevel accepted a level it does not know")
	}
	if got := d.LogLevel(); got != LogInfo {
		t.Fatalf("level after a refused change = %q, want %q", got, LogInfo)
	}
}

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		valid bool
	}{
		{"debug", LogDebug, true},
		{"DEBUG", LogDebug, true},
		{"  trace ", LogDebug, true},
		{"verbose", LogDebug, true},
		{"info", LogInfo, true},
		{"notice", LogInfo, true},
		{"warn", LogWarn, true},
		{"warning", LogWarn, true},
		{"Error", LogError, true},
		{"err", LogError, true},
		{"", DefaultLogLevel, false},
		{"loud", DefaultLogLevel, false},
	}
	for _, c := range cases {
		got, ok := ParseLogLevel(c.in)
		if got != c.want || ok != c.valid {
			t.Errorf("ParseLogLevel(%q) = (%q, %t), want (%q, %t)", c.in, got, ok, c.want, c.valid)
		}
	}
}

// TestLevelComesFromTheEnvironment: the support instruction is "set this, restart
// the core, reproduce", so the variable has to be read at construction.
func TestLevelComesFromTheEnvironment(t *testing.T) {
	t.Setenv(LogLevelEnv, "debug")
	d, _ := newTestDaemon(t)
	if got := d.LogLevel(); got != LogDebug {
		t.Fatalf("level from %s=debug is %q, want %q", LogLevelEnv, got, LogDebug)
	}
}

// TestGarbageInTheEnvironmentKeepsTheDefault: a mistyped variable must leave a
// working daemon, not a silent one.
func TestGarbageInTheEnvironmentKeepsTheDefault(t *testing.T) {
	t.Setenv(LogLevelEnv, "yes please")
	d, _ := newTestDaemon(t)
	if got := d.LogLevel(); got != DefaultLogLevel {
		t.Fatalf("level from a bad %s is %q, want %q", LogLevelEnv, got, DefaultLogLevel)
	}
}

// TestSinkAndRingSeeTheSameFilteredLines pins the property that makes the log
// file worth anything: a line that passes the filter reaches the process log and
// the in-memory ring whether or not a UI ever attached.
func TestSinkAndRingSeeTheSameFilteredLines(t *testing.T) {
	d, _ := newTestDaemon(t)
	var sunk []string
	d.SetLogSink(func(level, msg string) { sunk = append(sunk, level+":"+msg) })

	d.emitLog(LogDebug, "dropped")
	d.emitLog(LogInfo, "kept")
	d.emitLog(LogError, "also kept")

	if len(sunk) != 2 || sunk[0] != "info:kept" || sunk[1] != "error:also kept" {
		t.Fatalf("sink saw %v, want the two lines above the threshold", sunk)
	}
	ring := d.logs.snapshot()
	if len(ring) != 2 {
		t.Fatalf("ring holds %d lines, want 2", len(ring))
	}
	if ring[0].Msg != "kept" || ring[1].Msg != "also kept" {
		t.Fatalf("ring holds %+v, want the two lines above the threshold", ring)
	}
	if ring[0].At.IsZero() {
		t.Error("ring entry carries no timestamp")
	}
}

// TestRingKeepsTheNewestLines: the ring is what a diagnostics bundle reads on a
// machine with no log file, so overflowing it must cost the oldest lines and
// keep the ones next to the failure.
func TestRingKeepsTheNewestLines(t *testing.T) {
	r := newLogRing(3)
	for i, msg := range []string{"a", "b", "c", "d", "e"} {
		r.add(logEntry{At: time.Unix(int64(i), 0), Level: LogInfo, Msg: msg})
	}
	got := r.snapshot()
	if len(got) != 3 {
		t.Fatalf("ring holds %d entries, want 3", len(got))
	}
	for i, want := range []string{"c", "d", "e"} {
		if got[i].Msg != want {
			t.Fatalf("ring = %v, want [c d e]", got)
		}
	}
}

// TestRingBeforeItFillsIsInOrder covers the other half of the circular buffer:
// a short run must not report empty slots.
func TestRingBeforeItFillsIsInOrder(t *testing.T) {
	r := newLogRing(5)
	r.add(logEntry{Msg: "one"})
	r.add(logEntry{Msg: "two"})
	got := r.snapshot()
	if len(got) != 2 || got[0].Msg != "one" || got[1].Msg != "two" {
		t.Fatalf("ring = %v, want [one two]", got)
	}
}
