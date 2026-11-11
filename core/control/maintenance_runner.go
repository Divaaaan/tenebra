package control

import (
	"context"
	"github.com/Divaaaan/tenebra/core/maintenance"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

// MaintenanceRunner is optional: a missing capability cannot authorize direct
// maintenance traffic and does not prevent ordinary VPN bootstrap.
type MaintenanceRunner interface {
	SetRuntimeRoot(*securefs.Root) error
	PrepareMaintenance(context.Context) (maintenance.InboundConfig, error)
	MaintenanceEndpoint(maintenance.RunID) (maintenance.Endpoint, error)
	DiscardMaintenance(maintenance.RunID) error
}
