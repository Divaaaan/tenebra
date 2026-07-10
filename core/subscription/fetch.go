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
	"os"
	"strconv"
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

// errBlockedAddress marks a dial the SSRF guard refused because the target
// resolved into loopback, link-local/cloud-metadata or private space. It is
// deliberately NOT wrapped in errTransport: a blocked address is a policy
// decision, not a resolver fault, so it must never trigger the DoH fallback
// (retrying would only dial the same forbidden IP through a different resolver).
var errBlockedAddress = errors.New("subscription: fetch: refused to connect to blocked address")

// ssrfGuardOptOutEnv names the environment variable an operator sets (to a
// truthy value: 1/true/yes) to disable the SSRF guard, permitting the fetch to
// reach loopback/link-local/private addresses. It exists for the legitimate case
// of a panel self-hosted on a LAN or behind a private-range reverse proxy; the
// default is guarded.
const ssrfGuardOptOutEnv = "TENEBRA_SUBSCRIPTION_ALLOW_PRIVATE"

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
	// blockAddr is the SSRF guard predicate. When non-nil, any address the
	// subscription transport dials — the first URL, every redirect hop and every
	// DoH-resolved IP — whose IP it reports true for is refused. Production sets
	// it to blockedByDefaultIP unless the opt-out env var is set; a nil predicate
	// disables the guard, which is how tests reach loopback httptest servers.
	blockAddr func(net.IP) bool
}

func defaultFetchConfig() fetchConfig {
	cfg := fetchConfig{dohEndpoints: DefaultDoHEndpoints}
	if !ssrfGuardDisabled() {
		cfg.blockAddr = blockedByDefaultIP
	}
	return cfg
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

	// Scheme gate. net/http will happily dial anything with a registered
	// transport (and a bare or non-http(s) URL can smuggle the fetch somewhere
	// unexpected), so reject up front anything that is not http(s). Cleartext
	// http is permitted — some self-hosted panels only speak it — but it exposes
	// the subscription token on the wire, so it earns a scrubbed warning (host
	// only, never the token-bearing path).
	if err := checkScheme(rawURL, host); err != nil {
		log.Printf("tenebra-core: subscription fetch rejected for %s: %v", host, err)
		return nil, nil, err
	}

	body, header, err := doFetch(ctx, rawURL, cfg.primaryDial, cfg.rootCAs, cfg.blockAddr)
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
	dohBody, dohHeader, dohErr := doFetch(ctx, rawURL, resolver.dialContext, cfg.rootCAs, cfg.blockAddr)
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
func doFetch(ctx context.Context, rawURL string, dial dialFunc, rootCAs *x509.CertPool, blockAddr func(net.IP) bool) (body []byte, header http.Header, err error) {
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
	// Route the actual dial through the SSRF guard. base is the cloned default
	// dialer (or the caller's override — the DoH fallback's IP-pinned dialer);
	// guardDial wraps whichever it is, so every hop of the request, including
	// redirects and DoH-resolved addresses, is screened against blockAddr. Note
	// this only touches the subscription transport — the DoH resolver keeps its
	// own unguarded client so it still reaches its literal public resolver IPs.
	base := transport.DialContext
	if dial != nil {
		base = dial
	}
	transport.DialContext = guardDial(base, blockAddr)

	client := &http.Client{Timeout: fetchTimeout, Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddress) {
			// The SSRF guard tripped. This is not a resolver fault, so it is
			// reported as-is (never wrapped in errTransport) and never retried
			// through DoH. scrub still runs in case the offending URL was the
			// token-bearing original rather than a redirect target.
			return nil, nil, fmt.Errorf("subscription: fetch: %s", scrub(err, rawURL))
		}
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

// checkScheme rejects any URL whose scheme is not http or https, and warns
// (host only) on cleartext http since the token travels unencrypted. host is the
// pre-scrubbed value so the warning never carries the token-bearing path.
func checkScheme(rawURL, host string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("subscription: fetch: invalid URL for %s: %s", host, scrub(err, rawURL))
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		log.Printf("tenebra-core: subscription fetch for %s uses cleartext http; the subscription token is exposed in transit", host)
		return nil
	default:
		return fmt.Errorf("subscription: fetch: unsupported URL scheme %q for %s (only http and https are allowed)", u.Scheme, host)
	}
}

// guardDial wraps a dial function with the SSRF guard. When block is nil the base
// dial is returned unchanged (guard disabled). Otherwise every dial is screened
// twice: an IP-literal target (an IP-literal URL, or a redirect Location with a
// bare IP such as http://169.254.169.254) is refused before any socket opens;
// and once connected, the peer's real address is re-checked so a hostname that
// resolved into a forbidden range (DNS rebinding, or a DoH-resolved private IP)
// is dropped before a single request byte is written. Because http.Client runs
// this dial for every redirect hop, an https origin that 302s into a private or
// metadata address is caught mid-chain, not just on the first URL.
func guardDial(base dialFunc, block func(net.IP) bool) dialFunc {
	if block == nil {
		return base
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if host, _, err := net.SplitHostPort(address); err == nil {
			if ip := net.ParseIP(host); ip != nil && block(ip) {
				return nil, fmt.Errorf("%w %s", errBlockedAddress, ip)
			}
		}
		conn, err := base(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if ip := connRemoteIP(conn); ip != nil && block(ip) {
			conn.Close()
			return nil, fmt.Errorf("%w %s", errBlockedAddress, ip)
		}
		return conn, nil
	}
}

// connRemoteIP extracts the peer IP of a freshly dialed connection so the guard
// can screen the address actually connected to (not just the requested host).
func connRemoteIP(conn net.Conn) net.IP {
	if conn == nil || conn.RemoteAddr() == nil {
		return nil
	}
	if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		return tcp.IP
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

// blockedByDefaultIP reports whether ip falls in a range the subscription fetch
// must never reach. These are the pivots an attacker targets via a crafted
// subscription URL or an origin redirect to reach the operator's own host or
// internal network (SSRF):
//
//   - loopback           127.0.0.0/8, ::1                 (IsLoopback)
//   - unspecified        0.0.0.0, ::                      (IsUnspecified; a
//     well-known localhost bypass)
//   - link-local unicast 169.254.0.0/16 (incl. the
//     169.254.169.254 cloud-metadata endpoint), fe80::/10 (IsLinkLocalUnicast)
//   - link-local multicast 224.0.0.0/24, ff02::/16        (IsLinkLocalMulticast)
//   - private            10/8, 172.16/12, 192.168/16,
//     fc00::/7 (ULA)                                      (IsPrivate)
func blockedByDefaultIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate()
}

// ssrfGuardDisabled reports whether the operator opted out of the SSRF guard via
// the environment. Any value strconv.ParseBool accepts as true (1/t/true/…)
// disables it; anything else (including unset) keeps the guard on.
func ssrfGuardDisabled() bool {
	v, err := strconv.ParseBool(os.Getenv(ssrfGuardOptOutEnv))
	return err == nil && v
}
