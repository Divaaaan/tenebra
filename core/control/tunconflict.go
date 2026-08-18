package control

import (
	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/core/tunguard"
)

// SetInterfaceProbe installs the platform's interface/route enumerator, arming
// the tun-conflict guard. Call it before serving; passing nil leaves the guard
// disabled.
//
// The core cannot read a route table itself (it stays stdlib-only and
// platform-agnostic), so the adapter injects the capability — the same shape as
// Runner and the system-proxy controller.
func (d *Daemon) SetInterfaceProbe(probe func() ([]tunguard.Iface, error)) {
	d.ifaceProbe = probe
}

// checkTunConflict reports whether raising our tun right now would collide with
// another VPN that already owns the machine's default route.
//
// It is checked on a user-driven connect only, and only in tun mode:
//
//   - System-proxy mode creates no tun and installs no route, so it cannot
//     collide with anything; blocking it would be pure obstruction.
//   - Relaunch and reconcile paths are excluded on purpose. They fire while our
//     own tunnel is already up or mid-teardown, where the machine legitimately
//     has our route in flight; refusing there would turn a recoverable blip into
//     a tunnel that cannot come back on its own.
//
// A probe error is deliberately NOT fatal: if the route table cannot be read we
// know nothing, and converting "unknown" into "refuse" would strand the user
// with an app that will not connect for a reason it cannot even name. The guard
// exists to prevent a specific, diagnosable failure — not to gate connectivity
// on its own reliability.
//
// override comes from the user's explicit escape hatch, never from a default.
func (d *Daemon) checkTunConflict(override bool) error {
	if d.ifaceProbe == nil || override {
		return nil
	}

	d.mu.Lock()
	tun := d.tun
	d.mu.Unlock()
	if tun.IsSystemProxy() {
		return nil // no tun, no route, nothing to collide with
	}

	ifaces, err := d.ifaceProbe()
	if err != nil {
		return nil // unknown is not a conflict; see above
	}

	// Our own tun is excluded by the name the builder will give it, so a
	// reconnect is never blocked by the interface it is about to replace. An
	// unset name resolves to the platform default exactly as the builder would.
	name := tun.InterfaceName
	if name == "" {
		name = singbox.DefaultTUNName()
	}
	return tunguard.Check(ifaces, false, name)
}
