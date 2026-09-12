package control

import (
	"errors"
	"fmt"

	"github.com/Divaaaan/tenebra/core/protection"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
)

// EngineExecutablePath exposes only the runner's exact resolved executable.
// A fake or unsupported runner cannot accidentally authorize a host binary.
func (d *Daemon) EngineExecutablePath() (string, error) {
	if r, ok := d.runner.(interface{ ExecutablePath() (string, error) }); ok {
		return r.ExecutablePath()
	}
	return "", errors.New("runner has no trusted executable identity")
}

// SetProtection is the explicit composition seam. NewDaemon never attaches a
// native adapter; fake runners and ordinary unit fixtures cannot touch WFP.
func (d *Daemon) SetProtection(g *protection.Guard) {
	if g == nil {
		g = protection.New(nil)
	}
	d.protection = g
	g.SetNotify(d.emitProtection)
}

// UseLegacyEngineProtection is selected only by the non-Windows production
// composition root. Default/missing-backend constructors remain fail closed.
func (d *Daemon) UseLegacyEngineProtection() {
	d.SetProtection(protection.NewLegacyEngineOnly())
}

func (d *Daemon) emitProtection() {
	s := d.snapshotState()
	d.mu.Lock()
	emit := d.emit
	d.mu.Unlock()
	if emit != nil {
		emit(EventState, stateEventBody(s))
	}
}

// RecoverProtectionAtStartup preserves/repairs existing owned policy regardless
// of settings; failure remains visible while the control plane stays available.
func (d *Daemon) RecoverProtectionAtStartup() error {
	if err := d.protection.Recover(); err != nil {
		return fmt.Errorf("host protection recovery: %w", err)
	}
	return nil
}

func (d *Daemon) prepareProtection(ro routing.Options) error {
	d.protectionOp.Lock()
	defer d.protectionOp.Unlock()
	if d.protection.LegacyEngineOnly() {
		return nil
	}
	d.mu.Lock()
	wanted := d.routing.KillSwitch
	d.mu.Unlock()
	s := d.protection.Snapshot()
	if !wanted {
		if s.Status == "error" {
			return fmt.Errorf("host protection unresolved: %s; retry OFF or Disconnect", s.Error)
		}
		if !s.Enforced {
			return nil
		}
	}
	if err := protection.ValidateDNS(ro.Normalize().DNSDirect); err != nil {
		return d.protection.Reject(err)
	}
	return d.protection.Prepare()
}

// activateProtectionLocked runs inside recordSuccess's acceptance critical
// section. Keep protectionOp held through every local gate and the connected
// publication, so a setting command cannot replace the verified policy.
func (d *Daemon) activateProtectionLocked(ro routing.Options, tun singbox.TunOptions) error {
	if d.protection.LegacyEngineOnly() {
		return nil
	}
	d.mu.Lock()
	wanted := d.routing.KillSwitch
	d.mu.Unlock()
	s := d.protection.Snapshot()
	if !wanted && !s.Enforced {
		if s.Status == "error" {
			return fmt.Errorf("host protection unresolved: %s", s.Error)
		}
		return nil
	}
	if err := protection.ValidateDNS(ro.Normalize().DNSDirect); err != nil {
		return d.protection.Reject(err)
	}
	name := tun.InterfaceName
	if name == "" {
		name = singbox.DefaultTUNName()
	}
	return d.protection.VerifyTunnel(name, tun.Address, tun.IsSystemProxy())
}

// ProtectionDNS returns the requested encrypted endpoint and whether plaintext
// fallback is forbidden. The production resolver reads this on each lookup.
func (d *Daemon) ProtectionDNS() (string, bool) {
	d.mu.Lock()
	ro := d.routing
	d.mu.Unlock()
	s := d.protection.Snapshot()
	return ro.Normalize().DNSDirect, !d.protection.LegacyEngineOnly() && (ro.KillSwitch || s.Enforced || s.Status == "error")
}
