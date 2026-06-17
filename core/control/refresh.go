package control

import (
	"context"
	"time"
)

// refreshInterval is how often the daemon re-fetches every subscription profile
// in the background. Subscriptions change slowly (node rotations, quota resets),
// so a few hours keeps usage and node lists fresh without hammering panels.
const refreshInterval = 6 * time.Hour

// runAutoRefresh periodically refreshes all subscription profiles until ctx is
// cancelled. It is the long-lived background companion to the manual
// refresh_subscription command: the server starts it alongside its other work
// and ctx cancellation (shutdown) stops it. The sweep itself lives in
// refreshAllSubscriptions; this loop is only the ticker and ctx plumbing.
//
// Each tick honours ctx, so a sweep in flight at shutdown is cut short rather
// than blocking the daemon from exiting. A tick where nothing changed emits no
// event; one that changed any profile emits a single profiles event for the
// whole batch.
func (d *Daemon) runAutoRefresh(ctx context.Context) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if d.refreshAllSubscriptions(ctx) {
				d.emitProfiles()
			}
		}
	}
}
