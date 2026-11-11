package control

import (
	"context"
	"errors"

	"github.com/Divaaaan/tenebra/core/state"
)

func subscriptionCommitCode(err error) string {
	switch {
	case errors.Is(err, state.ErrProfileConflict):
		return "profile_stale"
	case errors.Is(err, state.ErrIntentConflict):
		return "intent_stale"
	case errors.Is(err, state.ErrRequiredReference):
		return "required_reference"
	default:
		return "storage_error"
	}
}

// Proposal preparation (including IDs, validation and matching) is outside F/S.
// The callback contains only the repository transaction and its atomic outcome.
func (d *Daemon) commitSubscriptionRefresh(operation *subscriptionOperation, result subscriptionFetchResult) (state.Snapshot, error) {
	scope := operation.scope
	var apply func(*state.Snapshot) error
	profileID := operation.view.Profile
	if operation.view.Kind == "refresh" {
		var required []string
		if operation.acceptance.ProfileID == profileID {
			required = append(required, operation.acceptance.RequiredNodeIDs...)
		}
		proposal, err := state.PrepareSubscriptionRefresh(scope.base, profileID, result.Nodes, result.Metadata, required)
		if err != nil {
			return state.Snapshot{}, err
		}
		apply = func(snapshot *state.Snapshot) error {
			_, err := state.ApplySubscriptionRefresh(snapshot, proposal)
			return err
		}
	} else {
		proposal, err := state.PrepareSubscriptionImport(scope.base, scope.name, scope.url, result.Nodes, result.Metadata, scope.allowHTTP)
		if err != nil {
			return state.Snapshot{}, err
		}
		apply = func(snapshot *state.Snapshot) error {
			profile, err := state.ApplySubscriptionImport(snapshot, proposal)
			if err == nil {
				profileID = profile.ID
			}
			return err
		}
	}
	var accepted state.Snapshot
	err := operation.lease.WithCommitFence(operation.runCtx, func() error {
		var err error
		accepted, err = d.canonical.Transact(operation.runCtx, state.AnyRevision, apply)
		outcome := &subscriptionCommitOutcome{stage: "succeeded", profile: profileID, committed: true}
		if err != nil {
			outcome = &subscriptionCommitOutcome{stage: "failed", code: subscriptionCommitCode(err),
				uncertain: errors.Is(err, state.ErrDurabilityUnknown)}
			if errors.Is(err, context.Canceled) {
				outcome = &subscriptionCommitOutcome{stage: "cancelled", code: "cancelled"}
			}
			if errors.Is(err, context.DeadlineExceeded) {
				outcome = &subscriptionCommitOutcome{stage: "failed", code: "expired"}
			}
		}
		// Cancel must observe success/uncertainty before F can become drained.
		operation.outcome.Store(outcome)
		return err
	})
	return accepted, err
}
