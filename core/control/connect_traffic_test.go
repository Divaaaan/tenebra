package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFrozenTrafficCountersAreReported is the regression test for a graph that
// stops moving and says nothing.
//
// The traffic poller treated every Stats error as "the API is not listening
// yet". That is true exactly once — before the first reading — and afterwards
// the same branch swallowed real failures forever. It is how the connection
// document outgrowing its read limit stayed invisible: every poll failed to
// parse, the counters froze, the tunnel kept carrying traffic, and nothing in
// the log connected the two. From the user's side that is indistinguishable
// from a dead tunnel, which is what it gets reported as.
func TestFrozenTrafficCountersAreReported(t *testing.T) {
	d, runner := newTestDaemon(t)

	var mu sync.Mutex
	var seen []LogEvent
	d.SetEmitter(func(name string, body any) {
		if name != "log" {
			return
		}
		if ev, ok := body.(LogEvent); ok {
			mu.Lock()
			seen = append(seen, ev)
			mu.Unlock()
		}
	})

	runner.setStats(1000, 2000)

	d.mu.Lock()
	gen := d.generation
	d.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.pollTraffic(ctx, gen)

	// Let one good reading land: a failure only counts as a stall once the
	// counters have been arriving.
	time.Sleep(2 * trafficPollInterval)
	runner.setStatsErr(errors.New("parse clash stats: unexpected end of JSON input"))

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, ev := range seen {
			if ev.Level == LogWarn && strings.Contains(ev.Msg, "счётчики трафика") {
				mu.Unlock()
				return // reported, which is the whole point
			}
		}
		mu.Unlock()
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the counters stopped answering and nothing was logged: " +
		"a frozen graph reads as a dead tunnel, and the log offers nothing to correct that")
}
