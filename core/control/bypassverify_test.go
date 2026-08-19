package control

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// currentGeneration reads the daemon's connection generation, the counter every
// background loop checks before touching live state.
func (d *Daemon) currentGeneration() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.generation
}

// armBypass puts the daemon in the state a successful connect leaves it in when
// the bypass came up: running, with coverage read from the bundle's lists.
func armBypass(d *Daemon) {
	d.mu.Lock()
	d.routing.ZapretActive = true
	d.routing.ZapretCovered = []string{"youtube.com", "discord.com"}
	d.mu.Unlock()
}

// TestBypassFallsBackToTunnelWhenVideoDoesNotSurvive is the failure this exists
// for: routing sends YouTube and Discord direct *because* the bypass is running,
// so a bypass that starts and does not work leaves them with no carrier at all —
// worse than never starting it, and from the user's side identical to "the VPN
// is broken".
func TestBypassFallsBackToTunnelWhenVideoDoesNotSurvive(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.bypassVerifyDelay = time.Millisecond
	armBypass(d)

	var asked int32
	d.httpGet = func(context.Context, string) ([]byte, error) {
		atomic.AddInt32(&asked, 1)
		return nil, errors.New("timeout")
	}

	d.verifyBypass(context.Background(), d.currentGeneration())

	if got := atomic.LoadInt32(&asked); got != bypassVerifyAttempts {
		t.Errorf("video was asked for %d times, want %d", got, bypassVerifyAttempts)
	}
	ro := d.snapshotRouting()
	if ro.ZapretActive {
		t.Error("routing still believes the bypass is carrying these services")
	}
	if len(ro.ZapretCovered) != 0 {
		t.Errorf("stale coverage survived the fallback: %v", ro.ZapretCovered)
	}
}

// TestBypassLeftAloneWhenVideoWorks: the direct path is the good outcome — it is
// the whole reason the bypass runs — so a working check must change nothing.
func TestBypassLeftAloneWhenVideoWorks(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.bypassVerifyDelay = time.Millisecond
	armBypass(d)
	d.httpGet = func(context.Context, string) ([]byte, error) { return nil, nil }

	d.verifyBypass(context.Background(), d.currentGeneration())

	ro := d.snapshotRouting()
	if !ro.ZapretActive {
		t.Error("a working bypass was handed back to the tunnel")
	}
	if len(ro.ZapretCovered) == 0 {
		t.Error("coverage was cleared even though the bypass works")
	}
}

// TestBypassRecoversAfterOneBlip: one failed request is a blip — a CDN hiccup, a
// filter still attaching. Acting on it would hand a working direct path back to
// the tunnel, costing the user the latency the bypass exists to save.
func TestBypassRecoversAfterOneBlip(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.bypassVerifyDelay = time.Millisecond
	armBypass(d)

	var n int32
	d.httpGet = func(context.Context, string) ([]byte, error) {
		if atomic.AddInt32(&n, 1) == 1 {
			return nil, errors.New("one blip")
		}
		return nil, nil
	}

	d.verifyBypass(context.Background(), d.currentGeneration())

	if !d.snapshotRouting().ZapretActive {
		t.Error("a single failed request was enough to give up on the bypass")
	}
}

// TestBypassNotVerifiedWhenItIsNotRunning: with no bypass the tunnel already
// carries these domains, so there is nothing to check — and checking anyway would
// park a timer on every connect for a machine that never uses the feature.
func TestBypassNotVerifiedWhenItIsNotRunning(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.bypassVerifyDelay = time.Millisecond

	var asked int32
	d.httpGet = func(context.Context, string) ([]byte, error) {
		atomic.AddInt32(&asked, 1)
		return nil, errors.New("should not be called")
	}

	d.verifyBypass(context.Background(), d.currentGeneration())

	if got := atomic.LoadInt32(&asked); got != 0 {
		t.Errorf("verified a bypass that was not running (%d requests)", got)
	}
}

// TestBypassVerifyStopsWhenSuperseded: a newer connection owns the state by then,
// and rewriting its routing from a stale generation is how one connect's verdict
// lands on another connect's session.
func TestBypassVerifyStopsWhenSuperseded(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.bypassVerifyDelay = time.Millisecond
	armBypass(d)
	d.httpGet = func(context.Context, string) ([]byte, error) { return nil, errors.New("timeout") }

	stale := d.currentGeneration()
	d.mu.Lock()
	d.generation++
	d.mu.Unlock()

	d.verifyBypass(context.Background(), stale)

	if !d.snapshotRouting().ZapretActive {
		t.Error("a superseded verification rewrote the live routing")
	}
}
