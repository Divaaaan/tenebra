package control

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/Divaaaan/tenebra/core/singbox"
)

// defaultTunWatchInterval is how often a live tunnel's interface is checked for
// still existing.
const defaultTunWatchInterval = 5 * time.Second

// tunAppearBudget is how long the watch waits for the interface to show up
// before giving up on watching it. sing-box creates the adapter as it starts, so
// a few seconds is generous; a machine that never produces one is a machine
// where this watch has nothing to say.
const tunAppearBudget = 20 * time.Second

// watchTunInterface reports the tunnel down when its network interface vanishes
// underneath a process that is still running.
//
// The daemon used to watch only the sing-box process, which is not the same
// thing: on a live machine the adapter disappeared about twenty seconds into
// every session while the process stayed up, so the app kept reporting a healthy
// connection over an interface that no longer existed. Everything downstream —
// the user, the logs, and a day of debugging — then started from a false premise.
//
// It watches by name and only in tun mode. System-proxy mode raises no interface,
// and on macOS the kernel names the device (utunN) so there is no name to watch;
// both leave this inert rather than guessing.
func (d *Daemon) watchTunInterface(ctx context.Context, gen uint64) {
	interval := d.tunWatchInterval
	if interval <= 0 {
		return // disabled
	}
	tun := d.snapshotTun()
	if tun.IsSystemProxy() {
		return
	}
	// An unset name resolves to the platform default exactly as the builder would,
	// which is the whole point: the adapter to watch is the one it is about to
	// create, and the options rarely name it. Where the platform lets the kernel
	// name the device (macOS utunN) that default is empty and this stays inert.
	name := tun.InterfaceName
	if name == "" {
		name = singbox.DefaultTUNName()
	}
	if name == "" {
		return
	}

	// Wait for it to come up first: reporting "gone" for an interface that has not
	// appeared yet would turn a slow start into a failure.
	appeared := false
	deadline := time.Now().Add(tunAppearBudget)
	for time.Now().Before(deadline) {
		if !d.isCurrent(gen) || ctx.Err() != nil {
			return
		}
		if d.ifacePresent(name) {
			appeared = true
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	if !appeared {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		if !d.isCurrent(gen) {
			return // superseded; a newer connection owns the state
		}
		if d.ifacePresent(name) {
			continue
		}
		// Give it one grace beat: an adapter can flicker while the stack
		// reconfigures, and tearing a session down for that would be its own bug.
		// The beat is capped by the interval so a fast watch stays fast.
		grace := 2 * time.Second
		if interval < grace {
			grace = interval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(grace):
		}
		if !d.isCurrent(gen) || d.ifacePresent(name) {
			continue
		}

		d.emitLog(LogError, "tunnel interface "+name+" disappeared while sing-box was still running")
		// Whatever sing-box said before losing its adapter is the only explanation
		// available, and it is exactly what was missing while this was diagnosed.
		d.emitSingboxTail()
		// Stop the orphan rather than inventing a state here. A process with no
		// interface carries nothing, and stopping it lands on watchProcess — the
		// one path that already disarms the system proxy, spends the kill-switch
		// relaunch budget, and settles the state. Two paths to the same outcome is
		// how the state ends up disagreeing with reality, which is this bug.
		if err := d.runner.Stop(); err != nil {
			d.emitLog(LogError, "could not stop the tunnel process: "+err.Error())
		}
		return
	}
}

// defaultIfacePresent reports whether a network interface with this name exists
// and is up. Name matching is case-insensitive and prefix-based because Windows
// appends a suffix when an adapter name is already taken ("tenebra 2").
func defaultIfacePresent(name string) bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return true // cannot tell: assume present rather than tear a session down
	}
	want := strings.ToLower(name)
	for _, i := range ifaces {
		if strings.HasPrefix(strings.ToLower(i.Name), want) {
			return true
		}
	}
	return false
}
