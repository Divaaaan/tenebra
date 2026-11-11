package control

import (
	"context"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/state"
)

func subscriptionResultError(code string) error {
	switch code {
	case "profile_stale":
		return state.ErrProfileConflict
	case "intent_stale":
		return state.ErrIntentConflict
	case "required_reference":
		return state.ErrRequiredReference
	default:
		return subscriptionError(code)
	}
}

// Legacy callers get their old profile response only after the same operation
// completes. They cannot supply direct consent or revive an ambient fetch path.
func (d *Daemon) handleCanonicalLegacyNetwork(ctx context.Context, req Request) Response {
	input := SubscriptionStart{Kind: "refresh", ProfileID: req.Profile}
	if req.Cmd == CmdImportSubscription {
		input = SubscriptionStart{Kind: "import", URL: req.URL, Name: req.Name, AllowHTTP: req.AllowHTTP != nil && *req.AllowHTTP}
	}
	updated, err := d.runSubscriptionToProfile(ctx, input, nil)
	if err != nil {
		return newError(req.ID, subscriptionErrorCode(err))
	}
	return d.profileResult(req.ID, updated)
}

func (d *Daemon) runSubscriptionToProfile(ctx context.Context, input SubscriptionStart, expected *profile.Profile) (profile.Profile, error) {
	prepared, err := d.startSubscriptionOperation(ctx, input)
	if err != nil {
		return profile.Profile{}, err
	}
	owner := d.subscriptionOps
	owner.mu.Lock()
	operation := owner.operations[prepared.OperationID]
	if operation == nil || operation.scope == nil {
		owner.mu.Unlock()
		return profile.Profile{}, subscriptionError("cancelled")
	}
	stale := expected != nil && (operation.scope.target.Revision != expected.Revision || operation.scope.url != expected.URL)
	owner.mu.Unlock()
	if stale {
		_, _ = d.cancelSubscriptionOperation(ctx, prepared.OperationID)
		return profile.Profile{}, state.ErrProfileConflict
	}
	view, err := d.continueSubscriptionOperation(ctx, prepared.OperationID, "default", "")
	if err != nil {
		return profile.Profile{}, err
	}
	if view.Stage != "running" {
		_, _ = d.cancelSubscriptionOperation(ctx, prepared.OperationID)
		return profile.Profile{}, subscriptionResultError(view.Code)
	}
	select {
	case <-operation.done:
	case <-ctx.Done():
		owner.revoke(operation)
		return profile.Profile{}, subscriptionError("cancelled")
	}
	owner.mu.Lock()
	view = operation.view
	owner.mu.Unlock()
	if view.Stage != "succeeded" {
		return profile.Profile{}, subscriptionResultError(view.Code)
	}
	updated, ok := d.canonical.View().Profiles[view.Profile]
	if !ok {
		return profile.Profile{}, state.ErrProfileConflict
	}
	return updated, nil
}
