package tenebracore

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
)

type fetchCall struct {
	url     string
	headers map[string]string
}

// stubFetch returns a fetchFunc that serves body/header and records the url and
// headers it was called with, so a test can assert both the parse result and that
// the envelope's request headers were threaded through. It touches no network.
func stubFetch(body string, header http.Header) (fetchFunc, *fetchCall) {
	call := &fetchCall{}
	fn := func(ctx context.Context, url string, headers map[string]string) ([]byte, http.Header, error) {
		call.url = url
		call.headers = headers
		return []byte(body), header, nil
	}
	return fn, call
}

func TestImportSubscriptionHappyPath(t *testing.T) {
	body := "vless://abc@e.com:443#a\ntrojan://pw@example.com:443#b\n"
	header := http.Header{}
	header.Set("Subscription-Userinfo", "upload=1; download=2; total=100; expire=1893456000")
	fetch, call := stubFetch(body, header)

	env := `{"url":"https://vpsxd.pro/sub/vpn_12_abcdef12/` + strings.Repeat("a", 32) + `","headers":{"X-Token":"t"}}`
	out, err := importSubscription(env, fetch)
	if err != nil {
		t.Fatalf("importSubscription() error = %v", err)
	}
	var p profile.Profile
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("output is not a profile: %v", err)
	}
	if len(p.Servers) != 2 {
		t.Fatalf("servers = %d, want 2", len(p.Servers))
	}
	if p.Source != profile.SourceSubscription {
		t.Errorf("source = %q, want subscription", p.Source)
	}
	// Userinfo folded in: used = upload+download, total as given, expiry set.
	if p.TrafficUsed != 3 || p.TrafficTotal != 100 {
		t.Errorf("traffic used/total = %d/%d, want 3/100", p.TrafficUsed, p.TrafficTotal)
	}
	if p.ExpiresAt == nil {
		t.Error("expiresAt not set from Subscription-Userinfo")
	}
	// The managed shape (vpsxd.pro + managed path) is recognised for the badge.
	if !p.Managed {
		t.Error("managed subscription not recognised")
	}
	// Server IDs are the stable content hashes production assigns, not empty.
	if p.Servers[0].ID == "" {
		t.Error("server ID empty; expected a stable content-derived id")
	}
	// The envelope's request headers reached the fetch.
	if call.headers["X-Token"] != "t" {
		t.Errorf("fetch headers = %v, want X-Token=t threaded through", call.headers)
	}
}

func TestImportSubscriptionErrors(t *testing.T) {
	okFetch, _ := stubFetch("vless://abc@e.com:443#a\n", http.Header{})

	if _, err := importSubscription("{bad", okFetch); err == nil {
		t.Error("malformed JSON: want error")
	}
	if _, err := importSubscription(`{}`, okFetch); err == nil {
		t.Error("missing url: want error")
	}

	failFetch := func(ctx context.Context, url string, h map[string]string) ([]byte, http.Header, error) {
		return nil, nil, context.DeadlineExceeded
	}
	if _, err := importSubscription(`{"url":"https://e.com/s"}`, failFetch); err == nil {
		t.Error("fetch error: want propagation")
	}

	emptyFetch, _ := stubFetch("not a link at all\n", http.Header{})
	if _, err := importSubscription(`{"url":"https://e.com/s"}`, emptyFetch); err == nil {
		t.Error("no parseable nodes: want error")
	}
}

// TestImportSubscriptionRejectsSSRF drives the real production fetch (defaultFetch)
// at a cloud-metadata literal. The SSRF guard refuses it before any socket opens,
// so this touches no network while proving the hardened fetch is wired in.
func TestImportSubscriptionRejectsSSRF(t *testing.T) {
	env := `{"url":"https://169.254.169.254/latest/meta-data/"}`
	if _, err := importSubscription(env, defaultFetch); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("importSubscription() error = %v, want a blocked-address SSRF error", err)
	}
}
