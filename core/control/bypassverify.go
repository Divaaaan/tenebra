package control

import (
	"context"
	"time"
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
func (d *Daemon) verifyBypass(ctx context.Context, gen uint64) {
	delay := d.bypassVerifyDelay
	if delay <= 0 {
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
		if d.bypassCarriesVideo(ctx) {
			return
		}
	}

	if !d.isCurrent(gen) {
		return
	}
	d.emitLog(LogWarn, "обход не вытянул видео — увожу эти сервисы в туннель")
	d.fallBackToTunnel(ctx)
}

// bypassCarriesVideo asks for the video control target the way the machine's own
// routing would. With the bypass up that request goes out the physical link, so
// a success means the bypass is doing its job and a failure means it is not.
func (d *Daemon) bypassCarriesVideo(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, serviceCheckTimeout)
	defer cancel()
	_, err := d.httpGet(ctx, videoProbeURL)
	return err == nil
}

// fallBackToTunnel marks the bypass as not carrying anything and rebuilds the
// live tunnel so the services it claimed travel through the node instead.
//
// The filter itself is left running. Stopping it is a separate decision with its
// own failure modes (it also covers destinations outside this preset), and the
// thing that was broken is the routing assumption, not the process.
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
	d.reapplyLive()
}
