package maintenance

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"sync"
	"time"
)

// Runtime owns one reserved loopback port and one cryptographic engine identity.
// Its certificate is never installed in a machine or process-wide trust store.
type Runtime struct {
	mu               sync.Mutex
	config           InboundConfig
	certificate      *x509.Certificate
	pin              [sha256.Size]byte
	now              func() time.Time
	reservation      net.Listener
	released, closed bool
	connections      map[*runtimeConnection]struct{}
	stop             func() bool
	ctx              context.Context
	cancel           context.CancelFunc
}

func PrepareRuntime(ctx context.Context, options RuntimeOptions) (*Runtime, error) {
	if ctx == nil {
		return nil, ErrInvalidLease
	}
	if ctx.Err() != nil {
		return nil, &causeError{ErrNetworkUnavailable, ctx.Err()}
	}
	reader := options.Rand
	if reader == nil {
		reader = rand.Reader
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), reader)
	if err != nil {
		return nil, ErrNetworkUnavailable
	}
	serial, err := rand.Int(reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, ErrNetworkUnavailable
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	created := now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Tenebra private maintenance"}, NotBefore: created.Add(-5 * time.Minute), NotAfter: created.AddDate(10, 0, 0), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, ErrNetworkUnavailable
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, ErrNetworkUnavailable
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, ErrNetworkUnavailable
	}
	material := make([]byte, 16+16+32)
	if _, err = io.ReadFull(reader, material); err != nil {
		return nil, ErrNetworkUnavailable
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, ErrNetworkUnavailable
	}
	runtime := &Runtime{certificate: certificate, pin: sha256.Sum256(der), now: now, reservation: listener, connections: make(map[*runtimeConnection]struct{})}
	runtime.ctx, runtime.cancel = context.WithCancel(ctx)
	runtime.config = InboundConfig{RunID: RunID(base64.RawURLEncoding.EncodeToString(material[:16])), Address: listener.Addr().(*net.TCPAddr).AddrPort(), CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})), Username: base64.RawURLEncoding.EncodeToString(material[16:32]), Password: base64.RawURLEncoding.EncodeToString(material[32:])}
	// Install the cancellation hook under the metadata lock. If the context was
	// cancelled during preparation, Close can only run after all fields exist.
	runtime.mu.Lock()
	runtime.stop = context.AfterFunc(ctx, func() { runtime.Close() })
	runtime.mu.Unlock()
	if ctx.Err() != nil {
		runtime.Close()
		return nil, &causeError{ErrNetworkUnavailable, ctx.Err()}
	}
	return runtime, nil
}

func (r *Runtime) InboundConfig() InboundConfig { return r.config }
func (r *Runtime) Endpoint() Endpoint           { return &runtimeEndpoint{runtime: r} }
func (r *Runtime) String() string               { return "private maintenance runtime" }
func (r *Runtime) GoString() string             { return r.String() }
func (c InboundConfig) String() string          { return "private maintenance inbound" }
func (c InboundConfig) GoString() string        { return c.String() }

func (r *Runtime) ReleasePort() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrNetworkUnavailable
	}
	listener := r.reservation
	r.reservation = nil
	r.released = true
	r.mu.Unlock()
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			r.Close()
			return ErrNetworkUnavailable
		}
	}
	return nil
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	listener, stop := r.reservation, r.stop
	r.reservation = nil
	connections := make([]*runtimeConnection, 0, len(r.connections))
	for connection := range r.connections {
		connections = append(connections, connection)
	}
	r.mu.Unlock()
	r.cancel()
	if stop != nil {
		stop()
	}
	if listener != nil {
		listener.Close()
	}
	for _, connection := range connections {
		connection.Close()
	}
	return nil
}

type runtimeConnection struct {
	net.Conn
	runtime *Runtime
	once    sync.Once
	err     error
}

func (c *runtimeConnection) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.runtime.mu.Lock()
		delete(c.runtime.connections, c)
		c.runtime.mu.Unlock()
	})
	return c.err
}
