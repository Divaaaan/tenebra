package control

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/core/zapret"
)

func TestMaintenanceFenceCancelsAndJoinsActualBusinessHandler(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseWork := func() { once.Do(func() { close(release) }) }
	defer releaseWork()
	d.SetZapretUpdater(func(ctx context.Context) (zapret.Release, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		// A native callback may take time to unwind after observing cancellation.
		<-release
		return zapret.Release{}, ctx.Err()
	}, nil, nil)
	response := make(chan Response, 1)
	go func() {
		response <- d.Handle(withPeerPrivilege(context.Background(), true), Request{ID: 1, Cmd: CmdUpdateZapret})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("business handler did not reach the injected updater")
	}
	short, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := d.FenceForMaintenance(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acknowledged before the native callback drained: %v", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("fence did not cancel the actual handler context")
	}
	if err := store.Ready(); !errors.Is(err, state.ErrMaintenancePending) {
		t.Fatal("canonical writer gate was left open", err)
	}
	if reply := d.Handle(context.Background(), Request{ID: 2, Cmd: CmdSetDNS}); reply.Error != "maintenance_pending" {
		t.Fatal("new business entry was admitted during drain", reply)
	}
	if reply := d.Handle(context.Background(), Request{ID: 3, Cmd: CmdStatus}); !reply.Ok {
		t.Fatal("status became unavailable during drain", reply)
	}
	releaseWork()
	select {
	case reply := <-response:
		if reply.Ok {
			t.Fatal("cancelled update reported success", reply)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not drain")
	}
	retry, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := d.FenceForMaintenance(retry); err != nil {
		t.Fatal("same drain could not be acknowledged after completion", err)
	}
	if err := d.FenceForMaintenance(retry); err != nil {
		t.Fatal("completed fence was not idempotent", err)
	}
}

func TestMaintenanceFenceJoinsEmbeddedBackgroundStartup(t *testing.T) {
	d, _, _ := newCanonicalTestDaemon(t)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseWork := func() { once.Do(func() { close(release) }) }
	defer releaseWork()
	d.SetZapretUpdater(nil, nil, func(string) ([]zapret.Strategy, error) {
		close(entered)
		<-release
		return nil, errors.New("synthetic embedded install stopped")
	})
	go func() { defer close(finished); d.RunZapretAutoUpdate(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("background startup did not reach embedded callback")
	}
	short, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := d.FenceForMaintenance(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("background callback outlived a successful fence", err)
	}
	releaseWork()
	retry, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := d.FenceForMaintenance(retry); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("background loop outlived fence acknowledgement")
	}
}

func TestMaintenanceFenceRejectsLateInternalConnectEvenIfStoreReadmitted(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	p := addCanonicalProfile(t, store)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.FenceForMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	// An old process must never revive because a queued internal reconnect or
	// stale local caller has reopened just the Store.
	if err := store.Admit(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.startConnect(context.Background(), p, "", true, false, ""); !errors.Is(err, state.ErrMaintenancePending) {
		t.Fatal("late internal connect bypassed process fence", err)
	}
	if err := d.BusinessReady(); !errors.Is(err, state.ErrMaintenancePending) {
		t.Fatal("process fence reopened", err)
	}
}
