package subscription

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type syntheticNetwork struct {
	lookup func(context.Context, string) ([]net.IP, error)
	dial   dialFunc
}

func (n syntheticNetwork) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return n.lookup(ctx, host)
}
func (n syntheticNetwork) DialContext(ctx context.Context, kind, address string) (net.Conn, error) {
	return n.dial(ctx, kind, address)
}

func TestInjectedNetworkNumericPinIgnoresAmbientProxy(t *testing.T) {
	var proxyHits, lookupCalls, dialCalls atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyHits.Add(1) }))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "" {
			t.Error("initial request leaked Referer")
		}
		io.WriteString(w, "authorized-network")
	}))
	defer server.Close()
	port := serverPort(t, server)
	network := syntheticNetwork{
		lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			lookupCalls.Add(1)
			if host != "example.com" {
				t.Error("unexpected lookup")
			}
			return []net.IP{net.ParseIP("203.0.113.1")}, nil
		},
		dial: func(ctx context.Context, kind, address string) (net.Conn, error) {
			dialCalls.Add(1)
			if address != net.JoinHostPort("203.0.113.1", port) {
				t.Error("authority received nonnumeric/unpinned target")
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, kind, server.Listener.Addr().String())
		},
	}
	body, _, err := fetchWithConfig(context.Background(), "https://example.com:"+port+"/private", fetchConfig{network: network, rootCAs: subPool(t, server), disableFallback: true, blockAddr: blockedByDefaultIP, headers: map[string]string{"Referer": "https://provider.test/private"}})
	if err != nil || string(body) != "authorized-network" || lookupCalls.Load() != 1 || dialCalls.Load() != 1 || proxyHits.Load() != 0 {
		t.Fatalf("injected authority bypassed: lookup=%d dial=%d proxy=%d error=%v", lookupCalls.Load(), dialCalls.Load(), proxyHits.Load(), err)
	}
	if _, err := newUnprotectedNetwork(); err == nil {
		t.Fatal("unprotected wrapper silently bypassed explicit proxy configuration")
	} else {
		var configuration *ConfigurationError
		if !errors.As(err, &configuration) {
			t.Fatal("proxy configuration failure lost its type")
		}
	}
}

func TestNetworkRequiredAndNoPhysicalFallback(t *testing.T) {
	if _, _, err := FetchWithOptions(context.Background(), "https://example.test/private", FetchOptions{Limits: DefaultLimits()}); !errors.Is(err, ErrNetworkRequired) {
		t.Fatal("missing authority selected a default network")
	}
	failure := errors.New("SYNTHETIC_CANARY https://u:p@other.test/private?token=CANARY")
	var lookups, dials atomic.Int64
	network := syntheticNetwork{lookup: func(context.Context, string) ([]net.IP, error) { lookups.Add(1); return nil, failure }, dial: func(context.Context, string, string) (net.Conn, error) { dials.Add(1); return nil, failure }}
	_, _, err := FetchWithOptions(context.Background(), "https://example.test/private", FetchOptions{Network: network, Limits: DefaultLimits()})
	if err == nil || !errors.Is(err, failure) || lookups.Load() != 1 || dials.Load() != 0 || strings.Contains(err.Error(), "CANARY") {
		t.Fatalf("failed authority retried or exposed raw error: lookup=%d dial=%d safe=%v", lookups.Load(), dials.Load(), err != nil && !strings.Contains(err.Error(), "CANARY"))
	}
	if _, err := FetchEntitlementWithOptions(context.Background(), "https://example.test", "INERT", FetchOptions{Limits: DefaultLimits()}); !errors.Is(err, ErrNetworkRequired) {
		t.Fatal("entitlement defaulted to a physical network")
	}
}

func TestInjectedNetworkStillValidatesTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS origin reached HTTP handler") }))
	defer server.Close()
	network := syntheticNetwork{lookup: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.1")}, nil }, dial: func(ctx context.Context, kind, address string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, kind, server.Listener.Addr().String())
	}}
	if _, _, err := FetchWithOptions(context.Background(), "https://example.com:"+serverPort(t, server), FetchOptions{Network: network, Limits: DefaultLimits()}); err == nil {
		t.Fatal("injected network disabled origin certificate verification")
	}
}

func TestDoHRequiresNumericBootstrapAndCloseCancels(t *testing.T) {
	if _, err := NewDoHResolver(DefaultDoHEndpoints, nil); !errors.Is(err, ErrNetworkRequired) {
		t.Fatal("nil DoH dialer selected OS network")
	}
	dial := func(context.Context, string, string) (net.Conn, error) {
		t.Error("invalid bootstrap reached network")
		return nil, errors.New("inert")
	}
	for _, endpoint := range []DoHEndpoint{{URL: "http://resolver.test/query", Addr: "1.1.1.1:443"}, {URL: "https://resolver.test/query", Addr: "resolver.test:443"}} {
		if resolver, err := NewDoHResolver([]DoHEndpoint{endpoint}, dial); err == nil {
			resolver.Close()
			t.Fatal("unsafe bootstrap accepted")
		}
	}
	resolver, err := NewDoHResolver(DefaultDoHEndpoints, dial)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.LookupIP(context.Background(), "1.1.1.1"); err == nil {
		t.Fatal("closed resolver still admitted lookup")
	}
}

func TestInjectedAuthorityDoesNotInheritAmbientTLSDialer(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	var ambient, injected atomic.Int64
	http.DefaultTransport = &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
		ambient.Add(1)
		return nil, errors.New("inert ambient dialer")
	}}
	network := syntheticNetwork{lookup: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.1")}, nil }, dial: func(context.Context, string, string) (net.Conn, error) {
		injected.Add(1)
		return nil, errors.New("inert injected dialer")
	}}
	FetchWithOptions(context.Background(), "https://example.test/inert", FetchOptions{Network: network, Limits: DefaultLimits()})
	if ambient.Load() != 0 || injected.Load() != 1 {
		t.Fatalf("ambient TLS dialer bypassed injected authority: ambient=%d injected=%d", ambient.Load(), injected.Load())
	}
}

func TestOperationCancellationClosesLiveAndLateConnections(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "late"}[late], func(t *testing.T) {
			operation, cancel := context.WithCancel(context.Background())
			defer cancel()
			left, right := net.Pipe()
			defer right.Close()
			dial := bindOperationDial(operation, func(context.Context, string, string) (net.Conn, error) {
				if late {
					cancel()
				}
				return left, nil
			})
			connection, err := dial(context.Background(), "tcp", "203.0.113.1:443")
			if late {
				if !errors.Is(err, context.Canceled) || connection != nil {
					t.Fatal("late socket admitted after cancellation")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				defer connection.Close()
			}
			right.SetReadDeadline(time.Now().Add(time.Second))
			buffer := make([]byte, 1)
			count, err := right.Read(buffer)
			if count != 0 || err != io.EOF {
				t.Fatalf("cancelled socket stayed open or emitted bytes: count=%d error=%v", count, err)
			}
		})
	}
}
