package control

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/zapret"
)

// TestTheConnectBypassBudgetFitsUnderTheBridge is the arithmetic the bug came
// down to, written where a change to either number will trip over it.
//
// The desktop bridge abandons a request at REQUEST_TIMEOUT
// (ui-desktop/src-tauri/src/backend/wire.rs) and tells the UI the connect
// failed. handleConnect runs on the strictly serial command loop, so everything
// it does before answering — the bypass raise included — is spent inside that
// minute. The raise used to cost a sixty-second download budget plus the
// runner's settle on top, which is over the line by construction: the first
// connect on a fresh install always failed, and always worked on the second
// press once the bundle was on disk.
//
// The compile-time assertion beside zapretConnectRaiseBudget catches the same
// thing at build time. This is the readable half — it names the numbers in the
// failure so whoever raised one can see what it has to fit inside.
func TestTheConnectBypassBudgetFitsUnderTheBridge(t *testing.T) {
	// The settle is inside the budget, not beside it: autoStartZapret runs under
	// the same context, and Runner.Start spends up to Settle waiting for winws to
	// attach. It is counted anyway, because a budget that only just fits with the
	// settle excluded is a budget that overruns the moment winws is slow.
	worst := zapretConnectRaiseBudget + zapret.DefaultSettle
	if worst >= bridgeRequestTimeout {
		t.Fatalf("a connect can spend %s raising the bypass (%s budget + %s settle), "+
			"but the desktop bridge gives up on the whole command at %s — "+
			"the connect would be reported failed while the daemon was still connecting",
			worst, zapretConnectRaiseBudget, zapret.DefaultSettle, bridgeRequestTimeout)
	}

	// And it has to leave a usable share of the minute to the connect itself,
	// which is the thing the user actually pressed the button for. Half is the
	// line check_nodes draws for the same reason (see defaultCheckBudget).
	if worst > bridgeRequestTimeout/2 {
		t.Errorf("the bypass raise may take %s of the bridge's %s, leaving the connect less than half",
			worst, bridgeRequestTimeout)
	}
}

// TestTheConnectRaiseGivesUpOnItsBudget: the bypass is allowed to make a connect
// slower, never to decide whether it happens.
//
// Every bypass operation is serialized on one lock and some of them are long — a
// strategy re-pick measures the whole bundle and holds it for minutes. Waiting on
// that with a plain Lock() ignores whatever deadline the caller was given, which
// is how a connect budgeted at ten seconds could still sit behind a re-pick until
// the bridge gave up on it. The wait has to end when the budget does.
func TestTheConnectRaiseGivesUpOnItsBudget(t *testing.T) {
	d, _ := newTestDaemon(t)
	stubStarts(d)
	events := captureLogs(t, d)
	d.zapretEmbed = func(string) ([]zapret.Strategy, error) {
		t.Error("unpacked a bundle while another bypass operation held the lock")
		return nil, nil
	}

	// Held for the whole test, standing in for the re-pick or the update that
	// would really be holding it.
	d.zapretOpMu.Lock()
	defer d.zapretOpMu.Unlock()

	// A parent deadline shorter than zapretConnectRaiseBudget: the wrapper derives
	// its own from this one, and the earlier of the two wins, so the real budget
	// stays the number that ships while the test still runs in milliseconds.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	if up := d.raiseZapretForConnectBounded(ctx); up {
		t.Fatal("reported the bypass up while the bundle lock was held by something else")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the raise waited %s on a busy lock; the budget is meant to end it", waited)
	}

	// Degradation, and said so: the connect goes ahead, and the log carries both
	// halves — that the clock is what stopped the bypass, and where the censored
	// services go instead. Silence here is the version of this bug the user cannot
	// tell from the app ignoring the button.
	if _, ok := loudest(*events, "не поднялся за"); !ok {
		t.Errorf("nothing said the bypass ran out of time: %v", *events)
	}
	if _, ok := loudest(*events, "пойдут через туннель"); !ok {
		t.Errorf("nothing said where YouTube and Discord end up: %v", *events)
	}
}

// TestAConnectAnswersWithNoNetworkAtAll is the regression test for the pressed
// button that reported "Отключено".
//
// Every network call the bypass path could make is wired to hang for longer than
// the bridge would wait. A raise that still finishes promptly is the proof that
// none of them is on this path any more — and that the bundle it leaves behind
// came from the bytes compiled into the binary.
func TestAConnectAnswersWithNoNetworkAtAll(t *testing.T) {
	d, _ := newTestDaemon(t)
	stubStarts(d)
	dir := filepath.Join(d.store.Dir(), zapretDirName)

	hang := make(chan struct{})
	defer close(hang)
	d.zapretLatest = func(ctx context.Context) (zapret.Release, error) {
		<-hang
		return zapret.Release{}, nil
	}
	d.zapretEmbed = func(target string) ([]zapret.Strategy, error) {
		return seedBundleAt(t, target, "1.10.2"), nil
	}

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		d.raiseZapretForConnect(context.Background())
		done <- time.Since(start)
	}()

	select {
	case took := <-done:
		if took > 10*time.Second {
			t.Fatalf("the bypass raise took %s with no network to be had", took)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the bypass raise never returned: something on the connect path is still waiting on the network")
	}

	if got := zapret.Version(dir); got != "1.10.2" {
		t.Errorf("installed version = %q, want the embedded 1.10.2", got)
	}
}

// TestABusyBundleLockDoesNotStallAConnectThatNeedsNothing covers the other half of
// the same wait: a bundle already on disk. The floor answers that without the
// lock at all, so a re-pick in progress cannot delay a connect that had nothing
// to install in the first place.
func TestABusyBundleLockDoesNotStallAConnectThatNeedsNothing(t *testing.T) {
	d, _ := newTestDaemon(t)
	dir := seedBundle(t, d, "1.10.2")

	d.zapretOpMu.Lock()
	defer d.zapretOpMu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.installEmbeddedZapretIfMissing(context.Background(), dir)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the floor queued behind a busy bypass lock with a bundle already installed")
	}
}
