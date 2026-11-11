package maintenance

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"
)

const connectHeaderLimit = 8 << 10
const connectHandshakeLimit = 5 * time.Second

type runtimeEndpoint struct{ runtime *Runtime }

func (e *runtimeEndpoint) RunID() RunID { return e.runtime.config.RunID }

func (e *runtimeEndpoint) DialNumeric(ctx context.Context, network string, target netip.AddrPort, register func(net.Conn) (net.Conn, error)) (net.Conn, error) {
	if ctx == nil || register == nil {
		return nil, ErrInvalidLease
	}
	if !target.IsValid() || target.Port() == 0 || target.Addr().Zone() != "" || (network != "tcp" && network != "tcp4" && network != "tcp6") || (network == "tcp4" && !target.Addr().Unmap().Is4()) || (network == "tcp6" && !target.Addr().Is6()) {
		return nil, ErrNumericAddressRequired
	}
	r := e.runtime
	r.mu.Lock()
	available := r.released && !r.closed
	r.mu.Unlock()
	if !available {
		return nil, ErrNetworkUnavailable
	}
	if ctx.Err() != nil {
		return nil, &causeError{ErrNetworkUnavailable, ctx.Err()}
	}
	handshake, cancel := context.WithTimeout(ctx, connectHandshakeLimit)
	defer cancel()
	stopRuntime := context.AfterFunc(r.ctx, cancel)
	defer stopRuntime()
	var dialer net.Dialer
	raw, err := dialer.DialContext(handshake, "tcp4", r.config.Address.String())
	if err != nil {
		return nil, ErrNetworkUnavailable
	}
	tracked := &runtimeConnection{Conn: raw, runtime: r}
	r.mu.Lock()
	closed := r.closed
	if !closed {
		r.connections[tracked] = struct{}{}
	}
	r.mu.Unlock()
	if closed {
		tracked.Close()
		return nil, ErrNetworkUnavailable
	}
	registered, err := register(tracked)
	if err != nil || registered == nil {
		tracked.Close()
		if err == nil {
			return nil, ErrRawRegistrationRequired
		}
		return nil, &causeError{ErrNetworkUnavailable, err}
	}
	// Raw admission precedes even the ClientHello, and its close never waits on
	// TLS close_notify. A revoked lease can therefore interrupt every boundary.
	ok := false
	defer func() {
		if !ok {
			registered.Close()
			tracked.Close()
		}
	}()
	stop := context.AfterFunc(handshake, func() { registered.Close() })
	defer stop()
	deadline, _ := handshake.Deadline()
	if registered.SetDeadline(deadline) != nil {
		return nil, ErrNetworkUnavailable
	}
	roots := x509.NewCertPool()
	roots.AddCert(r.certificate)
	secure := tls.Client(registered, &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12, Time: r.now, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) != 1 {
			return ErrNetworkUnavailable
		}
		pin := sha256.Sum256(state.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare(pin[:], r.pin[:]) != 1 {
			return ErrNetworkUnavailable
		}
		return nil
	}})
	if secure.HandshakeContext(handshake) != nil {
		return nil, ErrNetworkUnavailable
	}
	authorization := base64.StdEncoding.EncodeToString([]byte(r.config.Username + ":" + r.config.Password))
	request := "CONNECT " + target.String() + " HTTP/1.1\r\nHost: " + target.String() + "\r\nProxy-Authorization: Basic " + authorization + "\r\n\r\n"
	if _, err = io.WriteString(secure, request); err != nil {
		return nil, ErrNetworkUnavailable
	}
	reader := bufio.NewReaderSize(secure, connectHeaderLimit)
	header := make([]byte, 0, 256)
	for len(header) < connectHeaderLimit {
		value, readErr := reader.ReadByte()
		if readErr != nil {
			return nil, ErrNetworkUnavailable
		}
		header = append(header, value)
		if bytes.HasSuffix(header, []byte("\r\n\r\n")) {
			break
		}
	}
	if !bytes.HasSuffix(header, []byte("\r\n\r\n")) {
		return nil, ErrResourceLimit
	}
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(header)), &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		return nil, ErrNetworkUnavailable
	}
	if handshake.Err() != nil {
		return nil, &causeError{ErrNetworkUnavailable, handshake.Err()}
	}
	if registered.SetDeadline(time.Time{}) != nil {
		return nil, ErrNetworkUnavailable
	}
	r.mu.Lock()
	closed = r.closed
	r.mu.Unlock()
	if closed {
		return nil, ErrNetworkUnavailable
	}
	// Stop the handshake-only cancellation before handing off the live tunnel.
	// The lease retains the raw socket and its operation cancellation independently.
	if !stop() {
		return nil, ErrNetworkUnavailable
	}
	ok = true
	return &connectStream{Conn: secure, raw: registered, reader: reader}, nil
}

type connectStream struct {
	net.Conn
	raw    net.Conn
	reader *bufio.Reader
}

func (c *connectStream) Read(buffer []byte) (int, error) { return c.reader.Read(buffer) }
func (c *connectStream) Close() error                    { return c.raw.Close() }
