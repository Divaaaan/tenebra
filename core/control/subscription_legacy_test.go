package control

import (
	"context"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Divaaaan/tenebra/core/subscription"
)

func TestCanonicalLegacyNetworkCannotBypassOperationPolicy(t *testing.T) {
	for _, command := range []string{CmdRefreshSub, CmdImportSubscription, "background_refresh"} {
		t.Run(command, func(t *testing.T) {
			d, store, p, ctx := subscriptionWorkerFixture(t)
			d.subscriptionOps.accepted = subscriptionAcceptedInfo{Protected: true}
			var ambient atomic.Int64
			d.fetch = func(context.Context, string) ([]byte, http.Header, error) {
				ambient.Add(1)
				return syntheticOperationBody, http.Header{}, nil
			}
			before := store.View()
			if command == "background_refresh" {
				if _, _, err := d.refreshProfile(context.Background(), p); err == nil {
					t.Error("protected background refresh was accepted")
				}
			} else {
				response := d.Handle(ctx, Request{Cmd: command, Profile: p.ID, URL: "https://provider.test/sub/SYNTHETIC_ONLY"})
				if response.Ok {
					t.Error("protected legacy request was accepted")
				}
			}
			if ambient.Load() != 0 || !reflect.DeepEqual(before, store.View()) {
				t.Fatal("legacy path bypassed operation authority and changed the repository")
			}
		})
	}
}

func TestCanonicalLegacyRequestUsesOnlyOperationWorker(t *testing.T) {
	d, _, p, ctx := subscriptionWorkerFixture(t)
	var ambient, scoped atomic.Int64
	d.fetch = func(context.Context, string) ([]byte, http.Header, error) { ambient.Add(1); return nil, nil, nil }
	d.subscriptionOps.fetch = func(_ context.Context, _ string, opts subscription.FetchOptions) ([]byte, http.Header, error) {
		if opts.Network == nil {
			t.Error("legacy worker omitted authority")
		}
		scoped.Add(1)
		return syntheticOperationBody, http.Header{}, nil
	}
	response := d.Handle(ctx, Request{Cmd: CmdRefreshSub, Profile: p.ID})
	if !response.Ok || scoped.Load() != 1 || ambient.Load() != 0 {
		t.Fatal("legacy path escaped operation worker", response.Error)
	}
}

func TestCanonicalUninitializedMaintenanceNeverOffersDirectFallback(t *testing.T) {
	d, store, _ := newCanonicalTestDaemon(t)
	p := addCanonicalProfile(t, store)
	ctx := subscriptionTestSession(t, d)
	before := store.View()
	prepared, err := d.startSubscriptionOperation(ctx, SubscriptionStart{Kind: "refresh", ProfileID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := d.continueSubscriptionOperation(ctx, prepared.OperationID, "default", "")
	if err != nil || deferred.Stage != "deferred" || deferred.Code != "waiting_for_vpn" || deferred.ChallengeID != "" {
		t.Fatal(deferred, err)
	}
	if !reflect.DeepEqual(before, store.View()) {
		t.Fatal("uninitialized authority mutated state")
	}
}
