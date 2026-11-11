package control

import (
	"context"
	"errors"
	"sync"

	"github.com/Divaaaan/tenebra/core/state"
)

// This lock protects only entry bookkeeping. No Store transaction, network
// operation, native cleanup or worker join may run while it is held.
type businessAdmission struct {
	mu     sync.Mutex
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
	active sync.WaitGroup
	done   chan struct{}
	err    error
}

func (d *Daemon) MaintenanceFenceStarted() bool {
	d.business.mu.Lock()
	defer d.business.mu.Unlock()
	return d.business.closed
}

func (d *Daemon) beginBusiness(ctx context.Context) (context.Context, func(), error) {
	b := &d.business
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, state.ErrMaintenancePending
	}
	if err := d.CanonicalReady(); err != nil {
		return nil, nil, err
	}
	if b.ctx == nil {
		b.ctx, b.cancel = context.WithCancel(context.Background())
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(b.ctx, cancel)
	b.active.Add(1)
	var once sync.Once
	return child, func() {
		once.Do(func() { stop(); cancel(); b.active.Done() })
	}, nil
}

// BeginMaintenanceFence permanently closes this process's business admission.
// It is safe in the protected journal's short CAS callback: draining happens
// separately, outside all journal/lifecycle metadata locks.
func (d *Daemon) BeginMaintenanceFence() {
	b := &d.business
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.done = make(chan struct{})
	if b.cancel != nil {
		b.cancel()
	}
	b.mu.Unlock()
	go func() {
		var fenceErr error
		if d.canonical != nil {
			// Already admitted durable writes must resolve even when the helper's
			// particular request times out. A timeout never reopens this gate.
			fenceErr = d.canonical.Fence(context.Background())
		}
		b.active.Wait()
		closeErr := d.Close()
		b.mu.Lock()
		b.err = errors.Join(fenceErr, closeErr)
		close(b.done)
		b.mu.Unlock()
	}()
}

// FenceForMaintenance acknowledges only a fully drained old process. The
// bounded caller can retry after a timeout; the same drain keeps running.
func (d *Daemon) FenceForMaintenance(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.BeginMaintenanceFence()
	b := &d.business
	b.mu.Lock()
	done := b.done
	b.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		b.mu.Lock()
		err := b.err
		b.mu.Unlock()
		return err
	}
}
