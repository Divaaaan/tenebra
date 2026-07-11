package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/subscription"
)

// managedSubURL is an operator-served subscription URL: an allowlisted host with
// the managed path shape, so DetectManaged recognises it and EntitlementTarget
// can derive its origin and key.
const managedSubURL = "https://vpsxd.pro/sub/vpn_777_deadbeef/00112233445566778899aabbccddeeff"

// entitlementStub is an injectable d.entitlement returning a fixed answer, and
// flipping called when it runs.
func entitlementStub(ent subscription.Entitlement, err error, called *bool) func(context.Context, string, string) (subscription.Entitlement, error) {
	return func(context.Context, string, string) (subscription.Entitlement, error) {
		if called != nil {
			*called = true
		}
		return ent, err
	}
}

// TestLookupTierFailClosed locks the tier resolution: premium only on a positive,
// active answer for a managed subscription; free for every other managed outcome
// (inactive, non-premium, or a lookup error); and no tier at all when the
// subscription is not managed.
func TestLookupTierFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		managed bool
		ent     subscription.Entitlement
		err     error
		want    string
	}{
		{"non-managed", false, subscription.Entitlement{Tier: "premium", Active: true}, nil, ""},
		{"managed premium", true, subscription.Entitlement{Tier: "premium", Active: true}, nil, subscription.TierPremium},
		{"managed free", true, subscription.Entitlement{Tier: "free", Active: true}, nil, subscription.TierFree},
		{"managed inactive premium", true, subscription.Entitlement{Tier: "premium", Active: false}, nil, subscription.TierFree},
		{"managed lookup error", true, subscription.Entitlement{}, errors.New("endpoint down"), subscription.TierFree},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := daemonWithFetch(t, &fakeFetch{})
			d.entitlement = entitlementStub(tc.ent, tc.err, nil)
			if got := d.lookupTier(context.Background(), managedSubURL, tc.managed); got != tc.want {
				t.Fatalf("lookupTier = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRedactProfileCarriesManagedTier confirms the managed flag and tier survive
// the wire projection (they are not secret) and are omitted when empty.
func TestRedactProfileCarriesManagedTier(t *testing.T) {
	rp := redactProfile(profile.Profile{
		ID: "p1", Name: "N", Source: profile.SourceSubscription,
		Managed: true, Tier: subscription.TierPremium,
	})
	if !rp.Managed || rp.Tier != subscription.TierPremium {
		t.Fatalf("redactProfile dropped managed/tier: %+v", rp)
	}
	keys := wireKeys(t, rp)
	if _, ok := keys["managed"]; !ok {
		t.Error("wire missing managed")
	}
	if _, ok := keys["tier"]; !ok {
		t.Error("wire missing tier")
	}

	// A plain profile omits both (omitempty), so a non-managed profile's wire form
	// is byte-for-byte what it was before this field pair existed.
	plain := wireKeys(t, redactProfile(profile.Profile{ID: "p2", Source: profile.SourceManual}))
	if _, ok := plain["managed"]; ok {
		t.Error("wire should omit managed when false")
	}
	if _, ok := plain["tier"]; ok {
		t.Error("wire should omit tier when empty")
	}
}

func wireKeys(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// TestImportSubscriptionManagedSetsTierAsync drives a managed import: the profile
// is recognised as managed synchronously (on the reply), and the background
// entitlement lookup lands the premium tier on the stored profile and signals the
// UI — without ever blocking or failing the import itself.
func TestImportSubscriptionManagedSetsTierAsync(t *testing.T) {
	f := &fakeFetch{bodies: map[string]string{managedSubURL: vlessLink}}
	d, store := daemonWithFetch(t, f)
	var entCalled bool
	d.entitlement = entitlementStub(subscription.Entitlement{Tier: "premium", Active: true}, nil, &entCalled)
	rec := &eventRecorder{}
	d.SetEmitter(rec.emit)

	resp := d.handleImportSubscription(context.Background(),
		Request{ID: 1, Cmd: CmdImportSubscription, URL: managedSubURL, Name: "Provider"})
	if !resp.Ok {
		t.Fatalf("import failed: %s", resp.Error)
	}

	var out struct {
		Profile struct {
			ID      string `json:"id"`
			Managed bool   `json:"managed"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if !out.Profile.Managed {
		t.Error("reply profile.managed = false, want true (set synchronously)")
	}

	// The background lookup is what sets the tier; wait for it, then assert.
	d.entWG.Wait()
	if !entCalled {
		t.Error("entitlement lookup did not run for a managed import")
	}
	if got := mustGet(t, store, out.Profile.ID); got.Tier != subscription.TierPremium {
		t.Errorf("stored tier = %q, want premium", got.Tier)
	}
	var sawProfiles bool
	for _, ev := range rec.events {
		if ev.name == EventProfiles {
			sawProfiles = true
		}
	}
	if !sawProfiles {
		t.Error("no profiles event emitted after the tier landed")
	}
}

// TestImportSubscriptionNonManagedSkipsEntitlement confirms a third-party
// subscription is never marked managed and never triggers an entitlement lookup.
func TestImportSubscriptionNonManagedSkipsEntitlement(t *testing.T) {
	const subURL = "https://example.invalid/sub"
	f := &fakeFetch{bodies: map[string]string{subURL: vlessLink}}
	d, store := daemonWithFetch(t, f)
	d.entitlement = func(context.Context, string, string) (subscription.Entitlement, error) {
		t.Error("entitlement lookup must not run for a non-managed import")
		return subscription.Entitlement{}, nil
	}

	resp := d.handleImportSubscription(context.Background(),
		Request{ID: 1, Cmd: CmdImportSubscription, URL: subURL, Name: "Third party"})
	if !resp.Ok {
		t.Fatalf("import failed: %s", resp.Error)
	}
	d.entWG.Wait() // no lookup goroutine was spawned; returns at once
	for _, p := range store.List() {
		if p.Managed {
			t.Error("non-managed import was marked managed")
		}
		if p.Tier != "" {
			t.Errorf("non-managed tier = %q, want empty", p.Tier)
		}
	}
}

// TestRefreshRevalidatesTier drives the inline re-validation on refresh: a lost
// entitlement drops premium to free (fail-closed), and a subsequent good answer
// restores it — both reported as a material change so the UI reloads.
func TestRefreshRevalidatesTier(t *testing.T) {
	f := &fakeFetch{
		bodies:  map[string]string{managedSubURL: vlessLink},
		headers: map[string]http.Header{managedSubURL: {}},
	}
	d, store := daemonWithFetch(t, f)
	p := seedSubscription(t, store, "Provider", managedSubURL)
	p.Managed = true
	p.Tier = subscription.TierPremium
	if err := store.Update(p); err != nil {
		t.Fatalf("seed premium: %v", err)
	}

	// Endpoint down → fail-closed to free.
	d.entitlement = entitlementStub(subscription.Entitlement{}, errors.New("down"), nil)
	downgraded, changed, err := d.refreshProfile(context.Background(), mustGet(t, store, p.ID))
	if err != nil {
		t.Fatalf("refresh (downgrade): %v", err)
	}
	if downgraded.Tier != subscription.TierFree {
		t.Errorf("tier after failed re-validation = %q, want free", downgraded.Tier)
	}
	if !downgraded.Managed {
		t.Error("managed must stay true for a managed URL")
	}
	if !changed {
		t.Error("a premium->free downgrade must count as a change")
	}

	// Endpoint back with an active premium answer → premium restored.
	d.entitlement = entitlementStub(subscription.Entitlement{Tier: "premium", Active: true}, nil, nil)
	restored, changed, err := d.refreshProfile(context.Background(), mustGet(t, store, p.ID))
	if err != nil {
		t.Fatalf("refresh (restore): %v", err)
	}
	if restored.Tier != subscription.TierPremium {
		t.Errorf("tier after successful re-validation = %q, want premium", restored.Tier)
	}
	if !changed {
		t.Error("a free->premium upgrade must count as a change")
	}
}
