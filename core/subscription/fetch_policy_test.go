package subscription

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRedirectOriginPolicy(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("X-Private-Panel") != "" || r.Header.Get("Referer") != "")
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/destination", http.StatusFound)
	}))
	defer origin.Close()
	cfg := fetchConfig{rootCAs: subPool(t, origin), headers: map[string]string{"X-Private-Panel": "SYNTHETIC_ONLY", "Referer": "https://provider.test/private"}}
	_, _, err := fetchWithConfig(context.Background(), origin.URL+"/subscription", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if leaked.Load() {
		t.Fatal("changed-port redirect received private caller headers or Referer")
	}
}

func TestHTTPSDowngradeRejected(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); io.WriteString(w, "inert") }))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/inert", http.StatusFound)
	}))
	defer origin.Close()
	_, _, err := fetchWithConfig(context.Background(), origin.URL, fetchConfig{rootCAs: subPool(t, origin)})
	if err == nil || hits.Load() != 0 {
		t.Fatal("HTTPS-to-HTTP redirect reached a cleartext target")
	}
}

func TestHTTPRequiresProfileOptIn(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer server.Close()
	_, _, err := fetchWithConfig(context.Background(), server.URL, fetchConfig{})
	if err == nil || hits.Load() != 0 {
		t.Fatal("HTTP without explicit profile policy reached the network")
	}
}

func TestGuardBlocksCGNATBenchmarkAndMappedIPv6(t *testing.T) {
	for _, address := range []string{"100.64.0.1", "100.127.255.254", "198.18.0.1", "198.19.255.254", "::ffff:100.64.0.1", "::ffff:198.18.0.1"} {
		if !blockedByDefaultIP(net.ParseIP(address)) {
			t.Errorf("unsafe range accepted: %s", address)
		}
	}
}

func TestBodyLimitIsErrorNotTruncation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(w, strings.NewReader(strings.Repeat("x", maxBody+1)))
	}))
	defer server.Close()
	body, _, err := fetchWithConfig(context.Background(), server.URL, fetchConfig{rootCAs: subPool(t, server)})
	if err == nil || body != nil {
		t.Fatalf("oversized response returned truncated success: bytes=%d", len(body))
	}
}

func TestEntitlementAndDoHRejectRedirects(t *testing.T) {
	for _, operation := range []string{"entitlement", "doh"} {
		t.Run(operation, func(t *testing.T) {
			var hits atomic.Int64
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				io.WriteString(w, `{"tier":"premium","active":true}`)
			}))
			defer target.Close()
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/inert", http.StatusFound)
			}))
			defer origin.Close()
			var err error
			if operation == "entitlement" {
				_, err = fetchEntitlementWithConfig(context.Background(), origin.URL, "SYNTHETIC_ONLY", entitlementConfig{rootCAs: subPool(t, origin)})
			} else {
				resolver := newDoHResolver([]DoHEndpoint{{URL: origin.URL + "/dns-query", Addr: origin.Listener.Addr().String()}}, subPool(t, origin))
				defer resolver.Close()
				_, err = resolver.resolve(context.Background(), "inert.test")
			}
			if err == nil || hits.Load() != 0 {
				t.Fatalf("%s followed a redirect: hits=%d", operation, hits.Load())
			}
		})
	}
}
