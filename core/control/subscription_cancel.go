package control

import (
	"context"
	"time"

	"github.com/Divaaaan/tenebra/core/maintenance"
)

func (o *subscriptionOperations) revoke(operation *subscriptionOperation) {
	o.mu.Lock()
	cancel, lease := operation.cancel, operation.lease
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if lease != nil {
		lease.Revoke(maintenance.ErrRevoked)
	}
}

func (d *Daemon) cancelSubscriptionOperation(ctx context.Context, id string) (SubscriptionOperationView, error) {
	session, err := d.subscriptionSession(ctx)
	if err != nil {
		return SubscriptionOperationView{}, err
	}
	owner := d.subscriptionOps
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
	if !operation.started {
		owner.finishLocked(operation, "cancelled", "cancelled")
		view := operation.view
		owner.mu.Unlock()
		return view, nil
	}
	operation.view.Stage, operation.view.Code = "cancelling", "commit_pending"
	done, lease := operation.done, operation.lease
	owner.mu.Unlock()
	owner.revoke(operation)
	drain, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if lease != nil {
		_ = lease.WaitDrained(drain)
	}
	select {
	case <-done:
	case <-drain.Done():
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	view := operation.view
	if view.Stage == "succeeded" {
		view.Code = "already_completed"
	}
	return view, nil
}

func (o *subscriptionOperations) finishWorker(operation *subscriptionOperation, outcome *subscriptionCommitOutcome) {
	o.mu.Lock()
	o.finishLocked(operation, outcome.stage, outcome.code)
	if outcome.profile != "" {
		operation.view.Profile = outcome.profile
	}
	if operation.session.closed.Load() {
		delete(o.operations, operation.view.OperationID)
		delete(o.terminal, operation.session.id)
	}
	o.mu.Unlock()
}

func (o *subscriptionOperations) close() {
	o.mu.Lock()
	o.closed = true
	var running []*subscriptionOperation
	for _, operation := range o.operations {
		if terminalSubscriptionStage(operation.view.Stage) {
			continue
		}
		if operation.started {
			operation.view.Stage, operation.view.Code = "cancelling", "commit_pending"
			running = append(running, operation)
		} else {
			o.finishLocked(operation, "cancelled", "cancelled")
		}
	}
	o.mu.Unlock()
	for _, operation := range running {
		o.revoke(operation)
	}
	// No lifecycle/store locks are held. Already admitted durable writes may
	// finish; shutdown never acknowledges cancellation while their result is unknown.
	o.workers.Wait()
}
