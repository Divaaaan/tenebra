package subscription

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Divaaaan/tenebra/core/dnswire"
)

// DoH (DNS-over-HTTPS, RFC 8484) is used only as a fallback for resolving a
// subscription host when the system resolver returns a poisoned or blocked
// answer — the failure mode of a network that tampers with DNS for specific
// domains. The DoH request itself is dialed to the resolver's literal IP over
// HTTPS, so it never depends on the system resolver: that independence is the
// whole point of the fallback.

const (
	// dohTimeout bounds a single DoH request so a dead resolver cannot stall an
	// import. The wire responses involved are tiny, so this is generous.
	dohTimeout = 5 * time.Second
	// maxDoHResponse caps a DoH response body. A DNS message is at most 64 KiB
	// and an A/AAAA answer is a few hundred bytes; this bounds memory against a
	// misbehaving resolver.
	maxDoHResponse = 64 << 10
	// dohContentType is the RFC 8484 wire-format media type.
	dohContentType = "application/dns-message"
)

// DoHEndpoint is a single DNS-over-HTTPS resolver.
type DoHEndpoint struct {
	// URL is the RFC 8484 query endpoint. Its host is used for the TLS SNI and
	// Host header so the resolver certificate verifies by name.
	URL string
	// Addr is the "ip:port" the request is dialed to directly. Pinning the dial
	// to a literal IP is what lets the query bypass the system resolver.
	Addr string
}

// DefaultDoHEndpoints are the public resolvers used when no override is given.
// They are tried in order. Callers may replace this list (for example at init)
// to prefer a different public resolver.
var DefaultDoHEndpoints = []DoHEndpoint{
	{URL: "https://cloudflare-dns.com/dns-query", Addr: "1.1.1.1:443"},
	{URL: "https://dns.google/dns-query", Addr: "8.8.8.8:443"},
}

// dohResolver resolves a host through one or more DoH endpoints.
type dohResolver struct {
	endpoints []dohEndpointClient
}

// dohEndpointClient pairs an endpoint with an HTTP client whose dial is pinned
// to the endpoint's resolver IP.
type dohEndpointClient struct {
	ep     DoHEndpoint
	client *http.Client
}

// newDoHResolver builds a resolver over the given endpoints. rootCAs, when
// non-nil, overrides the system trust store for the resolver connections; it is
// nil in production and set by tests to trust a local resolver.
func newDoHResolver(endpoints []DoHEndpoint, rootCAs *x509.CertPool) *dohResolver {
	r := &dohResolver{}
	for _, ep := range endpoints {
		r.endpoints = append(r.endpoints, dohEndpointClient{
			ep:     ep,
			client: newDoHClient(ep.Addr, rootCAs),
		})
	}
	return r
}

// newDoHClient returns an HTTP client that always dials addr, regardless of the
// host in the request URL. TLS still uses the URL host for SNI and certificate
// verification, so the request is authenticated to the named resolver while the
// TCP connection targets a fixed IP the system resolver never sees.
func newDoHClient(addr string, rootCAs *x509.CertPool) *http.Client {
	dialer := &net.Dialer{Timeout: dohTimeout}
	return &http.Client{
		Timeout: dohTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			TLSClientConfig:     &tls.Config{RootCAs: rootCAs},
			TLSHandshakeTimeout: dohTimeout,
			ForceAttemptHTTP2:   true,
		},
	}
}

// resolve returns the addresses for host, trying each endpoint in order until
// one answers. A host that is already an IP literal is returned unchanged.
func (r *dohResolver) resolve(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if len(r.endpoints) == 0 {
		return nil, errors.New("subscription: doh: no resolvers configured")
	}
	var errs []error
	for _, ec := range r.endpoints {
		ips, err := r.queryEndpoint(ctx, ec, host)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(ips) > 0 {
			return ips, nil
		}
	}
	return nil, fmt.Errorf("subscription: doh: could not resolve %q via any resolver: %w", host, errors.Join(errs...))
}

// queryEndpoint asks one endpoint for A and then AAAA records, returning the
// union. IPv4 is placed first so the dialer prefers it. A per-type error is
// tolerated as long as the other type yields an address.
func (r *dohResolver) queryEndpoint(ctx context.Context, ec dohEndpointClient, host string) ([]net.IP, error) {
	var ips []net.IP
	var firstErr error
	for _, qtype := range []uint16{dnswire.TypeA, dnswire.TypeAAAA} {
		got, err := r.queryOne(ctx, ec, host, qtype)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ips = append(ips, got...)
	}
	if len(ips) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return ips, nil
}

// queryOne performs a single RFC 8484 GET query for one record type.
func (r *dohResolver) queryOne(ctx context.Context, ec dohEndpointClient, host string, qtype uint16) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(ctx, dohTimeout)
	defer cancel()

	query, err := dnswire.EncodeQuery(host, qtype)
	if err != nil {
		return nil, err
	}
	reqURL := ec.ep.URL + "?dns=" + base64.RawURLEncoding.EncodeToString(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("subscription: doh: build request: %w", err)
	}
	req.Header.Set("Accept", dohContentType)
	req.Header.Set("User-Agent", userAgent)

	resp, err := ec.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subscription: doh: query %s: %w", dohHost(ec.ep.URL), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription: doh: %s returned status %s", dohHost(ec.ep.URL), resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, dohContentType) {
		return nil, fmt.Errorf("subscription: doh: %s returned content-type %q", dohHost(ec.ep.URL), ct)
	}

	wire, err := io.ReadAll(io.LimitReader(resp.Body, maxDoHResponse))
	if err != nil {
		return nil, fmt.Errorf("subscription: doh: read response from %s: %w", dohHost(ec.ep.URL), err)
	}
	return dnswire.ParseResponse(wire, host, qtype)
}

// dialContext resolves the address's host through DoH and dials the resulting
// IP(s) in order. It is installed as an http.Transport.DialContext for the
// fallback fetch: only the TCP target changes, so the transport still derives
// the TLS SNI and Host header from the original URL.
func (r *dohResolver) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("subscription: doh dial: %w", err)
	}
	ips, err := r.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	var errs []error
	for _, ip := range ips {
		conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if derr == nil {
			return conn, nil
		}
		errs = append(errs, derr)
	}
	return nil, fmt.Errorf("subscription: doh dial: no reachable address for %q: %w", host, errors.Join(errs...))
}

// dohHost returns the host of a DoH URL for error messages.
func dohHost(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}
