package zapret

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// alwaysAnswers stands in for a path where every control target replies — which
// is exactly what a tunnel looks like to a probe that gets captured by it.
func alwaysAnswers(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestProbeDialsThroughTheGivenDialer pins the seam itself: when a dialer is
// supplied, every probe connection is opened by it. Without that the transport
// dials by the routing table, which is how the whole measurement ended up
// running through the tun.
func TestProbeDialsThroughTheGivenDialer(t *testing.T) {
	srv := alwaysAnswers(t)

	var dials int32
	r := &Runner{
		ProbeTimeout: 5 * time.Second,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			atomic.AddInt32(&dials, 1)
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}

	targets := []string{srv.URL + "/a", srv.URL + "/b"}
	got := Result{Targets: r.Probe(context.Background(), targets)}

	if n := atomic.LoadInt32(&dials); int(n) != len(targets) {
		t.Errorf("the supplied dialer opened %d connections, want %d: the probe dialed around it",
			n, len(targets))
	}
	if got.OKCount() != len(targets) {
		t.Errorf("probe scored %d/%d against a server that answers everything",
			got.OKCount(), len(targets))
	}
}

// TestProbeMeasuresThePhysicalPathNotTheTunnel is the bug this seam exists for.
//
// With the tunnel up and no dialer, the baseline is measured through the tun:
// every censored target answers, the baseline is full marks, and Best — which
// reports a strategy only when it BEATS the baseline — can never report one. The
// automatic re-pick after a bypass failure then always says "no strategy pierced
// the block", on a machine where a strategy would have.
//
// Dialed on the physical path, where the censor actually sits, the baseline is
// what it should be and the same strategy wins.
func TestProbeMeasuresThePhysicalPathNotTheTunnel(t *testing.T) {
	srv := alwaysAnswers(t)
	targets := []string{srv.URL + "/1", srv.URL + "/2", srv.URL + "/3"}
	ctx := context.Background()

	// Captured by the tun: everything is reachable, whatever the censor does.
	throughTunnel := &Runner{ProbeTimeout: 5 * time.Second}
	tunnelBaseline := Result{Targets: throughTunnel.Probe(ctx, targets)}.OKCount()

	// Bound to the physical uplink, where the block is: nothing gets through.
	blocked := &Runner{
		ProbeTimeout: 5 * time.Second,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("reset by the censor on the direct path")
		},
	}
	physicalBaseline := Result{Targets: blocked.Probe(ctx, targets)}.OKCount()

	if tunnelBaseline != len(targets) {
		t.Fatalf("unbound baseline = %d/%d, want all: the test's stand-in tunnel is not answering",
			tunnelBaseline, len(targets))
	}
	if physicalBaseline != 0 {
		t.Fatalf("baseline over the blocked physical path = %d/%d, want 0",
			physicalBaseline, len(targets))
	}

	// A strategy that carries every target, scored the way Pick scores one.
	winner := Result{Name: "general", Started: true, Targets: throughTunnel.Probe(ctx, targets)}

	if _, found := Best([]Result{winner}, tunnelBaseline); found {
		t.Error("a baseline taken through the tunnel let a strategy be reported; the arithmetic changed")
	}
	if _, found := Best([]Result{winner}, physicalBaseline); !found {
		t.Error("against a baseline measured on the physical path, a strategy that unblocks " +
			"everything was still not reported — the re-pick can never repair anything")
	}
}

// TestProbeWithoutADialerStillMeasures keeps the degradation honest: a Runner
// with no dialer (no physical interface to bind to, or a platform without the
// bind primitive) must fall back to ordinary routing rather than refusing to
// measure. A zero ProbeTimeout must not silently cancel every request either —
// the non-Windows constructor sets no timings at all.
func TestProbeWithoutADialerStillMeasures(t *testing.T) {
	srv := alwaysAnswers(t)

	got := Result{Targets: (&Runner{}).Probe(context.Background(), []string{srv.URL})}
	if got.OKCount() != 1 {
		t.Errorf("probe with no dialer and no timings scored %d/1; it measured nothing at all",
			got.OKCount())
	}
}

// TestProbeSpendsOneBudgetOnEveryTarget is the arithmetic the whole strategy
// pick was paying for.
//
// A blocked destination does not answer "no": it hangs until the budget runs
// out. Asked one after another, five blocked targets cost five full budgets —
// and a run measures every strategy in the bundle, so that multiplies by twenty.
// The wall clock is the measurement here: with a single shared budget the whole
// set costs one of them, whatever the targets do.
func TestProbeSpendsOneBudgetOnEveryTarget(t *testing.T) {
	const budget = 300 * time.Millisecond
	targets := []string{
		"http://one.invalid/", "http://two.invalid/", "http://three.invalid/",
		"http://four.invalid/", "http://five.invalid/",
	}

	// Every dial hangs until its context is cancelled — a target the censor drops
	// on the floor, which is the case that costs.
	r := &Runner{
		ProbeTimeout: budget,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	start := time.Now()
	got := Result{Targets: r.Probe(context.Background(), targets)}
	elapsed := time.Since(start)

	if got.OKCount() != 0 {
		t.Fatalf("probe scored %d/%d against dials that never connect", got.OKCount(), len(targets))
	}
	if elapsed > 2*budget {
		t.Errorf("probing %d blocked targets took %v, want about one %v budget: they were measured one after another",
			len(targets), elapsed.Round(time.Millisecond), budget)
	}
}

// TestProbeReportsTargetsInTheOrderTheyWereAsked pins the slice's shape against
// the obvious way to make the probe concurrent.
//
// Callers read the results positionally — the UI lists them against the target
// list it asked for, and the diagnostics bundle prints the pair — so a set
// collected in completion order silently relabels every measurement: the fast
// destination's round-trip filed under the slow one's name. Nothing errors, and
// the report is wrong.
func TestProbeReportsTargetsInTheOrderTheyWereAsked(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(slow.Close)
	fast := alwaysAnswers(t)

	// The first target is the last to answer, so completion order is not ask order.
	targets := []string{slow.URL + "/slow", fast.URL + "/a", fast.URL + "/b"}
	out := (&Runner{ProbeTimeout: 5 * time.Second}).Probe(context.Background(), targets)

	if len(out) != len(targets) {
		t.Fatalf("probe returned %d results, want %d", len(out), len(targets))
	}
	for i, want := range targets {
		if out[i].Target != want {
			t.Errorf("result %d is %q, want %q: the results came back in completion order",
				i, out[i].Target, want)
		}
		if !out[i].OK {
			t.Errorf("result %d (%s) did not complete against a server that answers everything",
				i, out[i].Target)
		}
	}
}
