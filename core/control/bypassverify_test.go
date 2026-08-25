package control

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/zapret"
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
	d.setBypassDelay(time.Millisecond)
	armBypass(d)

	var asked int32
	d.setBypassProbe(func(context.Context) bool { atomic.AddInt32(&asked, 1); return false })

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
	d.setBypassDelay(time.Millisecond)
	armBypass(d)
	d.setBypassProbe(func(context.Context) bool { return true })

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
	d.setBypassDelay(time.Millisecond)
	armBypass(d)

	var n int32
	d.setBypassProbe(func(context.Context) bool { return atomic.AddInt32(&n, 1) != 1 })

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
	d.setBypassDelay(time.Millisecond)

	var asked int32
	d.setBypassProbe(func(context.Context) bool { atomic.AddInt32(&asked, 1); return false })

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
	d.setBypassDelay(time.Millisecond)
	armBypass(d)
	d.setBypassProbe(func(context.Context) bool { return false })

	stale := d.currentGeneration()
	d.mu.Lock()
	d.generation++
	d.mu.Unlock()

	d.verifyBypass(context.Background(), stale)

	if !d.snapshotRouting().ZapretActive {
		t.Error("a superseded verification rewrote the live routing")
	}
}

// pageOnly answers as a censor that leaves the YouTube page alone and strangles
// the stream — the ordinary Russian block, and the one this check was blind to.
func pageOnly(_ context.Context, name string) bool { return name == zapret.VideoPageHost }

// TestBypassFallsBackWhenOnlyThePageSurvives is the case this check used to get
// wrong.
//
// It asked www.youtube.com and nothing else, so a bypass that opened the page and
// carried no video passed — while routing had already sent video to the direct
// path *because* the bypass was believed to be working. The user was left with
// the one thing they installed this for broken, and the app saying connected.
func TestBypassFallsBackWhenOnlyThePageSurvives(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.setBypassDelay(time.Millisecond)
	armBypass(d)

	var asked int32
	d.setBypassProbe(func(ctx context.Context) bool {
		atomic.AddInt32(&asked, 1)
		return bypassCarriesEvery(ctx, bypassProbeTargets(), pageOnly)
	})

	d.verifyBypass(context.Background(), d.currentGeneration())

	if got := atomic.LoadInt32(&asked); got != bypassVerifyAttempts {
		t.Errorf("the bypass was asked %d times, want %d", got, bypassVerifyAttempts)
	}
	ro := d.snapshotRouting()
	if ro.ZapretActive {
		t.Error("a bypass that opens the page and carries no video is still credited with these services")
	}
	if len(ro.ZapretCovered) != 0 {
		t.Errorf("stale coverage survived the fallback: %v", ro.ZapretCovered)
	}
}

// TestBypassLeftAloneWhenPageAndStreamBothSurvive is the other half of the same
// rule: a bypass that carries everything asked of it must not be second-guessed.
// The direct path is the good outcome, and taking it away costs the latency the
// bypass exists to save.
func TestBypassLeftAloneWhenPageAndStreamBothSurvive(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.setBypassDelay(time.Millisecond)
	armBypass(d)
	d.setBypassProbe(func(ctx context.Context) bool {
		return bypassCarriesEvery(ctx, bypassProbeTargets(), func(context.Context, string) bool { return true })
	})

	d.verifyBypass(context.Background(), d.currentGeneration())

	ro := d.snapshotRouting()
	if !ro.ZapretActive {
		t.Error("a working bypass was handed back to the tunnel")
	}
	if len(ro.ZapretCovered) == 0 {
		t.Error("coverage was cleared even though the bypass works")
	}
}

// TestBypassNeedsEveryTarget pins the verdict rule itself: every name, not any
// of them.
//
// The asymmetry is the argument. A false failure costs an optimisation — the
// tunnel is slower and shows ads, and it carries the traffic. A false success
// costs the service outright, because routing has already sent it to a direct
// path on the strength of this verdict.
func TestBypassNeedsEveryTarget(t *testing.T) {
	cases := []struct {
		name string
		ask  func(context.Context, string) bool
		want bool
	}{
		{"page and stream both answer", func(context.Context, string) bool { return true }, true},
		{"the page opens, the stream does not", pageOnly, false},
		{"the stream answers, the page does not", func(_ context.Context, n string) bool {
			return n != zapret.VideoPageHost
		}, false},
		{"nothing answers", func(context.Context, string) bool { return false }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bypassCarriesEvery(context.Background(), bypassProbeTargets(), c.ask); got != c.want {
				t.Errorf("verdict = %v, want %v", got, c.want)
			}
		})
	}
	// Nothing measured is not a pass. An empty target list crediting the bypass
	// would disable the check silently, which is the failure mode above.
	if bypassCarriesEvery(context.Background(), nil, func(context.Context, string) bool { return true }) {
		t.Error("a check that asked for nothing reported a working bypass")
	}
}

// TestBypassProbeAsksTheStreamHostToo watches the real probe from underneath the
// dialer: it has to put both names on the wire, not just the page.
//
// No network is touched — every dial fails immediately — because what is being
// tested is which names are asked for, not what the internet answers.
func TestBypassProbeAsksTheStreamHostToo(t *testing.T) {
	d, _ := newTestDaemon(t)

	var mu sync.Mutex
	var dialed []string
	d.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, address)
		mu.Unlock()
		return nil, errors.New("no network in tests")
	}

	if d.defaultBypassProbe(context.Background()) {
		t.Fatal("the probe credited a bypass it never reached anything through")
	}

	mu.Lock()
	got := strings.Join(dialed, " ")
	mu.Unlock()
	for _, want := range []string{
		net.JoinHostPort(zapret.VideoPageHost, bypassProbePort),
		net.JoinHostPort(zapret.VideoStreamHost, bypassProbePort),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the bypass check never asked for %s; it dialed [%s]", want, got)
		}
	}
}

// TestBypassProbeTargetsMatchTheStrategyPick keeps the two lists from drifting.
//
// The pick scores strategies against zapret.DefaultTargets and this check judges
// the winner afterwards. If they stop asking for the same things, the pick can
// hand over a strategy the check then rejects — or, worse, the check can bless
// one the pick would have failed, which is how the page-only probe survived.
func TestBypassProbeTargetsMatchTheStrategyPick(t *testing.T) {
	scored := map[string]bool{}
	for _, target := range zapret.DefaultTargets() {
		u, err := url.Parse(target)
		if err != nil {
			t.Fatalf("parse %s: %v", target, err)
		}
		scored[u.Hostname()] = true
	}
	for _, name := range bypassProbeTargets() {
		if !scored[name] {
			t.Errorf("the check asks for %s, which no strategy is ever scored on", name)
		}
	}

	// And the pair has to stay a pair. Collapsing the stream host back onto a
	// youtube.com name would restore the exact blindness this fixes: the page
	// loading says nothing about whether video streams.
	if !strings.HasSuffix(zapret.VideoStreamHost, ".googlevideo.com") {
		t.Errorf("the video half is %q, which is not where video streams from", zapret.VideoStreamHost)
	}
	if zapret.VideoStreamHost == zapret.VideoPageHost {
		t.Error("the page and the stream are being measured by the same name")
	}
}
