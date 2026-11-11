package control

import (
	"context"
	"crypto/rand"
	"sync/atomic"
)

type subscriptionSessionContextKey struct{}
type subscriptionSession struct {
	id     string
	daemon *Daemon
	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool
	manual bool
}

// Only an authenticated server stream creates this stamp. No request field
// supplies or replaces the identity of an operation's owning session.
func (d *Daemon) newSubscriptionSession(ctx context.Context) (context.Context, *subscriptionSession) {
	return d.newOwnedSubscriptionSession(ctx, true)
}

func (d *Daemon) newOwnedSubscriptionSession(ctx context.Context, manual bool) (context.Context, *subscriptionSession) {
	ctx, cancel := context.WithCancel(ctx)
	session := &subscriptionSession{id: rand.Text(), daemon: d, ctx: ctx, cancel: cancel, manual: manual}
	stamped := context.WithValue(ctx, subscriptionSessionContextKey{}, session)
	context.AfterFunc(ctx, func() { d.subscriptionOps.endSession(session) })
	return stamped, session
}

func (d *Daemon) subscriptionSession(ctx context.Context) (*subscriptionSession, error) {
	session, ok := ctx.Value(subscriptionSessionContextKey{}).(*subscriptionSession)
	if !ok || session == nil || session.daemon != d || session.closed.Load() || session.ctx.Err() != nil || ctx.Err() != nil {
		return nil, subscriptionError("wrong_session")
	}
	return session, nil
}
