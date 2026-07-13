package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
)

// These tests cover the "attempts" event: the step-by-step snapshot of the
// anti-DPI fallback walk. They assert the snapshot sequence a multi-candidate
// walk emits, the exhausted terminal, the last-good flag, that a client
// attaching mid-walk is re-synced on status, and that a superseded walk goes
// quiet rather than emitting over the connection that replaced it.

// attemptsFromEvent decodes a captured attempts event map back into the typed
// body, dropping the "event" discriminant.
func attemptsFromEvent(t *testing.T, ev map[string]any) attemptsEvent {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal attempts event: %v", err)
	}
	var out attemptsEvent
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode attempts event: %v", err)
	}
	return out
}

// statuses projects a snapshot's per-candidate statuses in order.
func statuses(s attemptsEvent) []string {
	out := make([]string, len(s.Items))
	for i, it := range s.Items {
		out[i] = it.Status
	}
	return out
}

func sameStatuses(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// awaitAttemptsWalk collects attempts snapshots until one carries a terminal
// outcome (ok or exhausted), returning the whole sequence in emission order.
func (h *harness) awaitAttemptsWalk() []attemptsEvent {
	h.t.Helper()
	var seq []attemptsEvent
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev["event"] != EventAttempts {
				continue
			}
			snap := attemptsFromEvent(h.t, ev)
			seq = append(seq, snap)
			if snap.Outcome != AttemptOutcomePending {
				return seq
			}
		case <-deadline:
			h.t.Fatalf("timed out collecting attempts walk; got %d snapshots", len(seq))
			return nil
		}
	}
}

// awaitAttemptsShowing returns the first attempts snapshot satisfying pred,
// draining intervening events.
func (h *harness) awaitAttemptsShowing(pred func(attemptsEvent) bool) attemptsEvent {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev["event"] != EventAttempts {
				continue
			}
			snap := attemptsFromEvent(h.t, ev)
			if pred(snap) {
				return snap
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for a matching attempts snapshot")
			return attemptsEvent{}
		}
	}
}

// TestAttemptsWalkEmitsSnapshotSequence: a walk that skips two blocked
// candidates before the third comes up emits the full waiting -> trying ->
// blocked -> ... -> ok snapshot sequence, opening with every candidate waiting in
// the resolved order and closing with the winner ok and outcome ok.
func TestAttemptsWalkEmitsSnapshotSequence(t *testing.T) {
	h := newHarness(t)
	h.runner.failStarts = 2 // vless, hy2 blocked; wg comes up third
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	seq := h.awaitAttemptsWalk()

	// The opening snapshot lists all three candidates waiting, seq'd 1..3 in the
	// DefaultOrder (vless, hy2, wg), with their real protocols and node IDs.
	first := seq[0]
	if first.Outcome != AttemptOutcomePending {
		t.Fatalf("opening outcome = %q, want pending", first.Outcome)
	}
	wantNode := []string{"vless-id", "hy2-id", "wg-id"}
	wantProto := []string{string(model.VLESS), string(model.Hysteria2), string(model.AmneziaWG)}
	if len(first.Items) != 3 {
		t.Fatalf("opening items = %d, want 3", len(first.Items))
	}
	for i, it := range first.Items {
		if it.Seq != i+1 {
			t.Errorf("item %d seq = %d, want %d", i, it.Seq, i+1)
		}
		if it.Node != wantNode[i] || it.Protocol != wantProto[i] {
			t.Errorf("item %d = %s/%s, want %s/%s", i, it.Protocol, it.Node, wantProto[i], wantNode[i])
		}
		if it.Status != AttemptWaiting {
			t.Errorf("opening item %d status = %q, want waiting", i, it.Status)
		}
	}

	// The full sequence a three-candidate walk (two blocked, third up) produces.
	want := [][]string{
		{AttemptWaiting, AttemptWaiting, AttemptWaiting}, // begin
		{AttemptTrying, AttemptWaiting, AttemptWaiting},  // trying vless
		{AttemptBlocked, AttemptWaiting, AttemptWaiting}, // vless blocked
		{AttemptBlocked, AttemptTrying, AttemptWaiting},  // trying hy2
		{AttemptBlocked, AttemptBlocked, AttemptWaiting}, // hy2 blocked
		{AttemptBlocked, AttemptBlocked, AttemptTrying},  // trying wg
		{AttemptBlocked, AttemptBlocked, AttemptOK},      // wg up
	}
	if len(seq) != len(want) {
		t.Fatalf("snapshot count = %d, want %d: %+v", len(seq), len(want), seq)
	}
	for i := range want {
		if got := statuses(seq[i]); !sameStatuses(got, want[i]) {
			t.Errorf("snapshot %d statuses = %v, want %v", i, got, want[i])
		}
		wantOutcome := AttemptOutcomePending
		if i == len(want)-1 {
			wantOutcome = AttemptOutcomeConnected
		}
		if seq[i].Outcome != wantOutcome {
			t.Errorf("snapshot %d outcome = %q, want %q", i, seq[i].Outcome, wantOutcome)
		}
	}
}

// TestAttemptsExhaustedWalkSnapshot: when every candidate is blocked the walk
// closes with an "exhausted" outcome and all candidates blocked, then reports the
// error state.
func TestAttemptsExhaustedWalkSnapshot(t *testing.T) {
	h := newHarness(t)
	h.runner.probeErr = errors.New("blocked") // every probe fails
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	seq := h.awaitAttemptsWalk()
	last := seq[len(seq)-1]
	if last.Outcome != AttemptOutcomeExhausted {
		t.Fatalf("terminal outcome = %q, want exhausted", last.Outcome)
	}
	for i, it := range last.Items {
		if it.Status != AttemptBlocked {
			t.Errorf("item %d status = %q, want blocked on exhaustion", i, it.Status)
		}
	}
	h.awaitState(StateError)
}

// TestExhaustedConnectEmitsSingboxTail: when a connect exhausts every candidate,
// the daemon surfaces the tail of the runner's sing-box log ring on the log
// channel, so a sing-box that failed to start or was rejected at config decode is
// diagnosable in the UI rather than hidden behind a bare "all protocols failed".
func TestExhaustedConnectEmitsSingboxTail(t *testing.T) {
	h := newHarness(t)
	h.runner.probeErr = errors.New("blocked") // every probe fails -> exhaustion
	h.runner.setLogs(
		"INFO[0000] sing-box started",
		`FATAL[0000] decode config: unknown field "jc"`,
	)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	// The distinctive FATAL line from the seeded ring must reach the log channel,
	// and it must precede the terminal error state (the tail is emitted before it).
	h.awaitLogContaining("unknown field")
	h.awaitState(StateError)
}

// TestAttemptsFlagsLastGoodLead: the profile's last-good node leads the walk and
// is the only item flagged last_good.
func TestAttemptsFlagsLastGoodLead(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)
	// Seed last-good so the WG node (normally last in preference order) leads.
	h.daemon.lastGood.Set(p.ID, "wg-id")

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	first := h.awaitAttemptsWalk()[0]
	if first.Items[0].Node != "wg-id" || !first.Items[0].LastGood {
		t.Errorf("lead item = %q last_good=%v, want wg-id/true", first.Items[0].Node, first.Items[0].LastGood)
	}
	flagged := 0
	for _, it := range first.Items {
		if it.LastGood {
			flagged++
		}
	}
	if flagged != 1 {
		t.Errorf("last_good flags = %d, want exactly 1", flagged)
	}
}

// TestAttemptsSingleCandidateWalk: an explicit-node connect runs the same loop
// with one candidate, emitting a one-item snapshot waiting -> trying -> ok. This
// is the shape a hot-swap re-apply also produces, keeping the UI uniform.
func TestAttemptsSingleCandidateWalk(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID, Node: "hy2-id"})
	h.await()

	seq := h.awaitAttemptsWalk()
	for _, s := range seq {
		if len(s.Items) != 1 || s.Items[0].Node != "hy2-id" {
			t.Fatalf("single-candidate snapshot = %+v, want one hy2-id item", s)
		}
	}
	want := [][]string{{AttemptWaiting}, {AttemptTrying}, {AttemptOK}}
	if len(seq) != len(want) {
		t.Fatalf("snapshot count = %d, want %d: %+v", len(seq), len(want), seq)
	}
	for i := range want {
		if got := statuses(seq[i]); !sameStatuses(got, want[i]) {
			t.Errorf("snapshot %d = %v, want %v", i, got, want[i])
		}
	}
	if seq[len(seq)-1].Outcome != AttemptOutcomeConnected {
		t.Errorf("terminal outcome = %q, want ok", seq[len(seq)-1].Outcome)
	}
}

// TestAttemptsAttachMidWalkResync: a client that sends status while a walk is in
// flight is re-pushed the current snapshot, so it sees the walk it joined late
// rather than only the steps after it subscribed.
func TestAttemptsAttachMidWalkResync(t *testing.T) {
	h := newHarness(t)
	// Hold the first candidate inside its probe long enough to attach mid-walk.
	h.daemon.probeBudget = 5 * time.Second
	h.daemon.probeTimeout = 5 * time.Second
	p := seedMultiProto(t, h)

	release := make(chan struct{})
	probing := make(chan struct{}, 1)
	h.runner.onProbe = func(ctx context.Context, n int) {
		if n != 1 {
			return
		}
		select {
		case probing <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()

	// Drain the opening + trying snapshots, then confirm the walk is parked.
	h.awaitAttemptsShowing(func(s attemptsEvent) bool {
		return s.Items[0].Status == AttemptTrying
	})
	select {
	case <-probing:
	case <-time.After(3 * time.Second):
		t.Fatal("probe never started")
	}

	// A fresh client re-syncs with status; the daemon must re-push the live walk.
	h.send(Request{ID: 2, Cmd: CmdStatus})
	var st State
	h.dataInto(h.await(), &st)
	if st.State != StateConnecting {
		t.Fatalf("mid-walk status state = %q, want connecting", st.State)
	}

	repush := attemptsFromEvent(t, h.awaitEvent(EventAttempts))
	if repush.Outcome != AttemptOutcomePending || len(repush.Items) != 3 || repush.Items[0].Status != AttemptTrying {
		t.Errorf("re-pushed snapshot = %+v, want the in-flight walk (first candidate trying)", repush)
	}
	close(release)
}

// TestAttemptsStatusSilentWhenIdle: a status re-sync with no walk in flight must
// not push an attempts event.
func TestAttemptsStatusSilentWhenIdle(t *testing.T) {
	h := newHarness(t)
	h.send(Request{ID: 1, Cmd: CmdStatus})
	h.await()
	select {
	case ev := <-h.events:
		if ev["event"] == EventAttempts {
			t.Errorf("idle status pushed an attempts event: %+v", ev)
		}
	case <-time.After(100 * time.Millisecond):
		// good: nothing pushed
	}
}

// TestAttemptsSupersededWalkStaysQuiet: a walk cancelled by a disconnect must not
// publish any further snapshot — a stale step over the torn-down connection would
// mislead a UI that has already moved on.
func TestAttemptsSupersededWalkStaysQuiet(t *testing.T) {
	h := newHarness(t)
	p := seedMultiProto(t, h)

	release := make(chan struct{})
	probing := make(chan struct{}, 1)
	h.runner.onProbe = func(ctx context.Context, n int) {
		select {
		case probing <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}

	h.send(Request{ID: 1, Cmd: CmdConnect, Profile: p.ID})
	h.await()
	select {
	case <-probing:
	case <-time.After(3 * time.Second):
		t.Fatal("probe never started")
	}

	h.send(Request{ID: 2, Cmd: CmdDisconnect})
	h.await()
	close(release)
	h.awaitState(StateIdle)

	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case ev := <-h.events:
			if ev["event"] == EventAttempts {
				t.Fatalf("superseded walk emitted an attempts snapshot: %+v", attemptsFromEvent(t, ev))
			}
		case <-deadline:
			return // good: silent after supersession
		}
	}
}

// TestEmitAttemptsGenerationGate locks the generation gate directly: a snapshot
// on the live generation publishes and is stored, while one on a stale generation
// is dropped and leaves the stored snapshot untouched — the mechanism that keeps a
// superseded walk from emitting over a newer one.
func TestEmitAttemptsGenerationGate(t *testing.T) {
	h := newHarness(t)
	h.daemon.mu.Lock()
	h.daemon.generation = 7
	h.daemon.mu.Unlock()

	cur := attemptsEvent{
		Items:   []attemptItem{{Seq: 1, Node: "a", Protocol: string(model.VLESS), Status: AttemptTrying}},
		Outcome: AttemptOutcomePending,
	}
	h.daemon.emitAttempts(7, cur)
	if got := attemptsFromEvent(t, h.awaitEvent(EventAttempts)); got.Items[0].Node != "a" {
		t.Fatalf("current-gen snapshot not published: %+v", got)
	}

	stale := attemptsEvent{
		Items:   []attemptItem{{Seq: 1, Node: "stale", Status: AttemptBlocked}},
		Outcome: AttemptOutcomeExhausted,
	}
	h.daemon.emitAttempts(6, stale)
	select {
	case ev := <-h.events:
		if ev["event"] == EventAttempts {
			t.Errorf("stale-gen snapshot was published: %+v", ev)
		}
	case <-time.After(100 * time.Millisecond):
	}

	h.daemon.mu.Lock()
	stored := h.daemon.attempts
	h.daemon.mu.Unlock()
	if stored == nil || stored.Items[0].Node != "a" {
		t.Errorf("stored snapshot = %+v, want the current-gen one (node a)", stored)
	}
}
