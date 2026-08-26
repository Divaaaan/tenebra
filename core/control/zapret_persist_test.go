package control

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/zapret"
)

// The bypass has to survive a restart of the daemon.
//
// It did not. It came up from exactly one place — connecting — so a machine that
// runs the bypass on the direct channel with no tunnel at all lost it whenever
// the service restarted: an app update, a reboot, a crash. Silently: the switch
// still read "on", the reported state was honest that no filter was running, and
// the first sign was video that stopped loading. Thirteen hours, once, before
// anyone pressed the button again.
//
// These drive the restart itself — the daemon's background job, which is the one
// thing that runs at every start — with a stand-in runner in place of winws. The
// real one hands a .bat to cmd.exe and loads the WinDivert driver, which is not
// something a test may do to the machine running it.

// stubStartRunner plays the bypass runner for a raise: it records what it was
// asked to launch and reports whether the filter came up, without going near
// winws. It is the start-side twin of stubPickRunner.
type stubStartRunner struct {
	mu sync.Mutex
	// started names every strategy the daemon asked to have launched, in order.
	started []string
	// tunnel records the tunnel state each raise was built for, which is what
	// decides the bundle's real-time-UDP block (see newZapretRunnerFor).
	tunnel []bool
	// dead makes every launch report that the filter did not come up, the shape
	// of winws refusing to start without administrator rights.
	dead bool
}

func (s *stubStartRunner) Start(_ context.Context, st zapret.Strategy) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = append(s.started, st.Name)
	return !s.dead, nil
}

// names copies out what has been launched so far.
func (s *stubStartRunner) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.started...)
}

// stubStarts routes every raise on d to a stand-in runner and hands it back.
func stubStarts(d *Daemon) *stubStartRunner {
	stub := &stubStartRunner{}
	d.newStartRunner = func(_ string, tunnelUp bool) startRunner {
		stub.mu.Lock()
		stub.tunnel = append(stub.tunnel, tunnelUp)
		stub.mu.Unlock()
		return stub
	}
	return stub
}

// launchBypassDaemon starts a daemon over one machine's directories: the store
// the bundle sits in, and the settings file the user's choices are written to.
// Calling it twice over the same pair is a restart — the daemon is new, the
// machine is not — which is the whole subject of this file.
func launchBypassDaemon(t *testing.T, storeDir, settingsDir string) (*Daemon, *stubStartRunner) {
	t.Helper()
	store, err := profile.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	d := NewDaemon(store, newFakeRunner())
	d.SetSettings(settingsAt(t, settingsDir))
	nets, err := OpenFileNetStrategies(storeDir)
	if err != nil {
		t.Fatalf("open network strategies: %v", err)
	}
	d.SetNetStrategies(nets)
	// One fixed network for the machine, so "what was measured on this network"
	// means something on a build machine whose real answer is "not recognisable".
	d.netFingerprint = func() string { return "test-network" }
	return d, stubStarts(d)
}

// seedBypassBundle lays an installed bundle in the machine's store: the default
// strategy every bundle carries, plus whichever others the test needs beside it.
func seedBypassBundle(t *testing.T, storeDir string, strategies ...string) {
	t.Helper()
	dir := filepath.Join(storeDir, zapretDirName)
	seedBundleAt(t, dir, "1.10.2")
	for _, name := range strategies {
		if err := os.WriteFile(filepath.Join(dir, name+".bat"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// daemonStart is the context the background job runs on here: already cancelled,
// so RunZapretAutoUpdate does its start-up work and then leaves the loop at its
// first select instead of parking a twelve-hour timer in the test binary. The
// same shape TestTheBypassArrivesWithoutConnecting uses.
func daemonStart() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// turnTheBypassOn presses the switch the way the UI does: start_zapret with the
// strategy named.
func turnTheBypassOn(t *testing.T, d *Daemon, strategy string) {
	t.Helper()
	resp := d.handleStartZapret(context.Background(), Request{ID: 1, Cmd: CmdStartZapret, Name: strategy})
	if !resp.Ok {
		t.Fatalf("start_zapret %q: %s", strategy, resp.Error)
	}
}

// The bug itself, end to end: the user turns the bypass on with no tunnel
// anywhere, the service restarts, and the bypass has to be running again without
// anyone touching anything.
func TestTheBypassComesBackUpAfterARestart(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")

	d, started := launchBypassDaemon(t, store, settings)
	turnTheBypassOn(t, d, "general (FAKE TLS AUTO)")
	if got := started.names(); len(got) != 1 || got[0] != "general (FAKE TLS AUTO)" {
		t.Fatalf("the switch launched %v, want the one strategy it was given", got)
	}

	// The restart. Nothing else happens: nobody connects, nobody presses
	// anything — that is exactly the machine this went wrong on.
	d2, restarted := launchBypassDaemon(t, store, settings)
	d2.RunZapretAutoUpdate(daemonStart())

	got := restarted.names()
	if len(got) == 0 {
		t.Fatal("the restart raised no bypass at all: the user is left unfiltered until they press the button themselves")
	}
	if len(got) != 1 || got[0] != "general (FAKE TLS AUTO)" {
		t.Errorf("the restart raised %v, want the strategy the bypass was running on", got)
	}
	// Routing has to move with it, or the censored services stay in a tunnel that
	// does not exist here while a packet filter carries nothing.
	if !d2.snapshotRouting().ZapretActive {
		t.Error("the bypass came up and routing was not told")
	}
	if st := d2.snapshotState(); !st.ZapretActive || st.ZapretStrategy != "general (FAKE TLS AUTO)" {
		t.Errorf("reported state after the restart = (active %v, strategy %q), want the running bypass",
			st.ZapretActive, st.ZapretStrategy)
	}
}

// The other half of the same rule: a machine whose owner has never touched the
// bypass switch must come up with no packet filter running. "Nobody has said" is
// not "yes", and a kernel filter appearing on its own is a surprise rather than a
// restoration — which is why the stored answer is a pointer and why an absent one
// is left absent.
func TestARestartRaisesNothingForAUserWhoNeverTurnedTheBypassOn(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")

	// A settings file written by some other preference, so the test is about an
	// absent bypass answer rather than about an absent file.
	d, started := launchBypassDaemon(t, store, settings)
	d.persistSettings()
	raw, err := os.ReadFile(filepath.Join(settings, settingsFile))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if bytes.Contains(raw, []byte("zapret_enabled")) {
		t.Errorf("a switch nobody touched was written down as an answer: %s", raw)
	}
	if got := started.names(); len(got) != 0 {
		t.Fatalf("something launched %v before the restart", got)
	}

	d2, restarted := launchBypassDaemon(t, store, settings)
	d2.RunZapretAutoUpdate(daemonStart())

	if got := restarted.names(); len(got) != 0 {
		t.Errorf("the restart started %v on a machine whose owner never turned the bypass on", got)
	}
	if d2.snapshotRouting().ZapretActive {
		t.Error("routing believes a bypass is running that was never started")
	}
}

// And a user who turned it off stays off. This is the case the stored false
// exists for: it has to overwrite the stored true, or every launch would put back
// a bypass the user had just switched off.
func TestARestartRaisesNothingAfterTheUserTurnedTheBypassOff(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")

	d, _ := launchBypassDaemon(t, store, settings)
	turnTheBypassOn(t, d, "general (FAKE TLS AUTO)")
	if resp := d.handleStopZapret(context.Background(), Request{ID: 2, Cmd: CmdStopZapret}); !resp.Ok {
		t.Fatalf("stop_zapret: %s", resp.Error)
	}

	d2, restarted := launchBypassDaemon(t, store, settings)
	d2.RunZapretAutoUpdate(daemonStart())

	if got := restarted.names(); len(got) != 0 {
		t.Errorf("the restart started %v after the user switched the bypass off", got)
	}
	if d2.snapshotRouting().ZapretActive {
		t.Error("routing believes a bypass is running that the user switched off")
	}
}

// The distinction the whole design rests on: the video check dropping the bypass
// is the system reporting that tonight's censor won, not the user changing their
// mind. It takes the censored services back into the tunnel — that part is
// right — but it must leave the switch alone, or one failed check would erase the
// setting for good and every restart after it would come up bare.
func TestAFailedVideoCheckDoesNotEraseTheUsersBypass(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")

	d, _ := launchBypassDaemon(t, store, settings)
	turnTheBypassOn(t, d, "general (FAKE TLS AUTO)")

	// What verifyBypass does when the bypass stops carrying video.
	d.fallBackToTunnel(context.Background())
	if d.snapshotRouting().ZapretActive {
		t.Fatal("the fallback did not take the services back into the tunnel; the test proves nothing")
	}

	d2, restarted := launchBypassDaemon(t, store, settings)
	d2.RunZapretAutoUpdate(daemonStart())

	got := restarted.names()
	if len(got) == 0 {
		t.Fatal("a failed video check unset the bypass: the restart left the user with nothing running")
	}
	if len(got) != 1 || got[0] != "general (FAKE TLS AUTO)" {
		t.Errorf("the restart raised %v, want the strategy the user had on", got)
	}
}

// A restart has to land on the same strategy a connect would, and that is the
// one measured on THIS network — not the one stored globally, which is whatever
// won on whichever network was picked on last. A laptop carried from home to a
// cafe would otherwise come up on the home answer, running a filter that does
// nothing there while routing sends video down the direct path because a bypass
// is "on".
func TestARestartRaisesTheStrategyRememberedForThisNetwork(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (ALT2)", "general (FAKE TLS AUTO)")

	d, _ := launchBypassDaemon(t, store, settings)
	// The stored, machine-wide answer.
	turnTheBypassOn(t, d, "general (ALT2)")
	// What a probe run on this network measured, which is the sharper answer.
	d.rememberStrategyForThisNetwork("general (FAKE TLS AUTO)")

	d2, restarted := launchBypassDaemon(t, store, settings)
	d2.RunZapretAutoUpdate(daemonStart())

	got := restarted.names()
	if len(got) != 1 {
		t.Fatalf("the restart raised %v, want exactly one strategy", got)
	}
	if got[0] != "general (FAKE TLS AUTO)" {
		t.Errorf("the restart raised %q, want the strategy measured on this network", got[0])
	}
}

// The boundary in the other direction, and the reason the connect path writes
// nothing down: a connect raises the bypass because a connect is happening, not
// because anybody asked for a bypass. Recording it would arm this restore for
// someone who only ever presses Connect, and leave a packet filter running on a
// machine that came up without connecting to anything — while a machine that
// does autoconnect raises the bypass on its way to the tunnel anyway, exactly as
// it did before.
func TestAConnectDoesNotArmTheStartUpRestoreByItself(t *testing.T) {
	store, settings := t.TempDir(), t.TempDir()
	seedBypassBundle(t, store, "general (FAKE TLS AUTO)")

	d, raised := launchBypassDaemon(t, store, settings)
	if !d.raiseZapretForConnect(context.Background()) {
		t.Fatal("the connect did not raise the bypass; the test proves nothing")
	}
	if got := raised.names(); len(got) != 1 {
		t.Fatalf("the connect launched %v, want one strategy", got)
	}

	d2, restarted := launchBypassDaemon(t, store, settings)
	d2.RunZapretAutoUpdate(daemonStart())

	if got := restarted.names(); len(got) != 0 {
		t.Errorf("the restart started %v for a user who only ever connected", got)
	}
}
