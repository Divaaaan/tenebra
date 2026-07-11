package subscription

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

// entitlementPath is the endpoint, relative to a managed subscription's origin,
// that answers an entitlement lookup.
const entitlementPath = "/api/v1/subscription/entitlement"

// Tier values an entitlement lookup resolves to. They are UX labels, not
// capabilities: what a premium subscription can actually do is decided by the
// data the operator's server returns, never by this string.
const (
	TierPremium = "premium"
	TierFree    = "free"
)

// Entitlement is the answer the operator's endpoint gives for a managed
// subscription key. Tier and Active together decide the effective tier (see
// NormalizedTier); Expires and Features are carried for future use (they let a
// later iteration show an entitlement's end date or gate a specific capability)
// and tolerate being absent or null.
type Entitlement struct {
	Tier     string   `json:"tier"`
	Active   bool     `json:"active"`
	Expires  string   `json:"expires"`
	Features []string `json:"features"`
}

// NormalizedTier folds the raw answer into a single effective tier, fail-closed:
// it returns "premium" only for an explicitly active premium entitlement, and
// "free" for anything else — inactive, a non-premium tier, or an unrecognised
// value. A premium badge therefore only ever shows on a positive, active answer.
func (e Entitlement) NormalizedTier() string {
	if e.Active && e.Tier == TierPremium {
		return TierPremium
	}
	return TierFree
}

// entitlementRequest is the POST body: the 32-hex subscription key.
type entitlementRequest struct {
	Key string `json:"key"`
}

// entitlementConfig carries the knobs the internal lookup honours. Production
// uses defaultEntitlementConfig; tests override the fields to reach a local
// httptest server (the SSRF guard would otherwise refuse loopback).
type entitlementConfig struct {
	// blockAddr is the SSRF guard predicate, screening every dialled address
	// (and redirect hop) the same way the subscription fetch does. Production
	// sets it to blockedByDefaultIP unless the operator opted out; nil disables
	// the guard, which is how tests reach loopback.
	blockAddr func(net.IP) bool
	// rootCAs overrides the system trust store when non-nil. Production leaves it
	// nil; tests point it at an httptest server's certificate.
	rootCAs *x509.CertPool
}

func defaultEntitlementConfig() entitlementConfig {
	cfg := entitlementConfig{}
	if !ssrfGuardDisabled() {
		cfg.blockAddr = blockedByDefaultIP
	}
	return cfg
}

// FetchEntitlement asks a managed subscription's origin for the entitlement of
// one key. It POSTs {"key":...} to origin + entitlementPath and decodes the
// answer. It reuses the subscription fetch's transport hardening — the single
// client-initiated TLS renegotiation some Cloudflare-fronted origins require,
// and the SSRF guard on every dialled address — but intentionally does not
// carry the DoH fallback: the endpoint sits behind the operator's own CDN, so a
// poisoned resolver is not the failure mode to design around here.
//
// A transport failure, a non-2xx status, or a body that does not decode is
// returned as an error; the daemon treats every such failure as the free tier
// (fail-closed), so a lookup that cannot complete never grants premium.
func FetchEntitlement(ctx context.Context, origin, key string) (Entitlement, error) {
	return fetchEntitlementWithConfig(ctx, origin, key, defaultEntitlementConfig())
}

func fetchEntitlementWithConfig(ctx context.Context, origin, key string, cfg entitlementConfig) (Entitlement, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint, err := entitlementURL(origin)
	if err != nil {
		return Entitlement{}, err
	}
	host := hostForLog(endpoint)
	if err := checkScheme(endpoint, host); err != nil {
		return Entitlement{}, err
	}
	reqBody, err := json.Marshal(entitlementRequest{Key: key})
	if err != nil {
		return Entitlement{}, fmt.Errorf("subscription: entitlement: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return Entitlement{}, fmt.Errorf("subscription: entitlement: %s", scrub(err, endpoint))
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Clone the default transport (keeping its proxy/HTTP2 defaults) and permit
	// the single client-initiated TLS renegotiation, matching doFetch. Route the
	// dial through the same SSRF guard so a crafted origin cannot pivot to
	// loopback/metadata/private space.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.Renegotiation = tls.RenegotiateOnceAsClient
	if cfg.rootCAs != nil {
		transport.TLSClientConfig.RootCAs = cfg.rootCAs
	}
	transport.DialContext = guardDial(transport.DialContext, cfg.blockAddr)

	client := &http.Client{Timeout: fetchTimeout, Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return Entitlement{}, fmt.Errorf("subscription: entitlement: %s", scrub(err, endpoint))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Entitlement{}, fmt.Errorf("subscription: entitlement: unexpected status %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Entitlement{}, fmt.Errorf("subscription: entitlement: read body: %w", err)
	}
	var ent Entitlement
	if err := json.Unmarshal(body, &ent); err != nil {
		return Entitlement{}, fmt.Errorf("subscription: entitlement: decode response: %w", err)
	}
	return ent, nil
}

// entitlementURL derives the entitlement endpoint from a managed subscription's
// origin, replacing any path/query/fragment with entitlementPath so only the
// scheme and host carry over.
func entitlementURL(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("subscription: entitlement: invalid origin %q", hostForLog(origin))
	}
	u.Path = entitlementPath
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
