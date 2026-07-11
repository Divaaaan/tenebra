package subscription

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizedTierIsFailClosed(t *testing.T) {
	cases := []struct {
		name string
		ent  Entitlement
		want string
	}{
		{"active premium", Entitlement{Tier: "premium", Active: true}, TierPremium},
		{"inactive premium", Entitlement{Tier: "premium", Active: false}, TierFree},
		{"active free", Entitlement{Tier: "free", Active: true}, TierFree},
		{"unknown tier", Entitlement{Tier: "gold", Active: true}, TierFree},
		{"empty", Entitlement{}, TierFree},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ent.NormalizedTier(); got != tc.want {
				t.Fatalf("NormalizedTier() = %q, want %q", got, tc.want)
			}
		})
	}
}

// entitlementServer stands in for the operator's endpoint: it records the last
// request (method, path, decoded body) and replies with a canned status/body.
type entitlementServer struct {
	origin  string
	status  int
	body    string
	gotPath string
	gotBody entitlementRequest
}

func newEntitlementServer(t *testing.T, status int, body string) *entitlementServer {
	t.Helper()
	es := &entitlementServer{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		es.gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &es.gotBody)
		w.WriteHeader(es.status)
		_, _ = io.WriteString(w, es.body)
	}))
	t.Cleanup(srv.Close)
	es.origin = srv.URL
	return es
}

// origin is set after the server starts.
func (es *entitlementServer) fetch(t *testing.T, key string) (Entitlement, error) {
	t.Helper()
	// blockAddr is left nil so the SSRF guard permits the loopback httptest server.
	return fetchEntitlementWithConfig(context.Background(), es.origin, key, entitlementConfig{})
}

func TestFetchEntitlementParsesPremium(t *testing.T) {
	es := newEntitlementServer(t, http.StatusOK,
		`{"tier":"premium","active":true,"expires":"2026-12-31T00:00:00Z","features":["a","b"]}`)
	ent, err := es.fetch(t, managedKey)
	if err != nil {
		t.Fatalf("FetchEntitlement: %v", err)
	}
	if ent.NormalizedTier() != TierPremium {
		t.Errorf("tier = %q, want premium", ent.NormalizedTier())
	}
	if len(ent.Features) != 2 {
		t.Errorf("features = %v, want 2 entries", ent.Features)
	}
	// The request must hit the entitlement path and carry the key.
	if es.gotPath != entitlementPath {
		t.Errorf("path = %q, want %q", es.gotPath, entitlementPath)
	}
	if es.gotBody.Key != managedKey {
		t.Errorf("request key = %q, want %q", es.gotBody.Key, managedKey)
	}
}

func TestFetchEntitlementParsesFreeAndNullExpiry(t *testing.T) {
	es := newEntitlementServer(t, http.StatusOK,
		`{"tier":"free","active":false,"expires":null,"features":[]}`)
	ent, err := es.fetch(t, managedKey)
	if err != nil {
		t.Fatalf("FetchEntitlement: %v", err)
	}
	if ent.NormalizedTier() != TierFree {
		t.Errorf("tier = %q, want free", ent.NormalizedTier())
	}
	if ent.Expires != "" {
		t.Errorf("expires = %q, want empty for null", ent.Expires)
	}
}

func TestFetchEntitlementErrorsOnBadStatus(t *testing.T) {
	es := newEntitlementServer(t, http.StatusInternalServerError, `{"tier":"premium","active":true}`)
	if _, err := es.fetch(t, managedKey); err == nil {
		t.Fatal("FetchEntitlement returned nil error on 500, want error (fail-closed at the caller)")
	}
}

func TestFetchEntitlementErrorsOnMalformedBody(t *testing.T) {
	es := newEntitlementServer(t, http.StatusOK, `not json`)
	if _, err := es.fetch(t, managedKey); err == nil {
		t.Fatal("FetchEntitlement returned nil error on malformed body, want error")
	}
}
