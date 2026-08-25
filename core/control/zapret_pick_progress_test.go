package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/zapret"
)

// A strategy probe run takes minutes: every strategy in the bundle is attached,
// measured against each destination and detached. Until it reported its own
// progress the only sign it was alive was a line on the log screen, which is a
// different screen from the button that starts it — and a control that says
// "measuring" for five minutes with nothing behind it is indistinguishable from
// a hung app. These cover the event that ended that, without going near winws:
// the run is driven by a stand-in runner, because the real one needs the
// WinDivert driver and would install it on the machine running the tests.

// stubPickRunner plays the bypass runner for a probe run: it reports a scripted
// measurement for every strategy it is handed and invokes the progress callback
// exactly where the real Pick invokes it — once per strategy, after that
// strategy has been measured.
type stubPickRunner struct {
	// carries is how many of the run's destinations each strategy is scripted to
	// carry. dead lists strategies whose process never came up — they report no
	// measurements at all, which is the shape that used to produce a "0/0".
	carries  int
	dead     map[string]bool
	baseline int
	err      error
	// stopOnPerfect mirrors Runner.StopOnPerfect: the walk ends at the first
	// strategy that carries every target. It is decided by the same rule the real
	// Pick uses, so a run driven by this stub ends where a real one would.
	stopOnPerfect bool
	// seen records the strategies the run was handed, in order.
	seen []string
}

func (s *stubPickRunner) Pick(
	_ context.Context,
	strategies []zapret.Strategy,
	targets []string,
	progress func(zapret.Result),
) ([]zapret.Result, int, error) {
	var out []zapret.Result
	for _, st := range strategies {
		s.seen = append(s.seen, st.Name)
		res := zapret.Result{Strategy: st, Name: st.Name, Started: !s.dead[st.Name]}
		if res.Started {
			for i, t := range targets {
				res.Targets = append(res.Targets, zapret.TargetResult{
					Target: t, OK: i < s.carries, RTTMs: 10,
				})
			}
		}
		out = append(out, res)
		if progress != nil {
			progress(res)
		}
		if s.stopOnPerfect && zapret.ShouldStopEarly(res, len(targets), s.baseline) {
			break
		}
	}
	return out, s.baseline, s.err
}

// pickProgressFromEvent decodes a captured pick_progress event back into the
// typed body, so the assertions read against field names rather than against a
// map of float64s.
func pickProgressFromEvent(t *testing.T, ev map[string]any) pickProgressEvent {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal pick_progress event: %v", err)
	}
	var out pickProgressEvent
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode pick_progress event: %v", err)
	}
	return out
}

// awaitPickProgress collects the next n pick_progress events, draining anything
// else the run emits (the log line that goes with each step).
func (h *harness) awaitPickProgress(n int) []pickProgressEvent {
	h.t.Helper()
	out := make([]pickProgressEvent, 0, n)
	for len(out) < n {
		out = append(out, pickProgressFromEvent(h.t, h.awaitEvent(EventPickProgress)))
	}
	return out
}

// pickStrategies builds a probe plan out of bare strategy names.
func pickStrategies(strategies ...string) []zapret.Strategy {
	out := make([]zapret.Strategy, 0, len(strategies))
	for _, n := range strategies {
		out = append(out, zapret.Strategy{Name: n, Path: n + ".bat"})
	}
	return out
}

// The whole point of the event: every strategy in the run reports itself, in
// order, numbered against a total the user can read as "7 of 23". The count is
// the run's plan, which the probe callback cannot see — it is handed one result
// at a time — so getting it right is this layer's job.
func TestPickRunReportsEveryStrategyWithItsPlaceInTheRun(t *testing.T) {
	h := newHarness(t)
	strategies := pickStrategies("general", "general (ALT2)", "general (МГТС)")
	targets := []string{"a", "b", "c", "d", "e"}
	runner := &stubPickRunner{carries: 3}

	results, _, err := h.daemon.runPick(context.Background(), runner, strategies, targets)
	if err != nil {
		t.Fatalf("runPick: %v", err)
	}
	if len(results) != len(strategies) {
		t.Fatalf("measured %d strategies, want %d", len(results), len(strategies))
	}

	// One opening event plus one per strategy.
	got := h.awaitPickProgress(len(strategies) + 1)

	// The opening one is emitted before anything is measured, so it names no
	// strategy — it exists so the run is not silent through the baseline probe,
	// which costs as much as a strategy does.
	if open := got[0]; open.Index != 0 || open.Strategy != "" || open.Total != len(strategies) {
		t.Errorf("opening event = %+v, want index 0, no strategy, total %d", open, len(strategies))
	}

	for i, want := range strategies {
		step := got[i+1]
		if step.Strategy != want.Name {
			t.Errorf("step %d named %q, want %q", i+1, step.Strategy, want.Name)
		}
		if step.Index != i+1 {
			t.Errorf("step for %q numbered %d, want %d", want.Name, step.Index, i+1)
		}
		if step.Total != len(strategies) {
			t.Errorf("step for %q reports total %d, want %d", want.Name, step.Total, len(strategies))
		}
		if step.OK != 3 || step.Targets != len(targets) {
			t.Errorf("step for %q scored %d/%d, want 3/%d", want.Name, step.OK, step.Targets, len(targets))
		}
	}
}

// The line the log screen and the diagnostics bundle carry is not replaced by
// the event: it outlives the run, and the event does not.
func TestPickRunStillLogsEachStrategy(t *testing.T) {
	h := newHarness(t)
	runner := &stubPickRunner{carries: 2}

	if _, _, err := h.daemon.runPick(
		context.Background(), runner, pickStrategies("general (ALT2)"), []string{"a", "b", "c"},
	); err != nil {
		t.Fatalf("runPick: %v", err)
	}

	h.awaitLogContaining("general (ALT2)")
}

// A strategy whose process never came up measures nothing at all. Reporting its
// own (empty) destination list would put a "0 of 0" on screen mid-run, which
// reads as a broken readout rather than as the failure it is — so the
// denominator is the run's, and stays put.
func TestPickRunKeepsTheDenominatorOnAStrategyThatNeverStarted(t *testing.T) {
	h := newHarness(t)
	targets := []string{"a", "b", "c", "d", "e"}
	runner := &stubPickRunner{carries: 5, dead: map[string]bool{"general (ALT2)": true}}

	if _, _, err := h.daemon.runPick(
		context.Background(), runner, pickStrategies("general", "general (ALT2)"), targets,
	); err != nil {
		t.Fatalf("runPick: %v", err)
	}

	got := h.awaitPickProgress(3)
	dead := got[2]
	if dead.Strategy != "general (ALT2)" {
		t.Fatalf("second step named %q, want the dead strategy", dead.Strategy)
	}
	if dead.OK != 0 || dead.Targets != len(targets) {
		t.Errorf("dead strategy scored %d/%d, want 0/%d", dead.OK, dead.Targets, len(targets))
	}
	if dead.Index != 2 || dead.Total != 2 {
		t.Errorf("dead strategy placed %d/%d in the run, want 2/2", dead.Index, dead.Total)
	}
}

// An empty run still opens: the caller has already told the user something is
// happening, and a run that emitted nothing would leave that hanging.
func TestPickRunOpensEvenWithNothingToMeasure(t *testing.T) {
	h := newHarness(t)
	runner := &stubPickRunner{carries: 0}

	if _, _, err := h.daemon.runPick(
		context.Background(), runner, nil, []string{"a"},
	); err != nil {
		t.Fatalf("runPick: %v", err)
	}

	open := h.awaitPickProgress(1)[0]
	if open.Total != 0 || open.Index != 0 {
		t.Errorf("opening event = %+v, want an empty run", open)
	}
}

// pickRunSentinel is emitted after a run has returned, so a test can tell "no
// more progress events are coming" from "the next one has not arrived yet".
// Counting what was pushed needs an end, and the only reliable end is something
// known to be last on the same stream.
const pickRunSentinel = "the pick run has returned"

// awaitPickProgressUntilSentinel drains the event stream up to the sentinel log
// line, returning every pick_progress that came before it.
func (h *harness) awaitPickProgressUntilSentinel() []pickProgressEvent {
	h.t.Helper()
	var out []pickProgressEvent
	deadline := time.After(4 * time.Second)
	for {
		select {
		case ev := <-h.events:
			switch ev["event"] {
			case EventPickProgress:
				out = append(out, pickProgressFromEvent(h.t, ev))
			case EventLog:
				if msg, _ := ev["msg"].(string); strings.Contains(msg, pickRunSentinel) {
					return out
				}
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for the run to finish; got %d progress events", len(out))
			return nil
		}
	}
}

// A run that has already found a strategy carrying everything is finished, and
// the twenty-one strategies after it cost the settle time plus a probe budget
// each for an answer that cannot change. The run stops there — fewer progress
// steps than the bundle has strategies — and still hands back a winner.
func TestPickRunStopsAtTheFirstStrategyThatTakesEveryTarget(t *testing.T) {
	h := newHarness(t)
	strategies := pickStrategies("general", "general (ALT2)", "general (МГТС)", "general (FAKE TLS AUTO)")
	targets := []string{"a", "b", "c", "d", "e"}
	runner := &stubPickRunner{carries: len(targets), baseline: 3, stopOnPerfect: true}

	results, baseline, err := h.daemon.runPick(context.Background(), runner, strategies, targets)
	if err != nil {
		t.Fatalf("runPick: %v", err)
	}
	h.daemon.emitLog(LogInfo, pickRunSentinel)

	if len(runner.seen) != 1 {
		t.Errorf("the run measured %d strategies (%v), want only the first", len(runner.seen), runner.seen)
	}
	if len(results) != 1 {
		t.Errorf("the run reported %d results, want 1", len(results))
	}

	// One opening beat plus one strategy: fewer steps than the bundle has
	// strategies, which is the whole point.
	steps := h.awaitPickProgressUntilSentinel()
	if len(steps) > len(strategies) {
		t.Errorf("the run emitted %d progress events for a %d-strategy bundle; it did not stop early",
			len(steps), len(strategies))
	}
	if len(steps) != 2 {
		t.Fatalf("the run emitted %d progress events, want the opening beat plus one strategy", len(steps))
	}
	if steps[1].Strategy != strategies[0].Name || steps[1].Index != 1 {
		t.Errorf("the one measured step is %+v, want %q at position 1", steps[1], strategies[0].Name)
	}
	// The denominator stays the run's plan: stopping early is not a smaller
	// bundle, and a step that renumbered itself to "1 of 1" would read as one.
	if steps[1].Total != len(strategies) {
		t.Errorf("the step reports a total of %d, want the run's %d", steps[1].Total, len(strategies))
	}

	best, found := zapret.Best(results, baseline)
	if !found {
		t.Fatal("the run stopped early and then reported no winner at all")
	}
	if best.Name != strategies[0].Name {
		t.Errorf("winner is %q, want %q", best.Name, strategies[0].Name)
	}
}

// The edge the early exit must not take: a network where nothing is blocked. The
// baseline already carries every target, so the first strategy measured scores
// full marks — as would every other, and as does running no bypass at all.
// Stopping there would crown whichever strategy the bundle lists first and leave
// the user running a kernel packet filter for nothing, on an answer Best then
// refuses anyway.
func TestPickRunMeasuresTheWholeBundleWhenNothingIsBlocked(t *testing.T) {
	h := newHarness(t)
	strategies := pickStrategies("general", "general (ALT2)", "general (МГТС)")
	targets := []string{"a", "b", "c"}
	runner := &stubPickRunner{carries: len(targets), baseline: len(targets), stopOnPerfect: true}

	results, baseline, err := h.daemon.runPick(context.Background(), runner, strategies, targets)
	if err != nil {
		t.Fatalf("runPick: %v", err)
	}

	if len(runner.seen) != len(strategies) {
		t.Errorf("the run measured %d strategies (%v), want all %d: it stopped on a strategy that beat nothing",
			len(runner.seen), runner.seen, len(strategies))
	}
	if _, found := zapret.Best(results, baseline); found {
		t.Error("a strategy was reported on a network where the baseline already carried everything")
	}
}
