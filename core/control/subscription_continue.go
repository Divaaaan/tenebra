package control

import (
	"context"
	"crypto/rand"
	"net/url"
	"time"

	"github.com/Divaaaan/tenebra/core/maintenance"
	"github.com/Divaaaan/tenebra/core/state"
)

type subscriptionConsentChallenge struct {
	id                             string
	acceptedEpoch, protectionEpoch uint64
}

func (d *Daemon) continueSubscriptionOperation(ctx context.Context, id, decision, challengeID string) (SubscriptionOperationView, error) {
	session, err := d.subscriptionSession(ctx)
	if err != nil {
		return SubscriptionOperationView{}, err
	}
	if decision != "default" && decision != "direct_once" {
		return SubscriptionOperationView{}, subscriptionError("policy_denied")
	}
	if decision == "default" && challengeID != "" {
		return SubscriptionOperationView{}, subscriptionError("policy_denied")
	}
	owner := d.subscriptionOps
	owner.mu.Lock()
	captured := owner.operations[id]
	var targetProfile string
	if captured != nil && captured.session == session {
		targetProfile = captured.view.Profile
	}
	owner.mu.Unlock()
	ready := d.canonical != nil && d.canonical.Ready() == nil
	var currentRecord state.SubscriptionRecord
	var currentExists bool
	if ready && targetProfile != "" {
		currentRecord, currentExists = state.SubscriptionAt(d.canonical.View(), targetProfile)
	}
	// Entropy is obtained outside A, even when no challenge will be needed.
	newChallengeID := rand.Text()
	owner.mu.Lock()
	operation := owner.operations[id]
	if operation == nil {
		owner.mu.Unlock()
		return SubscriptionOperationView{}, subscriptionError("not_found")
	}
	if operation.session != session {
		owner.mu.Unlock()
		return SubscriptionOperationView{}, subscriptionError("wrong_session")
	}
	if terminalSubscriptionStage(operation.view.Stage) {
		view := operation.view
		if view.Stage == "succeeded" {
			view.Code = "already_completed"
		}
		owner.mu.Unlock()
		return view, nil
	}
	if operation.started {
		view := operation.view
		if view.Stage != "cancelling" {
			view.Code = "already_started"
		}
		owner.mu.Unlock()
		return view, nil
	}
	finish := func(code string) (SubscriptionOperationView, error) {
		owner.finishLocked(operation, "failed", code)
		view := operation.view
		owner.mu.Unlock()
		return view, nil
	}
	if owner.closed || session.closed.Load() || session.ctx.Err() != nil {
		return finish("cancelled")
	}
	if !d.now().Before(operation.expires) {
		return finish("expired")
	}
	if !ready {
		return finish("storage_error")
	}
	scope := operation.scope
	if operation.view.Kind == "refresh" {
		record, ok := currentRecord, currentExists
		if !ok || record.Revision != scope.target.Revision || record.Profile.URL != scope.url || record.HTTPAllowed != scope.allowHTTP {
			return finish("scope_changed")
		}
	}
	source, _ := url.Parse(scope.url)
	if source.Scheme == "http" && !scope.allowHTTP {
		operation.view.Code = "http_opt_in_required"
		view := operation.view
		owner.mu.Unlock()
		return view, nil
	}
	if operation.challenge != nil && (operation.challenge.acceptedEpoch != owner.acceptedEpoch || operation.challenge.protectionEpoch != owner.protectionEpoch) {
		return finish("scope_changed")
	}
	egress, admission := classifySubscriptionEgress(owner.accepted, session.manual)
	switch admission {
	case subscriptionDefer:
		operation.view.Stage, operation.view.Code = "deferred", "waiting_for_vpn"
		view := operation.view
		owner.mu.Unlock()
		return view, nil
	case subscriptionUnsupported:
		return finish("transport_unavailable")
	case subscriptionChallenge:
		if decision != "direct_once" || operation.challenge == nil {
			if decision == "direct_once" {
				return finish("policy_denied")
			}
			if operation.challenge == nil {
				operation.challenge = &subscriptionConsentChallenge{newChallengeID, owner.acceptedEpoch, owner.protectionEpoch}
			}
			operation.view.Stage, operation.view.Code = "consent_required", "direct_consent_required"
			operation.view.ChallengeID = operation.challenge.id
			operation.view.ConsentExpiresAt = operation.expires.UTC().Format(time.RFC3339Nano)
			view := operation.view
			owner.mu.Unlock()
			return view, nil
		}
		if challengeID != operation.challenge.id {
			return finish("policy_denied")
		}
		egress = maintenance.DirectOnce
	case subscriptionAdmit:
		// Direct consent cannot be carried into a changed/default scope.
		if decision == "direct_once" {
			return finish("scope_changed")
		}
	}
	operation.started = true
	operation.view.Stage, operation.view.Code = "running", ""
	operation.view.ChallengeID, operation.view.ConsentExpiresAt = "", ""
	operation.challenge = nil
	operation.acceptance = owner.accepted
	operation.acceptance.RequiredNodeIDs = append([]string(nil), owner.accepted.RequiredNodeIDs...)
	operation.acceptedEpoch, operation.protectionEpoch = owner.acceptedEpoch, owner.protectionEpoch
	operation.runCtx, operation.cancel = context.WithTimeout(session.ctx, 45*time.Second)
	operation.done = make(chan struct{})
	// The selected egress is immutable after one authenticated Continue.
	operation.egress = egress
	if operation.timer != nil {
		operation.timer.Stop()
		operation.timer = nil
	}
	owner.workers.Add(1)
	view := operation.view
	owner.mu.Unlock()
	go d.runSubscriptionOperation(operation)
	return view, nil
}
