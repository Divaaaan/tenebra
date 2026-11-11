package control

import (
	"context"
	"errors"
	"time"

	"github.com/Divaaaan/tenebra/core/state"
)

// BusinessReady is the common admission check for wire and background entries.
// A canonical Store always opens held; only the composition root's verified
// operational commit may admit it. Legacy in-memory fixtures keep their scope.
func (d *Daemon) BusinessReady() error {
	d.business.mu.Lock()
	closed := d.business.closed
	d.business.mu.Unlock()
	if closed {
		return state.ErrMaintenancePending
	}
	return d.CanonicalReady()
}

func maintenanceAllowedCommand(command string) bool {
	switch command {
	case CmdStatus, CmdListProfiles, CmdCollectDiagnostics, CmdDisconnect, CmdSubscriptionOperationStatus, CmdCancelSubscriptionOperation:
		return true
	default:
		return false
	}
}

// A service can start its background loop while held. It must wait before
// installing embedded bytes, raising a bypass or reaching an update endpoint.
// Ready admitted/legacy starts retain their existing startup behavior.
func (d *Daemon) waitForBusinessAdmission(ctx context.Context) bool {
	for {
		d.business.mu.Lock()
		closed := d.business.closed
		d.business.mu.Unlock()
		if closed {
			return false
		}
		err := d.BusinessReady()
		if err == nil {
			return true
		}
		if !errors.Is(err, state.ErrMaintenancePending) {
			return false
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}
