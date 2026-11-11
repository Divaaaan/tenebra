package subscription

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/dnswire"
	"github.com/Divaaaan/tenebra/core/maintenance"
)

type operationFixture struct {
	lease                              *maintenance.Lease
	resolver                           *DoHResolver
	roots                              *x509.CertPool
	origin                             string
	dohDials, originDials, badDeadline atomic.Int64
}

func operationNetworkFixture(t *testing.T, handler http.Handler) *operationFixture {
	t.Helper()
	fixture := &operationFixture{}
	origin := httptest.NewTLSServer(handler)
	t.Cleanup(origin.Close)
	doh, _ := newFakeDoHServer(t, func(host string, kind uint16) []testRR {
		if host != "example.com" {
			t.Error("unexpected resolver authority")
		}
		if kind == dnswire.TypeA {
			return []testRR{{name: host, a: net.ParseIP("203.0.113.8")}}
		}
		return nil
	})
	fixture.roots = x509.NewCertPool()
	fixture.roots.AddCert(origin.Certificate())
	fixture.roots.AddCert(doh.Certificate())
	port := serverPort(t, origin)
	fixture.origin = "https://example.com:" + port
	originTarget := netip.MustParseAddrPort("203.0.113.8:" + port)
	dohTarget := netip.MustParseAddrPort("203.0.113.53:443")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	lease, err := maintenance.NewLease(ctx, maintenance.LeaseSpec{OperationID: "synthetic-operation", SessionID: "synthetic-session", ProfileID: "synthetic-profile", EngineRunID: "synthetic-live-run", Egress: maintenance.VPNOnly, Deadline: time.Now().Add(45 * time.Second)}, func(ctx context.Context, kind string, target netip.AddrPort, register func(net.Conn) (net.Conn, error)) (net.Conn, error) {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(time.Now().Add(45*time.Second)) {
			fixture.badDeadline.Add(1)
		}
		var address string
		switch target {
		case originTarget:
			fixture.originDials.Add(1)
			address = origin.Listener.Addr().String()
		case dohTarget:
			fixture.dohDials.Add(1)
			address = doh.Listener.Addr().String()
		default:
			return nil, errors.New("numeric destination left captured authority")
		}
		var dialer net.Dialer
		raw, err := dialer.DialContext(ctx, kind, address)
		if err != nil {
			return nil, err
		}
		registered, err := register(raw)
		if err != nil {
			raw.Close()
			return nil, err
		}
		return registered, nil
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	fixture.lease = lease
	fixture.resolver = newDoHResolverUsingDial([]DoHEndpoint{{URL: "https://example.com/dns-query", Addr: dohTarget.String()}}, fixture.roots, lease.DialContext)
	if err := lease.BindResolver(fixture.resolver.LookupIP); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		lease.Revoke(nil)
		fixture.resolver.Close()
		cancel()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := lease.WaitDrained(ctx); err != nil {
			t.Error(err)
		}
	})
	return fixture
}

func (f *operationFixture) fetch(ctx context.Context, path string) ([]byte, error) {
	body, _, err := fetchWithConfig(ctx, f.origin+path, fetchConfig{network: f.lease, rootCAs: f.roots, blockAddr: blockedByDefaultIP, limits: DefaultLimits(), disableFallback: true})
	return body, err
}

func TestOperationNetworkAllPathsUseLease(t *testing.T) {
	var bodyHits, entitlementHits, ambient atomic.Int64
	fixture := operationNetworkFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.Header.Get("Referer") != "" {
			t.Error("referer leaked")
		}
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/body", http.StatusFound)
		case "/body":
			bodyHits.Add(1)
			io.WriteString(w, "SYNTHETIC_OPERATION_BODY")
		case entitlementPath:
			entitlementHits.Add(1)
			io.WriteString(w, `{"tier":"premium","active":true}`)
		default:
			t.Error("unexpected operation path")
			http.Error(w, "unexpected", 404)
		}
	}))
	oldResolver, oldTransport := net.DefaultResolver, http.DefaultTransport
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		ambient.Add(1)
		return nil, errors.New("ambient DNS forbidden")
	}}
	http.DefaultTransport = &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		ambient.Add(1)
		return nil, errors.New("ambient transport forbidden")
	}}
	t.Cleanup(func() { net.DefaultResolver = oldResolver; http.DefaultTransport = oldTransport })
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	body, err := fixture.fetch(context.Background(), "/start")
	if err != nil || string(body) != "SYNTHETIC_OPERATION_BODY" {
		t.Fatal("positive operation path failed", err)
	}
	entitlement, err := fetchEntitlementWithConfig(context.Background(), fixture.origin, strings.Repeat("a", 32), entitlementConfig{network: fixture.lease, rootCAs: fixture.roots, blockAddr: blockedByDefaultIP, limits: DefaultLimits()})
	if err != nil || entitlement.NormalizedTier() != TierPremium {
		t.Fatal("same-authority entitlement failed", err)
	}
	if fixture.dohDials.Load() == 0 || fixture.originDials.Load() < 3 || bodyHits.Load() != 1 || entitlementHits.Load() != 1 || ambient.Load() != 0 || fixture.badDeadline.Load() != 0 {
		t.Fatal("operation authority bypass or positive control absent")
	}
}

func TestOperationNetworkRevokeDuringBodyPreservesCause(t *testing.T) {
	for _, kind := range []string{"subscription", "entitlement"} {
		t.Run(kind, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			fixture := operationNetworkFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(200)
				io.WriteString(w, "partial")
				w.(http.Flusher).Flush()
				entered <- struct{}{}
				<-r.Context().Done()
			}))
			finished := make(chan error, 1)
			go func() {
				if kind == "subscription" {
					body, err := fixture.fetch(context.Background(), "/body")
					if body != nil {
						finished <- errors.New("cancelled body was accepted")
						return
					}
					finished <- err
				} else {
					_, err := fetchEntitlementWithConfig(context.Background(), fixture.origin, strings.Repeat("a", 32), entitlementConfig{network: fixture.lease, rootCAs: fixture.roots, blockAddr: blockedByDefaultIP, limits: DefaultLimits()})
					finished <- err
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("positive body phase was not reached")
			}
			fixture.lease.Revoke(errors.New("synthetic operation cancelled"))
			select {
			case err := <-finished:
				if !errors.Is(err, maintenance.ErrRevoked) {
					t.Fatalf("body cancellation lost revocation identity: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled raw socket did not interrupt body")
			}
		})
	}
}
