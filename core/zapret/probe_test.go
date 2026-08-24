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
