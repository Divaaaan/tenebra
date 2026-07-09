package subscription

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newDoHServerCounting starts a TLS DoH resolver that answers every A query with
// ip and counts how many requests it receives. Its certificate is the shared
// httptest localhost cert, so the returned pool also trusts any other httptest
// TLS server (used here for the subscription server).
func newDoHServerCounting(t *testing.T, ip net.IP) (DoHEndpoint, *x509.CertPool, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		if err != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		host, qtype, err := decodeQuestion(raw)
		if err != nil {
			http.Error(w, "bad question", http.StatusBadRequest)
			return
		}
		var rrs []testRR
		if qtype == dnsTypeA {
			rrs = []testRR{{name: host, a: ip}}
		}
		w.Header().Set("Content-Type", dohContentType)
		w.Write(buildDNSResponse(host, qtype, 0, rrs))
	}))
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return DoHEndpoint{URL: srv.URL + "/dns-query", Addr: srv.Listener.Addr().String()}, pool, &hits
}

// newSubServer starts a TLS subscription server that records the SNI of the
// connection it receives and serves body (or the given non-2xx status).
func newSubServer(t *testing.T, status int, body string) (*httptest.Server, func() string) {
	t.Helper()
	var sni atomic.Value
	sni.Store("")
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 && (status < 200 || status >= 300) {
			w.WriteHeader(status)
			return
		}
		io.WriteString(w, body)
	}))
	srv.TLS = &tls.Config{
		GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
			sni.Store(chi.ServerName)
			return nil, nil
		},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, func() string { return sni.Load().(string) }
}

func serverPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split server addr: %v", err)
	}
	return port
}

// dnsFailDial simulates a poisoned/failing system resolver deterministically,
// without any real DNS, so the fallback path can be tested offline.
func dnsFailDial(host string) dialFunc {
	return func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.DNSError{Err: "poisoned reply", Name: host, IsNotFound: true}
	}
}

func TestFetchPrimarySucceedsSkipsDoH(t *testing.T) {
	sub, _ := newSubServer(t, 200, "primary-body")
	doh, pool, hits := newDoHServerCounting(t, net.IPv4(127, 0, 0, 1))

	// Host is a loopback IP literal, so the system path connects with no DNS.
	url := "https://127.0.0.1:" + serverPort(t, sub) + "/sub?token=secret"
	cfg := fetchConfig{dohEndpoints: []DoHEndpoint{doh}, rootCAs: pool}

	body, _, err := fetchWithConfig(context.Background(), url, cfg)
	if err != nil {
		t.Fatalf("fetchWithConfig() error = %v", err)
	}
	if string(body) != "primary-body" {
		t.Fatalf("body = %q, want primary-body", body)
	}
	if hits.Load() != 0 {
		t.Fatalf("DoH resolver was queried %d times, want 0 on the primary path", hits.Load())
	}
}

func TestFetchFallbackResolvesViaDoHPreservingSNI(t *testing.T) {
	sub, sniOf := newSubServer(t, 200, "doh-body")
	// The DoH resolver maps the subscription host to the loopback sub server.
	doh, pool, hits := newDoHServerCounting(t, net.IPv4(127, 0, 0, 1))

	// example.com is covered by the shared httptest cert, so TLS to the sub
	// server verifies once DoH points the dial at loopback. The primary dial is
	// forced to fail as a poisoned resolver would.
	url := "https://example.com:" + serverPort(t, sub) + "/sub?token=secret"
	cfg := fetchConfig{
		dohEndpoints: []DoHEndpoint{doh},
		rootCAs:      pool,
		primaryDial:  dnsFailDial("example.com"),
	}

	body, _, err := fetchWithConfig(context.Background(), url, cfg)
	if err != nil {
		t.Fatalf("fetchWithConfig() error = %v", err)
	}
	if string(body) != "doh-body" {
		t.Fatalf("body = %q, want doh-body", body)
	}
	if hits.Load() == 0 {
		t.Fatal("DoH resolver was never queried on the fallback path")
	}
	// The critical property: the dial targeted the DoH-resolved loopback IP, but
	// the TLS SNI stayed the original hostname (not the IP) — required for
	// TLS/REALITY to the real origin.
	if got := sniOf(); got != "example.com" {
		t.Fatalf("SNI = %q, want example.com (original host, not the resolved IP)", got)
	}
}

func TestFetchFallbackBothResolversFail(t *testing.T) {
	sub, _ := newSubServer(t, 200, "unreached")
	_, pool, _ := newDoHServerCounting(t, net.IPv4(127, 0, 0, 1))

	// Both DoH endpoints dial a closed loopback port, so the fallback cannot
	// resolve and the call must fail with a combined error.
	url := "https://example.com:" + serverPort(t, sub) + "/sub?token=secret"
	cfg := fetchConfig{
		dohEndpoints: []DoHEndpoint{
			{URL: "https://a.invalid/dns-query", Addr: "127.0.0.1:1"},
			{URL: "https://b.invalid/dns-query", Addr: "127.0.0.1:1"},
		},
		rootCAs:     pool,
		primaryDial: dnsFailDial("example.com"),
	}

	_, _, err := fetchWithConfig(context.Background(), url, cfg)
	if err == nil {
		t.Fatal("fetchWithConfig() error = nil, want failure when both resolvers are down")
	}
	msg := err.Error()
	if !strings.Contains(msg, "example.com") || !strings.Contains(msg, "doh") {
		t.Fatalf("error = %q, want it to name the host and the DoH failure", msg)
	}
	if strings.Contains(msg, "token=secret") {
		t.Fatalf("error leaked the subscription token: %q", msg)
	}
}

func TestFetchServerRespondedSkipsDoH(t *testing.T) {
	sub, _ := newSubServer(t, http.StatusForbidden, "")
	doh, pool, hits := newDoHServerCounting(t, net.IPv4(127, 0, 0, 1))

	// The server is reachable and answers 403: DNS worked, so DoH must not run.
	url := "https://127.0.0.1:" + serverPort(t, sub) + "/sub?token=secret"
	cfg := fetchConfig{dohEndpoints: []DoHEndpoint{doh}, rootCAs: pool}

	_, _, err := fetchWithConfig(context.Background(), url, cfg)
	if err == nil || !strings.Contains(err.Error(), "unexpected status") {
		t.Fatalf("error = %v, want unexpected-status error", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("DoH resolver was queried %d times after a server response, want 0", hits.Load())
	}
}

func TestFetchCancelledContextSkipsDoH(t *testing.T) {
	sub, _ := newSubServer(t, 200, "unreached")
	doh, pool, hits := newDoHServerCounting(t, net.IPv4(127, 0, 0, 1))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call

	url := "https://example.com:" + serverPort(t, sub) + "/sub?token=secret"
	cfg := fetchConfig{
		dohEndpoints: []DoHEndpoint{doh},
		rootCAs:      pool,
		primaryDial:  dnsFailDial("example.com"),
	}

	if _, _, err := fetchWithConfig(ctx, url, cfg); err == nil {
		t.Fatal("fetchWithConfig() error = nil, want failure on cancelled context")
	}
	if hits.Load() != 0 {
		t.Fatalf("DoH resolver was queried %d times on a cancelled context, want 0", hits.Load())
	}
}
