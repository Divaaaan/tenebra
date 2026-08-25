package control

import (
	"context"
	"encoding/json"
	"testing"

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
