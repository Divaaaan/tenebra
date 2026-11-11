package subscription

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

type Network interface {
	LookupIP(context.Context, string) ([]net.IP, error)
	DialContext(context.Context, string, string) (net.Conn, error)
}

type FetchOptions struct {
	Headers   map[string]string
	AllowHTTP bool
	Network   Network
	Limits    Limits
}

type ConfigurationError struct{ Kind string }

func (e *ConfigurationError) Error() string { return "subscription network configuration: " + e.Kind }

// Keep cancellation/revocation/policy identity without rendering raw url.Error,
// redirect locations or attacker-controlled transport error strings.
type safeFetchError struct {
	message string
	causes  []error
}

func (e *safeFetchError) Error() string   { return e.message }
func (e *safeFetchError) Unwrap() []error { return e.causes }

// net/http may detach a transport dial from the request's cancellation to
// reuse its connection later. Each operation owns its transport, so bind
// pending dials back to the operation explicitly and reject late admission.
func bindOperationDial(operation context.Context, dial dialFunc) dialFunc {
	return func(transportContext context.Context, network, address string) (net.Conn, error) {
		if err := transportContext.Err(); err != nil {
			return nil, err
		}
		if err := operation.Err(); err != nil {
			return nil, err
		}
		// The transport owns a dial attempt, while the operation owns the live
		// stream. A lease may retain this context until the connection closes.
		// Detach the transport cancellation only after successful admission.
		base := context.WithoutCancel(transportContext)
		var ctx context.Context
		var cancel context.CancelFunc
		if deadline, ok := operation.Deadline(); ok {
			ctx, cancel = context.WithDeadline(base, deadline)
		} else {
			ctx, cancel = context.WithCancel(base)
		}
		stop := context.AfterFunc(operation, cancel)
		stopTransport := context.AfterFunc(transportContext, cancel)
		fail := func() { stopTransport(); stop(); cancel() }
		connection, err := dial(ctx, network, address)
		cancelled := operation.Err()
		if cancelled == nil {
			cancelled = ctx.Err()
		}
		if cancelled != nil {
			fail()
			if connection != nil {
				connection.Close()
			}
			return nil, cancelled
		}
		if err != nil {
			fail()
			if connection != nil {
				connection.Close()
			}
			return nil, err
		}
		if connection == nil {
			fail()
			return nil, errors.New("subscription network returned no connection")
		}
		if !stopTransport() {
			fail()
			connection.Close()
			return nil, context.Canceled
		}
		retained := &operationConnection{Conn: connection, cancel: cancel}
		retained.mu.Lock()
		closeOnCancel := context.AfterFunc(ctx, func() { retained.Close() })
		retained.stop = func() bool { stopped := closeOnCancel(); stop(); return stopped }
		retained.mu.Unlock()
		if err := operation.Err(); err != nil {
			retained.Close()
			return nil, err
		}
		return retained, nil
	}
}

type operationConnection struct {
	net.Conn
	mu         sync.Mutex
	stop       func() bool
	cancel     context.CancelFunc
	once       sync.Once
	closeError error
}

func (c *operationConnection) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		stop := c.stop
		c.mu.Unlock()
		if stop != nil {
			stop()
		}
		if c.cancel != nil {
			c.cancel()
		}
		c.closeError = c.Conn.Close()
	})
	return c.closeError
}

type dialOperationContextKey struct{}

func operationRequestContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, dialOperationContextKey{}, ctx)
}
func requestOperationDial(dial dialFunc) dialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		operation, ok := ctx.Value(dialOperationContextKey{}).(context.Context)
		if !ok || operation == nil {
			return nil, ErrNetworkRequired
		}
		return bindOperationDial(operation, dial)(ctx, network, address)
	}
}

// Captured once from trusted process startup configuration, never from a
// request parameter, direct-consent decision or failed protected transport.
var startupAllowPrivate = ssrfGuardDisabled()

type directNetwork struct{ dialer net.Dialer }

func (n *directNetwork) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	result := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, address.IP)
	}
	return result, nil
}
func (n *directNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return n.dialer.DialContext(ctx, network, address)
}

func newUnprotectedNetwork() (Network, error) {
	// Numeric pinning cannot be promised by an ambient HTTP proxy. Never
	// silently bypass an operator's explicit proxy setting to make a fetch work.
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if os.Getenv(name) != "" {
			return nil, &ConfigurationError{Kind: "proxy_incompatible_with_numeric_pinning"}
		}
	}
	return &directNetwork{}, nil
}

type resolverNetwork struct {
	resolver func(context.Context, string) ([]net.IP, error)
	dial     dialFunc
}

func (n resolverNetwork) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return n.resolver(ctx, host)
}
func (n resolverNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return n.dial(ctx, network, address)
}

func newFetchTransport(rootCAs *x509.CertPool) *http.Transport {
	dialer := &net.Dialer{Timeout: fetchTimeout}
	return &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext,
		TLSClientConfig:     &tls.Config{RootCAs: rootCAs, MinVersion: tls.VersionTLS12, Renegotiation: tls.RenegotiateOnceAsClient},
		TLSHandshakeTimeout: 10 * time.Second, DisableKeepAlives: true,
		ForceAttemptHTTP2: false, TLSNextProto: make(map[string]func(string, *tls.Conn) http.RoundTripper),
	}
}

func networkDial(network Network, block func(net.IP) bool) dialFunc {
	return func(ctx context.Context, kind, address string) (net.Conn, error) {
		if network == nil {
			return nil, ErrNetworkRequired
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errInvalidOrigin
		}
		var addresses []net.IP
		if ip := net.ParseIP(host); ip != nil {
			addresses = []net.IP{ip}
		} else {
			addresses, err = network.LookupIP(ctx, host)
			if err != nil {
				return nil, err
			}
		}
		if len(addresses) == 0 {
			return nil, errors.New("subscription DNS returned no addresses")
		}
		if len(addresses) > 64 {
			return nil, &BudgetError{Kind: "dns_addresses", Limit: 64}
		}
		blocked := false
		var last error
		for _, ip := range addresses {
			if ip == nil || (block != nil && block(ip)) {
				blocked = true
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			connection, err := network.DialContext(ctx, kind, net.JoinHostPort(ip.String(), port))
			if err == nil && connection != nil {
				return connection, nil
			}
			if connection != nil {
				connection.Close()
			}
			last = err
		}
		if blocked {
			return nil, errBlockedAddress
		}
		if last == nil {
			last = errors.New("subscription network returned no connection")
		}
		return nil, last
	}
}

func FetchWithOptions(ctx context.Context, rawURL string, opts FetchOptions) ([]byte, http.Header, error) {
	if opts.Network == nil {
		return nil, nil, ErrNetworkRequired
	}
	if !opts.Limits.valid() {
		return nil, nil, errInvalidLimits
	}
	cfg := fetchConfig{network: opts.Network, headers: opts.Headers, allowHTTP: opts.AllowHTTP, limits: opts.Limits, disableFallback: true}
	if !startupAllowPrivate {
		cfg.blockAddr = blockedByDefaultIP
	}
	return fetchWithConfig(ctx, rawURL, cfg)
}
