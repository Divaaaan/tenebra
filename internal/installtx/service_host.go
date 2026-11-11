package installtx

import (
	"strconv"
	"strings"

	"github.com/Divaaaan/tenebra/core/state"
)

// HostedProcess is private process-lifetime metadata. Reading it is NOT proof
// of liveness, readiness or permission to signal a PID. The native owner must
// verify the kernel lifetime/peer and retain its owned child separately.
type HostedProcess struct {
	TxID       string        `json:"tx_id"`
	Runtime    RuntimeRef    `json:"runtime"`
	PID        uint32        `json:"pid"`
	StartToken string        `json:"start_token"`
	LaunchID   string        `json:"launch_id"`
	Ready      *ReadyReceipt `json:"ready"`
}

func (p HostedProcess) Identity() ProcessIdentity {
	return ProcessIdentity{PID: p.PID, StartToken: p.StartToken, LaunchID: p.LaunchID}
}
func (p HostedProcess) Validate() error {
	if !protocolID(p.TxID, "it1_") || !validRuntime(&p.Runtime) || !validHostIdentity(p.Identity()) {
		return ErrReadiness
	}
	if r := p.Ready; r != nil {
		if r.Schema != 1 || !oneOf(r.Status, "ready", "admitted") || r.TxID != p.TxID || r.LaunchID != p.LaunchID ||
			r.DaemonVersion != p.Runtime.Version || !hexValue(r.CoreSHA256, 64) || !safeToken(r.DaemonInstanceID, 128) || len(r.DaemonInstanceID) < 20 ||
			r.StoreSchema != state.SchemaVersion || r.StoreRevision == 0 || r.Transport != "unix" || !oneOf(r.BusinessAdmission, "open", "closed") ||
			r.Status == "ready" && !hexValue(r.Challenge, 32) || r.Status == "admitted" && r.Challenge != "" ||
			r.Status == "ready" && r.BusinessAdmission != "closed" || r.Status == "admitted" && r.BusinessAdmission != "open" {
			return ErrReadiness
		}
	}
	return nil
}
func validHostIdentity(p ProcessIdentity) bool {
	if p.PID == 0 || p.PID > 1<<31-1 || !hexValue(p.LaunchID, 32) || len(p.StartToken) > 40 {
		return false
	}
	parts := strings.Split(p.StartToken, ":")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	for i, part := range parts {
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil || strconv.FormatUint(value, 10) != part || i == 0 && value == 0 || i == 1 && value >= 1000000 {
			return false
		}
	}
	return true
}

// This plan names an action, never a PID to kill or an arbitrary path to exec.
// A host must reread the journal before each transition; actual readiness and
// admission still use the authenticated private protocol.
type ServiceHostPlan struct {
	Action                    string
	Runtime                   *RuntimeRef
	TxID, OperationalCommitID string
	SchemaReceipt             *SchemaReceipt
}

func PlanServiceHost(j Journal, ownedCurrent *HostedProcess) (ServiceHostPlan, error) {
	inactive := ServiceHostPlan{Action: "inactive"}
	if j.Validate() != nil {
		return inactive, ErrRecoveryRequired
	}
	if ownedCurrent != nil && ownedCurrent.Validate() != nil {
		return inactive, ErrReadiness
	}
	tx := j.Transaction
	if tx == nil {
		if ownedCurrent != nil {
			return inactive, ErrRecoveryRequired
		}
		return inactive, nil
	}
	if ownedCurrent != nil && !sameRuntime(&ownedCurrent.Runtime, j.Active) && !sameRuntime(&ownedCurrent.Runtime, tx.PriorActive) && !sameRuntime(&ownedCurrent.Runtime, tx.Candidate) {
		// Unknown/custom processes are preserved, never adopted or stopped.
		return inactive, ErrRecoveryRequired
	}
	plan := ServiceHostPlan{Action: "inactive", TxID: tx.ID}
	switch tx.Phase {
	case "pending", "staged":
		if j.Active == nil {
			if ownedCurrent != nil {
				return inactive, ErrRecoveryRequired
			}
			return plan, nil
		}
		if ownedCurrent == nil || !sameRuntime(&ownedCurrent.Runtime, j.Active) || ownedCurrent.Ready == nil || ownedCurrent.Ready.Status != "admitted" {
			// A pending transaction does not invent an old operational grant.
			// The identified old process may continue; an absent old process
			// stays inactive until explicit transaction recovery/application.
			return inactive, ErrRecoveryRequired
		}
		ref := *j.Active
		plan.Action, plan.Runtime = "keep_current", &ref
		return plan, nil
	case "fencing", "fenced", "rolling_back", "rolled_back", "removed", "blocked":
		if ownedCurrent != nil {
			plan.Action = "stop"
		}
		return plan, nil
	case "schema_ready", "ready", "committed_closed", "committed_open":
		if tx.Candidate == nil || tx.SchemaReceipt == nil {
			return inactive, ErrRecoveryRequired
		}
		if ownedCurrent != nil && (ownedCurrent.TxID != tx.ID || !sameRuntime(&ownedCurrent.Runtime, tx.Candidate)) {
			plan.Action = "stop"
			return plan, nil
		}
		ref, receipt := *tx.Candidate, *tx.SchemaReceipt
		if ownedCurrent != nil && ownedCurrent.Ready != nil &&
			(ownedCurrent.Ready.StoreSchema != receipt.Schema || ownedCurrent.Ready.StoreRevision < receipt.Revision) {
			return inactive, ErrRecoveryRequired
		}
		plan.Runtime, plan.SchemaReceipt = &ref, &receipt
		plan.OperationalCommitID = tx.OperationalCommitID // same durable ID, never regenerated
		plan.Action = "start_held"
		if ownedCurrent != nil {
			plan.Action = "keep_held"
			if ownedCurrent.Ready != nil && ownedCurrent.Ready.BusinessAdmission == "open" {
				if tx.OperationalCommitID == "" {
					return inactive, ErrRecoveryRequired
				}
				plan.Action = "keep_open"
			}
		}
		return plan, nil
	default:
		return inactive, ErrRecoveryRequired
	}
}
