package control

import (
	"context"
	"fmt"
	"time"

	"github.com/Divaaaan/tenebra/core/zapret"
)

// bridgeRequestTimeout is REQUEST_TIMEOUT in
// ui-desktop/src-tauri/src/backend/wire.rs. The desktop bridge abandons a
// request after it and reports the command as failed, whatever the daemon is
// still doing about it.
//
// It is a ceiling, not a timeout the daemon can negotiate with: past it the
// caller has already been told the command failed, so anything the handler
// achieves afterwards lands in a UI that has moved on. Every handler served on
// the strictly serial command loop is therefore working under it, and every
// budget carved out inside one of those handlers is carved out of this
// (defaultCheckBudget in nodecheck.go is the other one).
const bridgeRequestTimeout = 60 * time.Second

// zapretConnectRaiseBudget bounds the whole bypass raise that a connect performs
// before it starts building the tunnel: putting the bundle on disk, then
// starting a strategy.
//
// It exists because that work used to have no ceiling of its own and went
// straight through the bridge's. A first connect on a fresh install found no
// bundle, went to GitHub for one under a sixty-second budget, and only then
// waited for winws to attach — over a minute in total, on the command loop, so
// the bridge gave up first. The user got "Отключено" and a toast from a connect
// the daemon was still cheerfully performing, and the second press worked
// because by then the bundle was on disk. The same block held the loop, so a
// status or a disconnect pressed during it went unanswered too.
//
// Ten seconds is what the work costs now that the network is off this path (see
// raiseZapretForConnect): unpacking the compiled-in bundle is a second or two of
// local I/O, and starting a strategy is bounded by the runner's settle. The rest
// of the bridge's minute is left to the connect itself, which is the thing the
// user pressed the button for.
const zapretConnectRaiseBudget = 10 * time.Second

// Compile-time proof that a connect's bypass work fits under the bridge with the
// runner's settle counted in — the settle is spent inside the budget on Windows
// and is zero everywhere else, so this is the real worst case, not an estimate.
// An unsigned conversion cannot hold a negative value, so raising either budget
// past the ceiling (or lowering the ceiling to match a changed wire.rs) fails the
// build here instead of turning into a bug report about a button that does
// nothing.
const _ = uint64(bridgeRequestTimeout - (zapretConnectRaiseBudget + zapret.DefaultSettle))

// zapretOpAcquirePoll is how often a bounded wait re-tries the bypass lock.
//
// Polling rather than blocking is the price of bounding it at all: zapretOpMu is
// a plain mutex and Lock() cannot be given up on, so a caller with a deadline has
// to ask repeatedly. The interval is small enough to be invisible next to the
// seconds the budget is measured in, and the loop only ever runs while another
// bypass operation is genuinely in progress — the uncontended case takes the lock
// on the first TryLock and never starts a ticker.
const zapretOpAcquirePoll = 25 * time.Millisecond

// acquireZapretOp takes the bypass lock, or gives up when ctx does.
//
// Every bypass operation is serialized on that lock, and some of them are long:
// a strategy re-pick holds it for minutes while it measures the whole bundle, and
// an update holds it for as long as GitHub takes to answer. Waiting on it with
// plain Lock() therefore ignores the caller's deadline entirely — which is how a
// connect budgeted at ten seconds could still sit for minutes behind a re-pick,
// with the timeout it was given doing nothing at all.
//
// A caller whose ctx carries no deadline (the background jobs, the start-up
// restore) waits exactly as Lock() would; there is nothing to give up on.
//
// Reports whether the lock is held. The caller unlocks it on true and must not
// on false.
func (d *Daemon) acquireZapretOp(ctx context.Context) bool {
	if d.zapretOpMu.TryLock() {
		return true
	}
	ticker := time.NewTicker(zapretOpAcquirePoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if d.zapretOpMu.TryLock() {
				return true
			}
		}
	}
}

// raiseZapretForConnectBounded is raiseZapretForConnect under
// zapretConnectRaiseBudget, and it is how both connect paths reach it.
//
// The budget is applied here rather than inside raiseZapretForConnect because it
// is a fact about the connect, not about the bypass: this is the one caller that
// has a user watching a button and a bridge counting to sixty. The start-up
// restore raises the same bypass with nobody waiting and wants no clock.
//
// Overrunning is a degradation, never a refusal. The connect goes ahead either
// way: the tunnel carries the censored services by itself — slower, which is the
// cost the bypass exists to avoid, but carried — whereas refusing the connect
// would trade a slower YouTube for no VPN at all. The background updater keeps
// its own schedule and will have the bundle down long before the user presses
// anything a second time.
func (d *Daemon) raiseZapretForConnectBounded(ctx context.Context) bool {
	bounded, cancel := context.WithTimeout(ctx, zapretConnectRaiseBudget)
	defer cancel()

	up := d.raiseZapretForConnect(bounded)
	// Only when the clock is what stopped it. raiseZapretForConnect already says
	// where the traffic ends up in either case; this line is the missing half —
	// that nothing failed, the bypass simply did not finish in time, which is not
	// something the user should have to infer from a connect that worked.
	if !up && bounded.Err() != nil {
		d.emitLog(LogWarn, fmt.Sprintf(
			"zapret: обход не поднялся за %s — подключаюсь без него", zapretConnectRaiseBudget))
	}
	return up
}
