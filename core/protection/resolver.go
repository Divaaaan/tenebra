package protection

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// NewResolver creates no connections and changes no globals. The production
// entry point may install it before starting goroutines. While protected, even
// the resolver's retries use the same explicit encrypted endpoint: no OS DNS,
// proxy environment, redirected endpoint or plaintext fallback is consulted.
func NewResolver(source func() (endpoint string, required bool)) *net.Resolver {
	client := &http.Client{
		Timeout:       4 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("DNS bootstrap redirects are forbidden") },
		Transport:     &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second, MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, ForceAttemptHTTP2: true},
	}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		endpoint, required := source()
		if !required {
			return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, network, address)
		}
		if err := ValidateDNS(endpoint); err != nil {
			return nil, err
		}
		u, _ := url.Parse(endpoint)
		if u.Scheme == "tls" {
			port := u.Port()
			if port == "" {
				port = "853"
			}
			d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 4 * time.Second}, Config: &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}}
			return d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
		}
		if u.Path == "" {
			u.Path = "/dns-query"
		}
		return newDNSConn(ctx, u.String(), client), nil
	}}
}

// dnsConn adapts the Go resolver's TCP DNS framing to a single bounded RFC8484
// POST. It is intentionally not a PacketConn, even when Resolver.Dial asks for
// "udp": net.Resolver then uses the stream framing specified by its contract.
type dnsConn struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	endpoint string
	client   *http.Client
	deadline time.Time
	closed   bool
	pending  []byte
	response *bytes.Reader
}

func newDNSConn(ctx context.Context, endpoint string, client *http.Client) *dnsConn {
	ctx, cancel := context.WithCancel(ctx)
	return &dnsConn{ctx: ctx, cancel: cancel, endpoint: endpoint, client: client}
}
func (c *dnsConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	if len(c.pending)+len(p) > 65537 {
		c.mu.Unlock()
		return 0, errors.New("DNS query too large")
	}
	c.pending = append(c.pending, p...)
	if len(c.pending) < 2 {
		c.mu.Unlock()
		return len(p), nil
	}
	n := int(binary.BigEndian.Uint16(c.pending))
	if n < 12 || len(c.pending) > n+2 {
		c.mu.Unlock()
		return 0, errors.New("invalid DNS stream frame")
	}
	if len(c.pending) < n+2 {
		c.mu.Unlock()
		return len(p), nil
	}
	query := append([]byte(nil), c.pending[2:]...)
	c.pending = nil
	deadline := c.deadline
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 4*time.Second)
	defer cancel()
	if !deadline.IsZero() {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, deadline)
		defer stop()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(query))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("DNS bootstrap HTTP status %d", resp.StatusCode)
	}
	if strings.Split(resp.Header.Get("Content-Type"), ";")[0] != "application/dns-message" {
		return 0, errors.New("DNS bootstrap returned an invalid media type")
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return 0, err
	}
	if len(answer) < 12 || len(answer) > 65535 {
		return 0, errors.New("DNS bootstrap returned an invalid length")
	}
	framed := binary.BigEndian.AppendUint16(nil, uint16(len(answer)))
	framed = append(framed, answer...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	c.response = bytes.NewReader(framed)
	return len(p), nil
}
func (c *dnsConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	if c.response == nil {
		return 0, errors.New("DNS query has not completed")
	}
	return c.response.Read(p)
}
func (c *dnsConn) Close() error { c.mu.Lock(); c.closed = true; c.mu.Unlock(); c.cancel(); return nil }
func (c *dnsConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}
func (c *dnsConn) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *dnsConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }
func (c *dnsConn) LocalAddr() net.Addr                { return dnsAddr("core") }
func (c *dnsConn) RemoteAddr() net.Addr               { return dnsAddr("encrypted-resolver") }

type dnsAddr string

func (a dnsAddr) Network() string { return "tcp" }
func (a dnsAddr) String() string  { return string(a) }
