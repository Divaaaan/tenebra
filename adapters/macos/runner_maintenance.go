//go:build darwin

package macos

import (
	"context"
	"github.com/Divaaaan/tenebra/core/maintenance"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func (r *Runner) SetRuntimeRoot(root *securefs.Root) error { return r.maintenance.SetRoot(root) }
func (r *Runner) PrepareMaintenance(ctx context.Context) (maintenance.InboundConfig, error) {
	return r.maintenance.Prepare(ctx)
}
func (r *Runner) MaintenanceEndpoint(id maintenance.RunID) (maintenance.Endpoint, error) {
	return r.maintenance.Endpoint(id)
}
func (r *Runner) DiscardMaintenance(id maintenance.RunID) error { return r.maintenance.Discard(id) }
