package subscription

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
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

// DNS record and class type numbers (RFC 1035, RFC 3596) used by the minimal
// wire codec below.
const (
	dnsTypeA     uint16 = 1
	dnsTypeCNAME uint16 = 5
	dnsTypeAAAA  uint16 = 28
	dnsClassIN   uint16 = 1
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
	for _, qtype := range []uint16{dnsTypeA, dnsTypeAAAA} {
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

	query, err := encodeDNSQuery(host, qtype)
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
	return parseDNSResponse(wire, host, qtype)
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

// encodeDNSQuery builds the wire form of a single recursion-desired question for
// host of the given record type. The ID is 0, which RFC 8484 recommends for GET
// cacheability; over an authenticated TLS channel it need not be random.
func encodeDNSQuery(host string, qtype uint16) ([]byte, error) {
	name, err := encodeName(host)
	if err != nil {
		return nil, err
	}
	msg := make([]byte, 0, 12+len(name)+4)
	msg = append(msg,
		0x00, 0x00, // ID
		0x01, 0x00, // flags: recursion desired
		0x00, 0x01, // QDCOUNT
		0x00, 0x00, // ANCOUNT
		0x00, 0x00, // NSCOUNT
		0x00, 0x00, // ARCOUNT
	)
	msg = append(msg, name...)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	msg = binary.BigEndian.AppendUint16(msg, dnsClassIN)
	return msg, nil
}

// encodeName encodes a host as a length-prefixed, root-terminated DNS name.
func encodeName(host string) ([]byte, error) {
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return nil, errors.New("subscription: doh: empty host")
	}
	var out []byte
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, fmt.Errorf("subscription: doh: invalid label in %q", host)
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	out = append(out, 0x00)
	if len(out) > 255 {
		return nil, fmt.Errorf("subscription: doh: name too long: %q", host)
	}
	return out, nil
}

// parseDNSResponse validates a wire response and returns the addresses bound to
// host. It follows the CNAME chain from the queried name (common for CDN-fronted
// subscriptions) and only accepts records linked to it, so an answer for an
// unrelated name yields no addresses.
func parseDNSResponse(msg []byte, host string, qtype uint16) ([]net.IP, error) {
	if len(msg) < 12 {
		return nil, errors.New("subscription: doh: short response")
	}
	flags := binary.BigEndian.Uint16(msg[2:4])
	if flags&0x8000 == 0 {
		return nil, errors.New("subscription: doh: reply is not a response")
	}
	if rcode := flags & 0x000F; rcode != 0 {
		return nil, fmt.Errorf("subscription: doh: resolver returned rcode %d for %q", rcode, host)
	}
	qdcount := int(binary.BigEndian.Uint16(msg[4:6]))
	ancount := int(binary.BigEndian.Uint16(msg[6:8]))

	off := 12
	for i := 0; i < qdcount; i++ {
		_, next, err := readName(msg, off)
		if err != nil {
			return nil, err
		}
		off = next + 4 // QTYPE + QCLASS
		if off > len(msg) {
			return nil, errors.New("subscription: doh: truncated question")
		}
	}

	cnames := make(map[string]string)
	ipsByName := make(map[string][]net.IP)
	for i := 0; i < ancount; i++ {
		owner, next, err := readName(msg, off)
		if err != nil {
			return nil, err
		}
		off = next
		if off+10 > len(msg) {
			return nil, errors.New("subscription: doh: truncated record header")
		}
		rtype := binary.BigEndian.Uint16(msg[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
		off += 10
		if off+rdlen > len(msg) {
			return nil, errors.New("subscription: doh: truncated record data")
		}
		switch rtype {
		case dnsTypeA:
			if rdlen == net.IPv4len {
				ip := make(net.IP, net.IPv4len)
				copy(ip, msg[off:off+rdlen])
				ipsByName[owner] = append(ipsByName[owner], ip)
			}
		case dnsTypeAAAA:
			if rdlen == net.IPv6len {
				ip := make(net.IP, net.IPv6len)
				copy(ip, msg[off:off+rdlen])
				ipsByName[owner] = append(ipsByName[owner], ip)
			}
		case dnsTypeCNAME:
			target, _, err := readName(msg, off)
			if err != nil {
				return nil, err
			}
			cnames[owner] = target
		}
		off += rdlen
	}

	ips := followChain(canonicalHost(host), cnames, ipsByName)
	if len(ips) == 0 {
		return nil, fmt.Errorf("subscription: doh: no %s record for %q", typeName(qtype), host)
	}
	return ips, nil
}

// followChain walks the CNAME chain from start and gathers every address bound
// to a name along it. A visited set and hop cap guard against a malicious or
// broken chain that loops.
func followChain(start string, cnames map[string]string, ipsByName map[string][]net.IP) []net.IP {
	const maxHops = 32
	var ips []net.IP
	seen := make(map[string]bool)
	cur := start
	for i := 0; i < maxHops; i++ {
		if seen[cur] {
			break
		}
		seen[cur] = true
		ips = append(ips, ipsByName[cur]...)
		next, ok := cnames[cur]
		if !ok {
			break
		}
		cur = next
	}
	return ips
}

// readName reads a DNS name (following compression pointers) starting at off.
// It returns the lower-cased name without a trailing dot and the offset of the
// first byte after the name in the original stream (i.e. after the first
// pointer, not the pointer target).
func readName(msg []byte, off int) (name string, next int, err error) {
	const maxPointers = 32
	var sb strings.Builder
	next = -1
	pointers := 0
	for {
		if off < 0 || off >= len(msg) {
			return "", 0, errors.New("subscription: doh: name offset out of range")
		}
		b := msg[off]
		switch b & 0xC0 {
		case 0x00:
			if b == 0x00 {
				off++
				if next < 0 {
					next = off
				}
				return canonicalHost(sb.String()), next, nil
			}
			length := int(b)
			off++
			if off+length > len(msg) {
				return "", 0, errors.New("subscription: doh: truncated label")
			}
			sb.Write(msg[off : off+length])
			sb.WriteByte('.')
			off += length
		case 0xC0:
			if off+1 >= len(msg) {
				return "", 0, errors.New("subscription: doh: truncated compression pointer")
			}
			ptr := int(b&0x3F)<<8 | int(msg[off+1])
			if next < 0 {
				next = off + 2
			}
			if ptr >= off {
				// Pointers must reference earlier bytes; anything else risks a
				// loop or reads uninitialised data.
				return "", 0, errors.New("subscription: doh: non-backward compression pointer")
			}
			pointers++
			if pointers > maxPointers {
				return "", 0, errors.New("subscription: doh: too many compression pointers")
			}
			off = ptr
		default:
			return "", 0, errors.New("subscription: doh: reserved label type")
		}
	}
}

// canonicalHost lower-cases a host and strips a trailing dot so names compare
// consistently regardless of case or root form.
func canonicalHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// typeName names a record type for error messages.
func typeName(qtype uint16) string {
	switch qtype {
	case dnsTypeA:
		return "A"
	case dnsTypeAAAA:
		return "AAAA"
	default:
		return fmt.Sprintf("type-%d", qtype)
	}
}
