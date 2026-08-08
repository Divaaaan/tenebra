package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// dpiRunner owns the local DPI-bypass engine: a userspace SOCKS5 proxy that
// reshapes the connections handed to it (segmenting the ClientHello and friends)
// so a censor's inspection misses what it is looking for. sing-box routes the
// destinations smart routing keeps off the tunnel into its loopback port.
//
// Like Runner it is declared here, on the consumer side, so control keeps
// depending on nothing OS-specific and the dependency arrow stays adapter ->
// control. It is deliberately NOT Runner: the engine has no clash API, so there
// is nothing to poll for stats or probe through.
//
// A dpiRunner is single-use per Start/Stop cycle but reusable across cycles.
// Implementations must be safe for concurrent calls to Done/Logs alongside
// Start/Stop, since the daemon's watcher goroutine holds a Done channel while the
// command loop may Stop the process under it.
type dpiRunner interface {
	// Start launches the engine with the given argv. It returns once the process
	// has been spawned; a later failure is reported through Done.
	Start(ctx context.Context, args []string) error
	// Stop terminates the process and waits for it to exit. It is idempotent.
	Stop() error
	// Done delivers the process's exit: it sends the exit error (nil on a clean
	// exit) once and is closed afterwards. Before any Start it must block.
	Done() <-chan error
	// Logs returns a copy of the most recent engine output lines (newest last).
	Logs() []string
}

// dpiReadyWaiter is the optional readiness half of a dpiRunner: an engine that
// can prove its listener is serving before anything is routed into it. Start only
// proves the process was spawned, and the port is chosen before the process binds
// it, so an unrelated local listener that claimed the port in between would
// silently receive the bypassed traffic. A runner that offers this is asked; one
// that doesn't still works, it just starts sing-box a beat earlier.
type dpiReadyWaiter interface {
	WaitReady(ctx context.Context, addr string, within time.Duration) error
}

// errNoDPIEngine is what every bypass operation fails with when no engine was
// installed — a platform we have no build for, or a package missing the binary.
// It is a normal state, not a defect: the command says so and nothing else in the
// daemon changes behaviour.
var errNoDPIEngine = errors.New("no DPI bypass engine available on this platform")

// dpiEngineTailLines is how many trailing engine output lines a failure surfaces
// into the UI log — enough to show why it would not start without flooding it.
const dpiEngineTailLines = 10

// dpiReadyBudget bounds the wait for the engine's listener. It is short on
// purpose: a local process either binds a loopback port within a moment or is
// never going to, and the wait sits on the connect path.
const dpiReadyBudget = 3 * time.Second

// dpiLoopbackHost is the address the bypass listener is reached at. It matches
// what the config builder makes sing-box dial (singbox.dpiOutbound), which is the
// address the readiness check must therefore verify — checking any other would
// prove nothing about the connections that will actually be made.
const dpiLoopbackHost = "127.0.0.1"

// SetDPIRunner installs the DPI-bypass engine: the process runner, the loopback
// socks port it listens on, and the argv it runs under. main resolves all three
// (binary lookup, free port, strategy) and wires them here before serving; the
// port must be the one the argv makes the engine bind, since it is also what the
// config builder points the bypass outbound at. Leaving the engine unset keeps
// the feature honestly unavailable — set_dpi_bypass then fails instead of arming
// a toggle nothing implements. Call it before serving: it is not synchronised
// against an in-flight connect.
func (d *Daemon) SetDPIRunner(r dpiRunner, port int, args []string) {
	d.dpiMu.Lock()
	defer d.dpiMu.Unlock()
	d.dpi = r
	d.dpiPort = port
	d.dpiArgs = append([]string(nil), args...)
}

// dpiAvailable reports whether an engine is installed at all.
func (d *Daemon) dpiAvailable() bool {
	d.dpiMu.Lock()
	defer d.dpiMu.Unlock()
	return d.dpi != nil
}

// dpiUp reports whether the engine process is currently running, which is what
// makes an armed preference effective. Everything that decides whether a config
// may route into the bypass port asks this rather than the preference alone.
func (d *Daemon) dpiUp() bool {
	d.dpiMu.Lock()
	defer d.dpiMu.Unlock()
	return d.dpiRunning
}

// startDPIBypass brings the engine up, or reports why it cannot. It is idempotent:
// an already-running engine is left alone, so a connect, a hot swap and the toggle
// itself can all call it without churning the process.
//
// The engine lives outside the connection's generation protocol on purpose. That
// protocol (teardown, d.generation, d.wg) is written for exactly one process — the
// tunnel — and every node switch runs it; hanging the engine off it would kill and
// respawn the bypass on every reconnect, dropping every connection it carries.
// Hence its own mutex and its own watcher. dpiMu is never held while waiting on
// connMu, and connMu holders may take it, so the order is connMu -> dpiMu -> mu.
func (d *Daemon) startDPIBypass() error {
	d.dpiMu.Lock()
	defer d.dpiMu.Unlock()

	if d.dpi == nil {
		return errNoDPIEngine
	}
	if d.dpiRunning {
		return nil
	}

	d.setDPIStatus(DPIStatusStarting)
	ctx, cancel := context.WithCancel(context.Background())
	if err := d.dpi.Start(ctx, d.dpiArgs); err != nil {
		cancel()
		d.setDPIStatus(DPIStatusFailed)
		d.emitDPITail()
		return err
	}

	// Wait for the listener to answer where the config will dial it. A runner that
	// cannot prove this is not left half-started: the process is reaped and the
	// caller folds the bypass out, because routing traffic into a port we have not
	// identified is worse than not bypassing at all.
	if w, ok := d.dpi.(dpiReadyWaiter); ok {
		addr := net.JoinHostPort(dpiLoopbackHost, strconv.Itoa(d.dpiPort))
		if err := w.WaitReady(ctx, addr, dpiReadyBudget); err != nil {
			_ = d.dpi.Stop()
			cancel()
			d.setDPIStatus(DPIStatusFailed)
			d.emitDPITail()
			return err
		}
	}

	// Claim the new instance under the same lock that the watcher will re-read, so
	// a watcher left over from a previous instance can tell it has been superseded.
	d.dpiGen++
	gen := d.dpiGen
	d.dpiRunning = true
	d.dpiCancel = cancel
	d.dpiStop = make(chan struct{})
	done := d.dpi.Done()
	stopped := d.dpiStop
	d.setDPIStatus(DPIStatusRunning)

	d.dpiWG.Add(1)
	go func() {
		defer d.dpiWG.Done()
		d.watchDPIBypass(gen, done, stopped)
	}()
	return nil
}

// stopDPIBypass terminates the engine if it is running. It is idempotent and
// safe with no engine installed. It does not wait for the watcher goroutine —
// that would deadlock, since the watcher takes dpiMu when it wakes; Close waits
// for it after this returns.
func (d *Daemon) stopDPIBypass() {
	d.dpiMu.Lock()
	defer d.dpiMu.Unlock()
	d.stopDPILocked()
}

// stopDPILocked is stopDPIBypass's body for callers already holding dpiMu.
func (d *Daemon) stopDPILocked() {
	if d.dpi == nil || !d.dpiRunning {
		// Clear a leftover "failed" so a disarmed bypass reports nothing rather than
		// a stale failure from the last attempt.
		d.setDPIStatus(DPIStatusOff)
		return
	}
	// Bump first: the Stop below makes Done fire, and the watcher must read a
	// generation that has already moved on so it stays quiet about an exit we asked
	// for. Closing dpiStop releases it even if this runner's Stop reaps the process
	// without signalling Done at all — the daemon's shutdown must not depend on
	// that.
	d.dpiGen++
	d.dpiRunning = false
	if d.dpiStop != nil {
		close(d.dpiStop)
		d.dpiStop = nil
	}
	if err := d.dpi.Stop(); err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("dpi bypass: stopping the engine: %v", err))
	}
	if d.dpiCancel != nil {
		d.dpiCancel()
		d.dpiCancel = nil
	}
	d.setDPIStatus(DPIStatusOff)
}

// watchDPIBypass turns an engine exit we did not ask for into a failed status and
// a log line — and nothing else. The tunnel is a separate process that keeps
// carrying traffic; the destinations routed into the bypass simply go out
// unreshaped until it is started again, which is strictly better than tearing
// down a working tunnel over a helper. gen identifies the instance this watcher
// belongs to, so a Stop (which bumps the counter before signalling) is recognised
// as an exit we caused and passes silently; stopped is that Stop's direct signal,
// so this goroutine also unwinds under a runner whose Done stays silent afterwards.
func (d *Daemon) watchDPIBypass(gen uint64, done <-chan error, stopped <-chan struct{}) {
	var err error
	select {
	case e, ok := <-done:
		// A channel closed without a value means the runner is gone; treat it like a
		// clean exit and let the generation check below decide who still cares.
		if ok {
			err = e
		}
	case <-stopped:
		return // an exit we asked for; stopDPILocked owns the status
	}

	d.dpiMu.Lock()
	if gen != d.dpiGen {
		d.dpiMu.Unlock()
		return // superseded: we stopped it, or a newer instance replaced it
	}
	d.dpiRunning = false
	d.dpiStop = nil
	if d.dpiCancel != nil {
		d.dpiCancel()
		d.dpiCancel = nil
	}
	// Reap whatever the runner still holds; Stop is idempotent, so an already-dead
	// process costs nothing.
	if d.dpi != nil {
		_ = d.dpi.Stop()
	}
	d.setDPIStatus(DPIStatusFailed)
	d.emitDPITail()
	d.dpiMu.Unlock()

	msg := "engine exited"
	if err != nil {
		msg = err.Error()
	}
	d.emitLog(LogError, "dpi bypass: "+msg+"; the tunnel is unaffected, but the destinations it was reshaping now go out untouched")
}

// setDPIStatus records the engine's status on the daemon and mirrors it onto the
// reported state. Callers hold dpiMu; this takes mu, which is the fixed order
// (dpiMu -> mu) every bypass path follows. It does not emit a state event: the
// event body carries only state/node/error, so a client learns the new status
// from its next status call — the log line above is the push half of the news.
func (d *Daemon) setDPIStatus(status string) {
	d.mu.Lock()
	d.dpiStatus = status
	d.state.DPIStatus = status
	d.mu.Unlock()
}

// emitDPITail pushes the tail of the engine's output into the UI log. It is
// called only on a failure (a refused start, an unexpected exit), so a working
// bypass never spams the log, and an engine that captured nothing emits nothing.
// Callers hold dpiMu.
func (d *Daemon) emitDPITail() {
	if d.dpi == nil {
		return
	}
	lines := d.dpi.Logs()
	if len(lines) > dpiEngineTailLines {
		lines = lines[len(lines)-dpiEngineTailLines:]
	}
	for _, ln := range lines {
		d.emitLog(LogInfo, "  dpi bypass: "+ln)
	}
}

// prepareDPIBypass brings the engine up for a connect that wants it and folds the
// result into the options the config is built from: the port sing-box points the
// bypass outbound at, and the flag that makes it emit one at all.
//
// The flag is folded OUT whenever the engine is not actually running — no engine
// on this platform, a binary that will not spawn, a preference restored from a
// settings file written elsewhere. A config that routes into a socks port nothing
// listens on would black-hole exactly the destinations the user cares about, so
// the honest degradation is to build the config the daemon has always built and
// say so in the log. The preference itself stays armed and reported, with
// DPIStatus telling the difference.
func (d *Daemon) prepareDPIBypass(ro routing.Options, tun singbox.TunOptions) (routing.Options, singbox.TunOptions) {
	if !ro.DPIBypass {
		return ro, tun
	}
	if err := d.startDPIBypass(); err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("dpi bypass: %v; connecting without it", err))
		ro.DPIBypass = false
		return ro, tun
	}
	d.dpiMu.Lock()
	tun.DPIPort = d.dpiPort
	d.dpiMu.Unlock()
	return ro, tun
}

// handleSetDPIBypass arms or disarms the local DPI-bypass engine. Like the kill
// switch the choice is recorded, persisted and applied to a live tunnel in place
// via reapplyLive, so it doesn't wait for a reconnect — but here the hot swap has
// to be sequenced around a second process:
//
//   - arming starts the engine BEFORE the tunnel is rebuilt, because the config
//     the rebuild renders already routes into its socks port;
//   - disarming rebuilds the tunnel FIRST and stops the engine after, so no live
//     config is ever left pointing at a port that has gone away.
//
// Arming without an engine installed is refused outright: recording a preference
// nothing can honour would leave a toggle that looks armed and does nothing.
// Disarming always works — a settings file carried over from a machine that had
// an engine has to be clearable. An engine that is present but refuses to start
// is not a command failure: the preference is kept, the tunnel is rebuilt without
// the bypass, and DPIStatus reports "failed" so the UI can say so.
func (d *Daemon) handleSetDPIBypass(req Request) Response {
	if req.On && !d.dpiAvailable() {
		return newError(req.ID, fmt.Sprintf("set_dpi_bypass: %v", errNoDPIEngine))
	}

	d.mu.Lock()
	changed := d.routing.DPIBypass != req.On
	d.routing.DPIBypass = req.On
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		if req.On {
			if err := d.startDPIBypass(); err != nil {
				// Reported, not swallowed: the log line and the failed status are what
				// tell the user the armed toggle is not currently doing anything.
				d.emitLog(LogError, fmt.Sprintf("dpi bypass: %v; the tunnel keeps running without it", err))
			}
			d.reapplyLive()
		} else {
			d.reapplyLive()
			d.stopDPIBypass()
		}
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}
