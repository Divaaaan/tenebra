package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

type serviceReply struct {
	Checks []ServiceCheck `json:"checks"`
}

func runServiceCheck(t *testing.T, d *Daemon) map[string]ServiceCheck {
	t.Helper()
	resp := d.handleCheckServices(context.Background(), Request{ID: 1, Cmd: CmdCheckServices})
	if resp.Error != "" {
		t.Fatalf("check_services: %s", resp.Error)
	}
	var out serviceReply
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	by := make(map[string]ServiceCheck, len(out.Checks))
	for _, c := range out.Checks {
		by[c.Service] = c
	}
	return by
}

// TestCheckServicesAnswersAllThree: the check exists to replace "connected" with
// something a user can act on, so it must always report every service — a
// missing one reads as "unknown", which is the state it was built to remove.
func TestCheckServicesAnswersAllThree(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.httpGet = func(context.Context, string) ([]byte, error) { return nil, nil }
	d.dial = func(context.Context, string, string) (net.Conn, error) { return fakeConn{}, nil }

	got := runServiceCheck(t, d)

	for _, name := range []string{"video", "voice", "games"} {
		c, ok := got[name]
		if !ok {
			t.Errorf("no check reported for %q", name)
			continue
		}
		if c.Detail == "" {
			t.Errorf("%s: no destination named, so a failure could not be repeated by hand", name)
		}
	}
}

// TestCheckServicesReportsAFailureAsFailure: a censored destination must come
// back not-ok rather than as a zero latency that reads like success.
func TestCheckServicesReportsAFailureAsFailure(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.httpGet = func(context.Context, string) ([]byte, error) { return nil, errors.New("timeout") }
	d.dial = func(context.Context, string, string) (net.Conn, error) { return fakeConn{}, nil }

	got := runServiceCheck(t, d)

	if got["video"].OK {
		t.Error("video reported ok while its request failed")
	}
	if got["video"].RTTMs != 0 {
		t.Errorf("failed video check carries a latency of %d ms", got["video"].RTTMs)
	}
	if !got["games"].OK {
		t.Error("games was dragged down by an unrelated failure")
	}
}

// TestCheckServicesMeasuresGamesOnThePhysicalLink: the games number is the whole
// justification for keeping game traffic out of the tunnel, so it has to be
// measured with the pinned dialer. An ordinary dial would be captured by the tun
// and report the trip to the local device — a fabulous ping that means nothing.
func TestCheckServicesMeasuresGamesOnThePhysicalLink(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.httpGet = func(context.Context, string) ([]byte, error) { return nil, nil }

	used := false
	d.dial = func(_ context.Context, _, addr string) (net.Conn, error) {
		used = true
		if addr != gamesProbeHost {
			t.Errorf("games probe dialled %q, want %q", addr, gamesProbeHost)
		}
		return fakeConn{}, nil
	}

	runServiceCheck(t, d)

	if !used {
		t.Error("the games check did not go through the pinned dialer")
	}
}

// TestCheckServicesRunsProbesConcurrently: three sequential timeouts would take
// long enough that the UI would have to explain the wait. They are independent,
// so they overlap.
func TestCheckServicesRunsProbesConcurrently(t *testing.T) {
	d, _ := newTestDaemon(t)
	const delay = 300 * time.Millisecond
	d.httpGet = func(ctx context.Context, _ string) ([]byte, error) {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
		return nil, nil
	}
	d.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
		return fakeConn{}, nil
	}

	start := time.Now()
	runServiceCheck(t, d)
	elapsed := time.Since(start)

	// Sequential would be three delays; concurrent is about one. The bound is
	// deliberately loose — this asserts "not serial", not a stopwatch.
	if elapsed > 2*delay {
		t.Errorf("three probes took %v, want about %v — they ran one after another", elapsed, delay)
	}
}
