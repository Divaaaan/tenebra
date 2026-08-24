package dnswire

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxResponse caps a response body. A DNS message is at most 64 KiB and an
// A/AAAA answer is a few hundred bytes; this bounds memory against a
// misbehaving resolver.
const maxResponse = 64 << 10

// contentType is the RFC 8484 wire-format media type.
const contentType = "application/dns-message"

// Default ports per transport, matching the schemes sing-box accepts in a
// resolver address.
const (
	portPlain = "53"
	portDoT   = "853"
	portDoH   = "443"
)

// Resolver asks one named resolver, over the transport its address names.
//
// The address form is sing-box's: an optional scheme, a host, an optional port,
// and for DoH a path — "https://77.88.8.8/dns-query", "tls://dns.quad9.net",
// "udp://8.8.8.8", or a bare "8.8.8.8", which means plain DNS like it does
// there. That is deliberate: the point of this type is to ask the same resolver
// sing-box was configured with, so it reads the same string sing-box is given.
type Resolver struct {
	// query sends one encoded question and returns the raw response.
	query func(ctx context.Context, msg []byte) ([]byte, error)
	// name is the resolver as configured, for error messages.
	name string
}

// NewResolver builds a resolver for a sing-box resolver address. The second
// result is false when the address names a transport this cannot speak, so a
// caller can fall back rather than pretend.
//
// The gap is DNS-over-QUIC ("quic://"): it needs a QUIC stack, which this client
// does not link, and inventing a substitute — DoT on the same host, or plain 53
// — would query something the user did not configure and may hold a different
// answer. h3:// is served over HTTP/2 instead: same resolver, same URL, same
// answer, one transport version down.
func NewResolver(addr string) (*Resolver, bool) {
	return newResolver(addr, dohClient)
}

// newResolver is NewResolver with the HTTP client for DoH handed in, so a test
// can point one at a resolver it started itself.
func newResolver(addr string, client *http.Client) (*Resolver, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, false
	}
	scheme, rest := "udp", addr
	if i := strings.Index(addr, "://"); i >= 0 {
		scheme, rest = strings.ToLower(addr[:i]), addr[i+3:]
	}
	host, path := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		host, path = rest[:i], rest[i:]
	}
	if host == "" {
		return nil, false
	}

	switch scheme {
	case "https", "h3":
		if path == "" {
			path = "/dns-query"
		}
		return &Resolver{name: addr, query: dohQuery(client, "https://"+withPort(host, portDoH)+path)}, true
	case "tls":
		return &Resolver{name: addr, query: streamQuery(withPort(host, portDoT), unbracket(host))}, true
	case "tcp":
		return &Resolver{name: addr, query: streamQuery(withPort(host, portPlain), "")}, true
	case "udp":
		return &Resolver{name: addr, query: udpQuery(withPort(host, portPlain))}, true
	default:
		return nil, false
	}
}

// LookupIP resolves host through this resolver, asking for A and AAAA at once
// and returning the union. An IP literal is returned as itself, so a caller can
// hand over whatever it has without checking first.
//
// One record type answering is enough: a name with only A records is ordinary,
// and failing the whole lookup because AAAA came back empty would throw away the
// answer we did get.
func (r *Resolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return []net.IP{ip}, nil
	}

	var (
		mu   sync.Mutex
		ips  []net.IP
		errs []error
		wg   sync.WaitGroup
	)
	for _, qtype := range []uint16{TypeA, TypeAAAA} {
		wg.Add(1)
		go func(qtype uint16) {
			defer wg.Done()
			got, err := r.lookupType(ctx, host, qtype)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ips = append(ips, got...)
		}(qtype)
	}
	wg.Wait()

	if len(ips) == 0 {
		return nil, fmt.Errorf("dns: %s: %w", r.name, errors.Join(errs...))
	}
	return ips, nil
}

// lookupType asks for one record type.
func (r *Resolver) lookupType(ctx context.Context, host string, qtype uint16) ([]net.IP, error) {
	msg, err := EncodeQuery(host, qtype)
	if err != nil {
		return nil, err
	}
	resp, err := r.query(ctx, msg)
	if err != nil {
		return nil, err
	}
	return ParseResponse(resp, host, qtype)
}

// dohQuery returns a query function that asks a DoH endpoint (RFC 8484). The URL
// is used as given: when it names the resolver by IP the request never touches
// the system resolver, and when it names it by hostname that one name is
// resolved the ordinary way — the same bootstrap sing-box does for a resolver
// addressed by name.
func dohQuery(client *http.Client, endpoint string) func(context.Context, []byte) ([]byte, error) {
	return func(ctx context.Context, msg []byte) ([]byte, error) {
		url := endpoint + "?dns=" + base64.RawURLEncoding.EncodeToString(msg)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("dns: build request: %w", err)
		}
		req.Header.Set("Accept", contentType)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("dns: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("dns: resolver returned status %s", resp.Status)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
		if err != nil {
			return nil, fmt.Errorf("dns: read response: %w", err)
		}
		return body, nil
	}
}

// dohClient is the HTTP client DoH queries go out on. The deadline is the
// caller's context; only the handshake gets a limit of its own, so a resolver
// that accepts a connection and then says nothing cannot outlive the budget by
// sitting in the TLS handshake.
//
// One client for the process, not one per resolver. NewResolver runs on every
// connect, every bypass start and every strategy pick, and a client built in
// there brought its own transport — and so its own connection pool — with it:
// the pool outlives the call that made it, holding an idle TLS connection and
// the goroutines reading it until the far end gives up. Sharing one means the
// second question to the same resolver reuses the first one's connection instead
// of opening another beside it.
var dohClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		// A connection to a resolver nobody is asking any more is closed rather
		// than held for the life of the process; a bare Transport keeps idle
		// connections forever.
		IdleConnTimeout: 90 * time.Second,
	},
}

// streamQuery returns a query function for DNS over a stream: TCP (RFC 1035
// section 4.2.2, a two-byte length in front of the message) and, when serverName
// is set, the same framing inside TLS — DoT.
func streamQuery(addr, serverName string) func(context.Context, []byte) ([]byte, error) {
	return func(ctx context.Context, msg []byte) ([]byte, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("dns: dial %s: %w", addr, err)
		}
		defer conn.Close()
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}
		if serverName != "" {
			// The name in the address is what the certificate is checked against:
			// an encrypted resolver whose identity is not verified is a plain
			// resolver with extra steps.
			tlsConn := tls.Client(conn, &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12})
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				return nil, fmt.Errorf("dns: tls to %s: %w", addr, err)
			}
			conn = tlsConn
		}

		framed := make([]byte, 0, len(msg)+2)
		framed = binary.BigEndian.AppendUint16(framed, uint16(len(msg)))
		framed = append(framed, msg...)
		if _, err := conn.Write(framed); err != nil {
			return nil, fmt.Errorf("dns: write to %s: %w", addr, err)
		}

		var length [2]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return nil, fmt.Errorf("dns: read from %s: %w", addr, err)
		}
		n := int(binary.BigEndian.Uint16(length[:]))
		if n == 0 || n > maxResponse {
			return nil, fmt.Errorf("dns: %s answered with a %d byte message", addr, n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return nil, fmt.Errorf("dns: read from %s: %w", addr, err)
		}
		return buf, nil
	}
}

// udpQuery returns a query function for plain DNS on port 53.
//
// Plain DNS is forgeable by anyone on the path, and this client offers it only
// as a last resort (see the dnspick package) — but if that is what the user is
// configured with, it is also what sing-box will be asking, and asking something
// else here would defeat the point of matching. Two things still cost nothing: a
// random query ID, so an off-path guess has to hit 1 in 65536, and dropping
// answers whose ID does not match.
func udpQuery(addr string) func(context.Context, []byte) ([]byte, error) {
	return func(ctx context.Context, msg []byte) ([]byte, error) {
		q := append([]byte(nil), msg...)
		var id [2]byte
		if _, err := rand.Read(id[:]); err == nil {
			copy(q[:2], id[:])
		}

		var d net.Dialer
		conn, err := d.DialContext(ctx, "udp", addr)
		if err != nil {
			return nil, fmt.Errorf("dns: dial %s: %w", addr, err)
		}
		defer conn.Close()
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}
		if _, err := conn.Write(q); err != nil {
			return nil, fmt.Errorf("dns: write to %s: %w", addr, err)
		}

		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return nil, fmt.Errorf("dns: read from %s: %w", addr, err)
			}
			if n < 12 || !bytes.Equal(buf[:2], q[:2]) {
				continue // not an answer to our question
			}
			answer := append([]byte(nil), buf[:n]...)
			if binary.BigEndian.Uint16(answer[2:4])&0x0200 == 0 {
				return answer, nil
			}
			// Truncated: the rest of the answer only exists over TCP. Same host,
			// same port number, stream framing.
			return streamQuery(addr, "")(ctx, msg)
		}
	}
}

// withPort appends the transport's default port when the address carries none.
// A bare IPv6 literal is bracketed on the way so it splits back correctly.
func withPort(host, port string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// unbracket strips the brackets from an IPv6 literal, which is how a TLS
// ServerName wants it.
func unbracket(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}
