package subscription

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// userAgent is a browser-like UA; some subscription panels gate responses on it.
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// maxBody caps a subscription response at 8 MiB to bound memory use.
const maxBody = 8 << 20

// fetchTimeout bounds a single subscription request.
const fetchTimeout = 30 * time.Second

// dialFunc matches net.Dialer.DialContext and http.Transport.DialContext.
type dialFunc = func(ctx context.Context, network, address string) (net.Conn, error)

// errTransport marks a fetch that failed at the transport layer — DNS
// resolution, dialing or TLS. This is the only class of failure the DoH fallback
// can address, so it is the trigger for a fallback attempt. Failures that carry
// a server response (any HTTP status) or a malformed request do not wrap it and
// are never retried through DoH.
var errTransport = errors.New("connection failed")

// fetchConfig carries the knobs the internal fetch honours. Production uses
// defaultFetchConfig; tests override the fields to drive the DoH fallback
// against local servers without touching real DNS.
type fetchConfig struct {
	// dohEndpoints are the resolvers the fallback tries, in order.
	dohEndpoints []DoHEndpoint
	// rootCAs overrides the system trust store for both the subscription and the
	// DoH connections when non-nil. Production leaves it nil.
	rootCAs *x509.CertPool
	// primaryDial overrides the dialer for the first (system-resolver) attempt.
	// Production leaves it nil so the OS resolver is used.
	primaryDial dialFunc
}

func defaultFetchConfig() fetchConfig {
	return fetchConfig{dohEndpoints: DefaultDoHEndpoints}
}

// Fetch retrieves a subscription body over HTTP(S). It first tries the system
// resolver, with the single client-initiated TLS renegotiation some
// Cloudflare-fronted panels require. If that attempt fails at the transport
// layer — the fingerprint of DNS poisoning or a blocked/wrong IP — it retries
// once, resolving the host through public DNS-over-HTTPS resolvers while keeping
// the original SNI and Host so TLS (and REALITY) still target the real origin.
//
// It is intentionally the only network-touching function in the package;
// parsing is done separately so it can be tested offline.
func Fetch(ctx context.Context, url string) (body []byte, header http.Header, err error) {
	return fetchWithConfig(ctx, url, defaultFetchConfig())
}

func fetchWithConfig(ctx context.Context, rawURL string, cfg fetchConfig) ([]byte, http.Header, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	host := hostForLog(rawURL)

	body, header, err := doFetch(ctx, rawURL, cfg.primaryDial, cfg.rootCAs)
	if err == nil {
		return body, header, nil
	}
	if !dohEligible(ctx, err) {
		log.Printf("tenebra-core: subscription fetch failed for %s: %v", host, err)
		return nil, header, err
	}

	// The system-resolver attempt failed the way a poisoned or blocked resolver
	// would. Retry once through DoH, dialing the resolved IP while preserving the
	// original SNI/Host.
	log.Printf("tenebra-core: subscription fetch via system resolver failed for %s: %v; trying DoH fallback", host, err)
	resolver := newDoHResolver(cfg.dohEndpoints, cfg.rootCAs)
	dohBody, dohHeader, dohErr := doFetch(ctx, rawURL, resolver.dialContext, cfg.rootCAs)
	if dohErr != nil {
		log.Printf("tenebra-core: subscription DoH fallback failed for %s: %v", host, dohErr)
		return nil, header, fmt.Errorf("subscription: fetch: system resolver and DoH fallback failed for %s [%v]: %w", host, err, dohErr)
	}
	log.Printf("tenebra-core: subscription fetch for %s recovered via DoH fallback", host)
	return dohBody, dohHeader, nil
}

// dohEligible reports whether a failed first attempt is worth retrying through
// DoH: only transport-level failures are, and only while the caller's context is
// still live — a cancelled or timed-out context will not be helped by a retry.
func dohEligible(ctx context.Context, err error) bool {
	return ctx.Err() == nil && errors.Is(err, errTransport)
}

// doFetch performs a single subscription GET. When dial is non-nil it replaces
// the transport's dialer (the DoH fallback uses this to dial a pre-resolved IP).
// TLSClientConfig.ServerName is deliberately left unset so the transport still
// derives the SNI and Host header from rawURL: dialing an IP while keeping the
// original SNI is what keeps TLS/REALITY working through the fallback.
func doFetch(ctx context.Context, rawURL string, dial dialFunc, rootCAs *x509.CertPool) (body []byte, header http.Header, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("subscription: fetch: %s", scrub(err, rawURL))
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	// Cloudflare and some subscription panels ask for a TLS renegotiation
	// mid-handshake. Go's default is RenegotiateNever, which fails the request
	// with "tls: no renegotiation" — so a URL that loads fine in curl or a
	// browser fails here. Clone the default transport (keeping its proxy, HTTP/2
	// and dial defaults) and permit a single client-initiated renegotiation.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.Renegotiation = tls.RenegotiateOnceAsClient
	if rootCAs != nil {
		transport.TLSClientConfig.RootCAs = rootCAs
	}
	if dial != nil {
		transport.DialContext = dial
	}

	client := &http.Client{Timeout: fetchTimeout, Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		// Transport-level failure (DNS, dial, TLS): the one class DoH can fix.
		return nil, nil, fmt.Errorf("subscription: fetch: %w: %s", errTransport, scrub(err, rawURL))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Header, fmt.Errorf("subscription: fetch: unexpected status %s", resp.Status)
	}

	body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, resp.Header, fmt.Errorf("subscription: fetch: read body: %w", err)
	}
	return body, resp.Header, nil
}

// hostForLog extracts the host from a subscription URL so a failed import leaves
// a trail without ever writing the token-bearing path to the log.
func hostForLog(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "subscription host"
}

// scrub returns err's message with the full subscription URL replaced by its
// host, so neither logs nor surfaced errors echo the token-bearing path. Go's
// url.Error embeds the whole request URL in its message, which is why this is
// needed on transport errors.
func scrub(err error, rawURL string) string {
	msg := err.Error()
	if rawURL != "" {
		msg = strings.ReplaceAll(msg, rawURL, hostForLog(rawURL))
	}
	return msg
}
