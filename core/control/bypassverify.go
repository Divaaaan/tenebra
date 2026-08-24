package control

import (
	"context"
	"crypto/tls"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Divaaaan/tenebra/core/zapret"
)

// videoProbeHost/videoProbeSNI are what the bypass check dials and names in its
// ClientHello. A censor acts on the name, so the name is the measurement.
const (
	videoProbeHost = "www.youtube.com:443"
	videoProbeSNI  = "www.youtube.com"
)

// defaultBypassVerifyDelay is how long after a connect the bypass is checked.
//
// Long enough for winws to have attached and for the routing to have settled,
// short enough that a user who is about to open YouTube finds it already fixed
// rather than finding it broken and then fixed.
const defaultBypassVerifyDelay = 6 * time.Second

// bypassVerifyAttempts is how many times video is asked for before the bypass is
// declared not to be carrying it. One failure is a blip — a CDN hiccup, a
// half-attached filter; two in a row is a pattern, and the cost of being wrong
// here is handing a working direct path back to the tunnel for no reason.
const bypassVerifyAttempts = 2

// verifyBypass checks that the bypass is actually carrying video and, when it is
// not, hands the services it claimed back to the tunnel.
//
// Without this the failure is silent and total: routing sends YouTube and
// Discord to the direct path *because* the bypass is running, so a bypass that
// starts but does not work leaves exactly those services with no carrier at all
// — worse than never having run it, and indistinguishable from "the VPN is
// broken" from the user's side. The tunnel carries them slower and with ads,
// which is a bad outcome; carrying them nowhere is not an outcome.
//
// It runs once per connection rather than on a ticker. A bypass that works at
// connect time and dies later is the health watchdog's kind of problem, and
// re-testing forever would mean re-testing during the game the user is playing.
// bypassCheckSettings reads the delay and the probe together under the lock.
//
// Production writes both once, before anything serves, so a plain field read was
// safe in the shipped binary. Tests are the honest case: they set them on a
// daemon that is already connected, while the goroutine this check runs on is
// live — which is a real data race, and -race is right to call it. Reading a
// consistent pair here also means a test cannot see a new delay next to the old
// probe.
func (d *Daemon) bypassCheckSettings() (time.Duration, func(context.Context) bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.bypassVerifyDelay, d.bypassProbe
}

// setBypassCheck installs the delay and probe used by verifyBypass. Tests reach
// for it instead of assigning the fields, so the write is ordered against the
// read above.
func (d *Daemon) setBypassCheck(delay time.Duration, probe func(context.Context) bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bypassVerifyDelay = delay
	d.bypassProbe = probe
}

// setBypassDelay and setBypassProbe set one half each, for tests that change
// only one of the two.
func (d *Daemon) setBypassDelay(delay time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bypassVerifyDelay = delay
}

func (d *Daemon) setBypassProbe(probe func(context.Context) bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bypassProbe = probe
}

func (d *Daemon) verifyBypass(ctx context.Context, gen uint64) {
	delay, probe := d.bypassCheckSettings()
	if delay <= 0 || probe == nil {
		return // disabled (tests that do not exercise this path)
	}
	// Nothing was handed to the bypass, so there is nothing to verify. Checked
	// before the wait as well as after it: otherwise every connect on a machine
	// with no bypass parks a timer that shutdown then has to wait out.
	if !d.snapshotRouting().ZapretActive {
		return
	}

	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	if !d.isCurrent(gen) {
		return // superseded; a newer connection owns the state
	}
	// Read the flag late: the bypass may have been stopped while we waited, and
	// verifying something that is not running would report a failure that is
	// simply the user's choice.
	if !d.snapshotRouting().ZapretActive {
		return
	}

	for i := 0; i < bypassVerifyAttempts; i++ {
		if ctx.Err() != nil || !d.isCurrent(gen) {
			return
		}
		if probe(ctx) {
			return
		}
	}

	if !d.isCurrent(gen) {
		return
	}

	// Service first, optimisation second. The tunnel carries these domains slower
	// and with ads, but it carries them; leaving them on a bypass that is not
	// working leaves them carried by nothing while the app claims to be connected.
	d.emitLog(LogWarn, "обход не вытянул видео — увожу эти сервисы в туннель")
	d.fallBackToTunnel(ctx)

	// Then find out whether another strategy works. The bundle ships about twenty
	// because which one defeats a given ISP's DPI is discoverable only by trying,
	// and the one that worked last month is exactly what stops working when the
	// censor is updated. This is the difference between "YouTube broke, go read a
	// forum" and "YouTube was slow for a few minutes".
	d.repickStrategy(ctx, gen)
}

// repickStrategy measures every strategy in the bundle and, if one carries the
// control targets, puts the services back on the direct path behind it.
//
// It runs after the fallback rather than instead of it: probing takes minutes —
// each strategy needs the filter attached, several requests, and a clean detach —
// and the user should not spend those minutes with no video at all.
//
// A run that finds nothing leaves the tunnel arrangement in place, which is the
// honest outcome: the bypass has nothing to offer on this network today.
func (d *Daemon) repickStrategy(ctx context.Context, gen uint64) {
	if !d.bypassRepick {
		return // disabled (tests that are not about this path)
	}

	dir := filepath.Join(d.store.Dir(), zapretDirName)
	strategies := zapret.Discover(dir, dirFileNames(dir))
	if len(strategies) == 0 {
		return
	}

	d.emitLog(LogInfo, fmt.Sprintf("подбираю стратегию обхода — %d вариантов", len(strategies)))

	d.zapretOpMu.Lock()
	runner := d.newZapretRunner(dir)
	results, baseline, err := runner.Pick(ctx, strategies, zapret.DefaultTargets(), nil)
	d.zapretOpMu.Unlock()
	if err != nil || !d.isCurrent(gen) {
		return
	}

	best, found := zapret.Best(results, baseline)
	if !found {
		// The baseline goes in the message because this line is the one users
		// report, and on its own it cannot be told from the failure it replaced: a
		// baseline of full marks means the run measured a path where nothing is
		// blocked — the tunnel, most likely — and no strategy could have beaten it
		// whatever the bundle contains.
		d.emitLog(LogWarn, fmt.Sprintf(
			"ни одна стратегия не пробила блокировку (без обхода уже %d/%d) — остаёмся в туннеле",
			baseline, len(zapret.DefaultTargets())))
		return
	}

	d.zapretOpMu.Lock()
	started, startErr := runner.Start(ctx, best.Strategy)
	d.zapretOpMu.Unlock()
	if startErr != nil || !started || !d.isCurrent(gen) {
		d.emitLog(LogWarn, fmt.Sprintf("стратегия %s не запустилась", best.Name))
		return
	}

	d.mu.Lock()
	d.zapretActive = best.Name
	d.routing.ZapretActive = true
	d.routing.ZapretCovered = zapret.Covered(dir)
	d.refreshZapretStateLocked()
	d.mu.Unlock()
	d.persistSettings()

	d.emitLog(LogInfo, fmt.Sprintf("стратегия %s работает — возвращаю сервисы на прямой канал", best.Name))
	d.reapplyLive()
}

// bypassCarriesVideo reports whether the bypass is piercing the censor for video
// on the path it actually acts on.
//
// The probe is pinned to the physical link (see newPingDialer) and stops at the
// TLS handshake, because that is exactly the operation the DPI interferes with
// and exactly where the bypass intervenes. Asking over ordinary routing instead
// measures something else entirely: on a machine where another VPN owns the
// default route, the request leaves through *that* tunnel and says nothing about
// our bypass. It said "broken" on a working setup for that reason, and the
// fallback it triggered tore down a healthy tunnel — the check has to be about
// the thing it judges.
func (d *Daemon) defaultBypassProbe(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, serviceCheckTimeout)
	defer cancel()

	conn, err := d.dial(ctx, "tcp", videoProbeHost)
	if err != nil {
		return false
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsConn := tls.Client(conn, &tls.Config{ServerName: videoProbeSNI})
	return tlsConn.HandshakeContext(ctx) == nil
}

// fallBackToTunnel records that the bypass is not carrying what it claimed, so
// the next connect routes those services through the node instead.
//
// It deliberately does NOT rebuild the live tunnel. Rebuilding means stopping
// sing-box and starting a replacement, and on a live machine that is a real risk
// taken for an optimisation: the difference between YouTube going direct and
// YouTube going through the node is latency and ads, while a failed rebuild is
// no connection at all. Measured on the author's machine, that risk landed —
// twenty seconds after a healthy connect this check fired, the rebuild started a
// replacement that never came up, and the tunnel was gone while the app still
// said connected. Three separate debugging sessions started from that wreckage.
//
// So the verdict is recorded and reported, and the routing follows on the next
// connect. The filter itself is left running: stopping it is a separate decision
// with its own failure modes, and what was wrong is the routing assumption, not
// the process.
func (d *Daemon) fallBackToTunnel(ctx context.Context) {
	d.mu.Lock()
	already := !d.routing.ZapretActive
	d.routing.ZapretActive = false
	// Coverage read from the bundle's lists is what put these domains on the
	// direct path; clearing it with the flag keeps the two from disagreeing.
	d.routing.ZapretCovered = nil
	d.refreshZapretStateLocked()
	d.mu.Unlock()
	if already {
		return
	}
	d.persistSettings()
	d.emitLog(LogWarn, "маршрутизация обхода обновится при следующем подключении — живой туннель не трогаю")
}
