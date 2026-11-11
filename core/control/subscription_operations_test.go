package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func subscriptionTestSession(t *testing.T, d *Daemon) context.Context {
	t.Helper()
	ctx, session := d.newSubscriptionSession(context.Background())
	t.Cleanup(func() {
		session.cancel()
		waitFor(t, func() bool {
			d.subscriptionOps.mu.Lock()
			defer d.subscriptionOps.mu.Unlock()
			for _, op := range d.subscriptionOps.operations {
				if op.session == session {
					return false
				}
			}
			return true
		}, "session operation cleanup")
	})
	return ctx
}

func TestSubscriptionStartCapturesPrivateScopeWithoutNetwork(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	p := addCanonicalProfile(t, store)
	var calls atomic.Int64
	d.fetch = func(context.Context, string) ([]byte, http.Header, error) { calls.Add(1); return nil, nil, nil }
	ctx := subscriptionTestSession(t, d)
	before := store.View()
	view, err := d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "refresh", ProfileID: p.ID})
	if err != nil || view.OperationID == "" || view.Stage != "prepared" || view.Profile != p.ID {
		t.Fatal(view, err)
	}
	if calls.Load() != 0 || store.View().Revision != before.Revision {
		t.Fatal("Start performed network or storage mutation")
	}
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{p.URL, "provider.test", "/private", "SYNTHETIC_ONLY"} {
		if strings.Contains(string(data), private) {
			t.Fatal("operation DTO exposed private input")
		}
	}
	other := subscriptionTestSession(t, d)
	if _, err := d.subscriptionOperationStatus(other, view.OperationID); subscriptionErrorCode(err) != "wrong_session" {
		t.Fatal("another session acquired a handle", err)
	}
	if _, err := d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "refresh", ProfileID: p.ID}); subscriptionErrorCode(err) != "busy" {
		t.Fatal("resource single-flight missing", err)
	}
	cancelled, err := d.cancelSubscriptionOperation(ctx, view.OperationID)
	if err != nil || cancelled.Stage != "cancelled" {
		t.Fatal(cancelled, err)
	}
	d.subscriptionOps.mu.Lock()
	scope := d.subscriptionOps.operations[view.OperationID].scope
	d.subscriptionOps.mu.Unlock()
	if scope != nil {
		t.Fatal("terminal handle retained URL/base snapshot")
	}
	if _, err := d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "refresh", ProfileID: p.ID}); err != nil {
		t.Fatal("terminal resource not released", err)
	}
}

func TestSubscriptionStartRequiresServerOwnedSession(t *testing.T) {
	d, _, _ := newCanonicalTestDaemon(t)
	if _, err := d.startSubscriptionOperation(context.Background(), SubscriptionStart{Kind: "import", URL: "https://provider.test/sub/SYNTHETIC_ONLY"}); subscriptionErrorCode(err) != "wrong_session" {
		t.Fatal(err)
	}
	ctx := subscriptionTestSession(t, d)
	other, _, _ := newCanonicalTestDaemon(t)
	if _, err := other.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "import", URL: "https://provider.test/sub/SYNTHETIC_ONLY"}); subscriptionErrorCode(err) != "wrong_session" {
		t.Fatal("stamp crossed daemon ownership", err)
	}
}

func TestSubscriptionPreparedCapacityTTLAndTerminalRetention(t *testing.T) {
	d, _, _ := newCanonicalTestDaemon(t)
	ctx := subscriptionTestSession(t, d)
	start := func(i int) (SubscriptionOperationView, error) {
		return d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "import", URL: fmt.Sprintf("https://provider.test/sub/%d?key=SYNTHETIC_ONLY", i)})
	}
	var handles []string
	for i := 0; i < 32; i++ {
		v, err := start(i)
		if err != nil {
			t.Fatal(i, err)
		}
		handles = append(handles, v.OperationID)
	}
	if _, err := start(32); subscriptionErrorCode(err) != "busy" {
		t.Fatal("nonterminal session capacity not enforced", err)
	}
	for _, id := range handles {
		if _, err := d.cancelSubscriptionOperation(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 32; i < 64; i++ {
		v, err := start(i)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = d.cancelSubscriptionOperation(ctx, v.OperationID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.subscriptionOperationStatus(ctx, handles[0]); subscriptionErrorCode(err) != "not_found" {
		t.Fatal("old terminal retained indefinitely", err)
	}
	d.subscriptionOps.mu.Lock()
	count := len(d.subscriptionOps.operations)
	for _, op := range d.subscriptionOps.operations {
		if op.scope != nil {
			t.Error("terminal retained private scope")
		}
	}
	d.subscriptionOps.mu.Unlock()
	if count != 32 {
		t.Fatal("terminal history not bounded", count)
	}
	view, err := start(65)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	d.now = func() time.Time { return now.Add(61 * time.Second) }
	expired, err := d.subscriptionOperationStatus(ctx, view.OperationID)
	if err != nil || expired.Stage != "failed" || expired.Code != "expired" {
		t.Fatal(expired, err)
	}
}

func TestSubscriptionHTTPPermissionIsSeparateFromDirectConsent(t *testing.T) {
	d, _, _ := newCanonicalTestDaemon(t)
	ctx := subscriptionTestSession(t, d)
	view, err := d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "import", URL: "http://provider.test/SYNTHETIC_ONLY"})
	if err != nil || view.Stage != "prepared" || view.Code != "http_opt_in_required" || view.ChallengeID != "" {
		t.Fatal(view, err)
	}
	// Equivalent effective origins share the same import resource, even while
	// the first handle waits for explicit HTTP permission.
	_, err = d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "import", URL: "http://PROVIDER.test:80/SYNTHETIC_ONLY", AllowHTTP: true})
	if subscriptionErrorCode(err) != "busy" {
		t.Fatal("equivalent URL bypassed resource single-flight", err)
	}
}
