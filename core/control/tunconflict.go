package control

import (
	"fmt"

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
// It is checked on the two connects that start from nothing — the user-driven
// one and the daemon-start autoconnect — and only in tun mode:
//
//   - System-proxy mode creates no tun and installs no route, so it cannot
//     collide with anything; blocking it would be pure obstruction.
//   - Relaunch and reconcile paths are excluded on purpose. They fire while our
//     own tunnel is already up or mid-teardown, where the machine legitimately
//     has our route in flight; refusing there would turn a recoverable blip into
//     a tunnel that cannot come back on its own. That reason does not extend to
//     autoconnect, which raises its tun on a machine where we have nothing up —
//     and where another client's service, started with the machine, is exactly
//     what the guard exists to find.
//
// A probe error is deliberately NOT fatal: if the route table cannot be read we
// know nothing, and converting "unknown" into "refuse" would strand the user
// with an app that will not connect for a reason it cannot even name. The guard
// exists to prevent a specific, diagnosable failure — not to gate connectivity
// on its own reliability.
//
// override comes from the user's explicit escape hatch, never from a default.
func (d *Daemon) checkTunConflict(override bool) error {
	if d.ifaceProbe == nil {
		d.emitDebug("tun guard: no route enumeration on this platform; not checking")
		return nil
	}
	if override {
		d.emitLog(LogWarn, "tun guard: overridden by the request; raising our tun without checking the default route")
		return nil
	}

	d.mu.Lock()
	tun := d.tun
	d.mu.Unlock()
	if tun.IsSystemProxy() {
		d.emitDebug("tun guard: system-proxy mode creates no tun; nothing to collide with")
		return nil
	}

	ifaces, err := d.ifaceProbe()
	if err != nil {
		// Worth a line: "the guard did not fire" and "the guard could not look"
		// are the same silence otherwise, and they mean opposite things when a
		// user later reports the exact failure this exists to prevent.
		d.emitLog(LogWarn, fmt.Sprintf("tun guard: could not read the route table (%v); proceeding unchecked", err))
		return nil
	}

	// Our own tun is excluded by the name the builder will give it, so a
	// reconnect is never blocked by the interface it is about to replace. An
	// unset name resolves to the platform default exactly as the builder would,
	// and tunguard matches it by prefix, so the "tenebra 2" Windows hands back
	// when the name is still taken is recognised as ours too.
	name := tun.InterfaceName
	if name == "" {
		name = singbox.DefaultTUNName()
	}

	// Say what the guard saw, not only what it decided. The decision is one bit;
	// the evidence is what tells a maintainer whether a refusal was right, and
	// whether a clean pass merely means the adapter enumerated nothing.
	d.emitDebug(fmt.Sprintf("tun guard: %d interface(s) enumerated, ours is %q", len(ifaces), name))
	for _, ifc := range ifaces {
		m, hasDef := ifc.BestMetric()
		if !hasDef {
			continue
		}
		d.emitDebug(fmt.Sprintf("tun guard: default route on %q (tunnel=%t, metric=%d)",
			ifc.Name, tunguard.IsTunnelIface(ifc), m))
	}

	err = tunguard.Check(ifaces, false, name)
	if err != nil {
		d.emitLog(LogWarn, "tun guard: "+err.Error())
	}
	return err
}
