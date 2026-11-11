package control

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/subscription"
)

func subscriptionServerStream(t *testing.T, d *Daemon) *lineClient {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	server := NewServer(d, serverConn, serverConn)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := server.serveStream(withPeerPrivilege(ctx, true))
		_ = serverConn.Close()
		server.stopWriter()
		done <- err
	}()
	client := newLineClient(t, clientConn)
	t.Cleanup(func() {
		cancel()
		_ = clientConn.Close()
		_ = serverConn.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("subscription server stream failed to stop")
		}
	})
	return client
}

func TestSubscriptionStartRealServerIDBeforeNetworkAndResponsiveStatus(t *testing.T) {
	d, store, p, _ := subscriptionWorkerFixture(t)
	var calls atomic.Int64
	entered := make(chan struct{})
	d.subscriptionOps.fetch = func(ctx context.Context, _ string, _ subscription.FetchOptions) ([]byte, http.Header, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	client := subscriptionServerStream(t, d)
	before := store.View().Revision
	client.send(Request{ID: 1, Cmd: CmdStartSubscriptionOperation, Kind: "refresh", Profile: p.ID})
	var prepared SubscriptionOperationView
	client.dataInto(client.await(), &prepared)
	if prepared.OperationID == "" || prepared.Stage != "prepared" || calls.Load() != 0 {
		t.Fatal(prepared)
	}
	for i := int64(2); i < 7; i++ {
		client.send(Request{ID: i, Cmd: CmdSubscriptionOperationStatus, OperationID: prepared.OperationID})
		var view SubscriptionOperationView
		client.dataInto(client.await(), &view)
		if view.Stage != "prepared" || calls.Load() != 0 {
			t.Fatal("scheduled Start performed network", view)
		}
	}
	client.send(Request{ID: 7, Cmd: CmdContinueSubscriptionOperation, OperationID: prepared.OperationID, Decision: "default"})
	var running SubscriptionOperationView
	client.dataInto(client.await(), &running)
	if running.Stage != "running" {
		t.Fatal(running)
	}
	<-entered
	client.send(Request{ID: 8, Cmd: CmdStatus})
	if r := client.await(); r.ID != 8 || !r.Ok {
		t.Fatal("status blocked by fetch", r)
	}
	client.send(Request{ID: 9, Cmd: CmdDisconnect})
	if r := client.await(); r.ID != 9 || !r.Ok {
		t.Fatal("disconnect blocked by fetch", r)
	}
	d.subscriptionOps.mu.Lock()
	operation := d.subscriptionOps.operations[prepared.OperationID]
	d.subscriptionOps.mu.Unlock()
	fenced, release, fenceDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() {
		fenceDone <- operation.lease.WithCommitFence(context.Background(), func() error { close(fenced); <-release; return nil })
	}()
	<-fenced
	client.send(Request{ID: 10, Cmd: CmdCancelSubscriptionOperation, OperationID: prepared.OperationID})
	client.send(Request{ID: 11, Cmd: CmdStatus})
	if r := client.await(); r.ID != 11 || !r.Ok {
		unblock()
		<-fenceDone
		t.Fatal("cancel drain held the command stream", r)
	}
	unblock()
	if err := <-fenceDone; err != nil {
		t.Fatal(err)
	}
	var cancelled SubscriptionOperationView
	response := client.await()
	if response.ID != 10 {
		t.Fatal("wrong cancel correlation", response.ID)
	}
	client.dataInto(response, &cancelled)
	if cancelled.Stage != "cancelled" || calls.Load() != 1 || store.View().Revision != before {
		t.Fatal(cancelled)
	}
}

func TestSubscriptionServerEOFOnlyRevokesOwningSession(t *testing.T) {
	d, _, p, _ := subscriptionWorkerFixture(t)
	p2 := addCanonicalProfile(t, d.canonical)
	var calls atomic.Int64
	d.subscriptionOps.fetch = func(ctx context.Context, _ string, _ subscription.FetchOptions) ([]byte, http.Header, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	first := subscriptionServerStream(t, d)
	first.send(Request{ID: 1, Cmd: CmdStartSubscriptionOperation, Kind: "refresh", Profile: p.ID})
	var a SubscriptionOperationView
	first.dataInto(first.await(), &a)
	first.send(Request{ID: 2, Cmd: CmdContinueSubscriptionOperation, OperationID: a.OperationID, Decision: "default"})
	var av SubscriptionOperationView
	first.dataInto(first.await(), &av)
	second := subscriptionServerStream(t, d)
	second.send(Request{ID: 1, Cmd: CmdSubscriptionOperationStatus, OperationID: a.OperationID})
	if r := second.await(); r.Ok || r.Error != "wrong_session" {
		t.Fatal("cross-session operation access", r)
	}
	second.send(Request{ID: 2, Cmd: CmdStartSubscriptionOperation, Kind: "refresh", Profile: p2.ID})
	var b SubscriptionOperationView
	second.dataInto(second.await(), &b)
	second.send(Request{ID: 3, Cmd: CmdContinueSubscriptionOperation, OperationID: b.OperationID, Decision: "default"})
	var bv SubscriptionOperationView
	second.dataInto(second.await(), &bv)
	waitFor(t, func() bool { return calls.Load() == 2 }, "two independent session workers")
	before := d.snapshotState().State
	_ = first.conn.Close()
	waitFor(t, func() bool {
		d.subscriptionOps.mu.Lock()
		defer d.subscriptionOps.mu.Unlock()
		return d.subscriptionOps.operations[a.OperationID] == nil
	}, "EOF revocation and private scope release")
	second.send(Request{ID: 4, Cmd: CmdSubscriptionOperationStatus, OperationID: b.OperationID})
	var stillRunning SubscriptionOperationView
	second.dataInto(second.await(), &stillRunning)
	if stillRunning.Stage != "running" || d.snapshotState().State != before {
		t.Fatal("EOF changed unrelated session or VPN", stillRunning)
	}
	second.send(Request{ID: 5, Cmd: CmdCancelSubscriptionOperation, OperationID: b.OperationID})
	var cancelled SubscriptionOperationView
	second.dataInto(second.await(), &cancelled)
	if cancelled.Stage != "cancelled" {
		t.Fatal(cancelled)
	}
}
