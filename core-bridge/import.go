package tenebracore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/subscription"
)

// importRequest is the JSON envelope importSubscription accepts.
type importRequest struct {
	// URL is the subscription URL to fetch. Required.
	URL string `json:"url"`
	// Headers are optional extra request headers, for a panel that gates its
	// response on one. They are threaded through the same hardened fetch (SSRF
	// guard + DoH fallback) as the URL, so setting one costs no protection.
	Headers map[string]string `json:"headers,omitempty"`
	// Name is the profile's display name. Empty falls back to the URL, matching the
	// desktop import.
	Name string `json:"name,omitempty"`
}

// fetchFunc matches subscription.FetchWithHeaders. It is a seam: production wires
// in defaultFetch, tests inject a stub that returns a canned body so no test
// touches the network.
type fetchFunc func(ctx context.Context, url string, headers map[string]string) ([]byte, http.Header, error)

// defaultFetch is the production fetch — the core's hardened FetchWithHeaders (SSRF
// guard + DoH fallback) — carrying any envelope request headers.
func defaultFetch(ctx context.Context, url string, headers map[string]string) ([]byte, http.Header, error) {
	return subscription.FetchWithHeaders(ctx, url, headers)
}

// ImportSubscription fetches a subscription URL and returns the JSON of the profile
// built from it — its nodes plus any quota/expiry. It is the "import a subscription,
// get a profile" call the app makes before GenerateConfig. See importRequest for the
// envelope. It runs the production fetch (defaultFetch); the fetch seam is exercised
// by the unexported importSubscription below, which the tests drive with a stub.
func ImportSubscription(requestJSON string) (string, error) {
	return importSubscription(requestJSON, defaultFetch)
}

// importSubscription fetches a subscription, parses its nodes, and builds a stored
// profile from them — the mobile equivalent of the desktop daemon's
// import_subscription handler. It mirrors that path field for field: fetch, parse,
// reject an empty node set, NewProfile (same content-stable server IDs so ping and
// connection state survive a refresh), fold in the Subscription-Userinfo quota,
// and flag a managed subscription for the UI badge.
//
// Unlike the desktop control reply, this returns the FULL profile JSON, credentials
// included. The desktop redacts because the profile crosses a local control socket;
// here the mobile app is itself the profile store and needs the whole profile to
// call GenerateConfig later, so there is nothing to redact for and doing so would
// strip the very fields the next step needs.
func importSubscription(requestJSON string, fetch fetchFunc) (string, error) {
	var req importRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("tenebracore: decode import request: %w", err)
	}
	if req.URL == "" {
		return "", fmt.Errorf("tenebracore: import: missing url")
	}

	body, header, err := fetch(context.Background(), req.URL, req.Headers)
	if err != nil {
		return "", fmt.Errorf("tenebracore: import: %w", err)
	}
	nodes, _, err := subscription.ParseSubscription(body)
	if err != nil {
		return "", fmt.Errorf("tenebracore: import: %w", err)
	}
	if len(nodes) == 0 {
		return "", fmt.Errorf("tenebracore: import: no usable nodes in subscription")
	}

	name := req.Name
	if name == "" {
		name = req.URL
	}
	p, err := profile.NewProfile(name, profile.SourceSubscription, req.URL, nodes)
	if err != nil {
		return "", fmt.Errorf("tenebracore: import: %w", err)
	}
	applyUserInfo(&p, header.Get("Subscription-Userinfo"))
	// Recognise a managed (operator-served) subscription so the app can badge it.
	// This gates only presentation, never any capability, exactly as on the desktop.
	p.Managed = subscription.DetectManaged(req.URL)

	out, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("tenebracore: import: encode profile: %w", err)
	}
	return string(out), nil
}

// applyUserInfo folds a Subscription-Userinfo response header into the profile's
// traffic and expiry, matching the desktop daemon's applyUserInfo. A blank or
// unparseable header is a no-op, so a panel that omits it simply leaves the quota
// unset rather than zeroing it.
func applyUserInfo(p *profile.Profile, header string) {
	if header == "" {
		return
	}
	info, err := subscription.ParseUserInfo(header)
	if err != nil {
		return
	}
	p.TrafficUsed = info.Upload + info.Download
	p.TrafficTotal = info.Total
	if !info.Expire.IsZero() {
		e := info.Expire.UTC()
		p.ExpiresAt = &e
	}
}
