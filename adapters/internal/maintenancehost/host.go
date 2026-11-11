package maintenancehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/Divaaaan/tenebra/core/maintenance"
	"github.com/Divaaaan/tenebra/internal/securefs"
	"net"
	"sync"
	"time"
)

type Host struct {
	mu          sync.Mutex
	root        *securefs.Root
	fault       error
	preparing   bool
	configuring bool
	legacyUsed  bool
	pending     *maintenance.Runtime
	active      *Launch
}
type Launch struct {
	host            *Host
	runtime         *maintenance.Runtime
	config          *ConfigLease
	ctx             context.Context
	cancel          context.CancelFunc
	started, closed bool // guarded by host.mu
}

func (h *Host) SetRoot(root *securefs.Root) error {
	h.mu.Lock()
	if h.configuring || h.legacyUsed {
		h.mu.Unlock()
		return securefs.ErrBusy
	}
	if root == nil {
		defer h.mu.Unlock()
		if h.root != nil {
			return securefs.ErrBusy
		}
		h.fault = securefs.ErrUntrusted
		return h.fault
	}
	if h.fault != nil {
		err := h.fault
		h.mu.Unlock()
		return err
	}
	if h.root != nil {
		same := h.root == root
		h.mu.Unlock()
		if same {
			return nil
		}
		return securefs.ErrBusy
	}
	h.configuring = true
	h.mu.Unlock()
	_, verification := root.Path()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.configuring = false
	if h.fault != nil {
		return h.fault
	}
	if h.root != nil {
		if h.root == root {
			return nil
		}
		return securefs.ErrBusy
	}
	if verification != nil {
		h.fault = verification
		return verification
	}
	h.root = root
	return nil
}

func (h *Host) Prepare(ctx context.Context) (maintenance.InboundConfig, error) {
	h.mu.Lock()
	if h.configuring {
		h.mu.Unlock()
		return maintenance.InboundConfig{}, securefs.ErrBusy
	}
	if h.fault != nil {
		err := h.fault
		h.mu.Unlock()
		return maintenance.InboundConfig{}, err
	}
	if h.root == nil {
		h.mu.Unlock()
		return maintenance.InboundConfig{}, maintenance.ErrNetworkUnavailable
	}
	if h.preparing || h.pending != nil || h.active != nil {
		h.mu.Unlock()
		return maintenance.InboundConfig{}, securefs.ErrBusy
	}
	h.preparing = true
	h.mu.Unlock()
	runtime, err := maintenance.PrepareRuntime(ctx, maintenance.RuntimeOptions{})
	h.mu.Lock()
	h.preparing = false
	if err == nil {
		h.pending = runtime
	}
	h.mu.Unlock()
	if err != nil {
		return maintenance.InboundConfig{}, err
	}
	return runtime.InboundConfig(), nil
}

func privateInbound(data []byte) (json.RawMessage, error) {
	var document struct {
		Inbounds []json.RawMessage `json:"inbounds"`
	}
	if len(data) == 0 || len(data) > maxConfigBytes || json.Unmarshal(data, &document) != nil {
		return nil, maintenance.ErrNetworkUnavailable
	}
	var found json.RawMessage
	for _, raw := range document.Inbounds {
		var name struct {
			Tag string `json:"tag"`
		}
		if json.Unmarshal(raw, &name) != nil {
			return nil, maintenance.ErrNetworkUnavailable
		}
		if name.Tag == "maintenance-in" {
			if found != nil {
				return nil, maintenance.ErrNetworkUnavailable
			}
			found = raw
		}
	}
	return found, nil
}

func matchesRuntime(raw json.RawMessage, private maintenance.InboundConfig) bool {
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	expected := map[string]any{"type": "http", "tag": "maintenance-in", "listen": "127.0.0.1", "listen_port": int(private.Address.Port()), "users": []map[string]any{{"username": private.Username, "password": private.Password}}, "tls": map[string]any{"enabled": true, "min_version": "1.2", "certificate": []string{private.CertificatePEM}, "key": []string{private.PrivateKeyPEM}}}
	got, err := json.Marshal(object)
	if err != nil {
		return false
	}
	want, err := json.Marshal(expected)
	return err == nil && bytes.Equal(got, want)
}

func (h *Host) Stage(ctx context.Context, data []byte) (*Launch, error) {
	if ctx == nil {
		return nil, maintenance.ErrInvalidLease
	}
	raw, err := privateInbound(data)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.fault != nil {
		err := h.fault
		h.mu.Unlock()
		return nil, err
	}
	if h.active != nil || h.preparing || h.configuring {
		h.mu.Unlock()
		return nil, securefs.ErrBusy
	}
	root, pending := h.root, h.pending
	if root == nil {
		if raw == nil {
			h.legacyUsed = true
		}
		h.mu.Unlock()
		if raw != nil {
			return nil, maintenance.ErrNetworkUnavailable
		}
		return nil, nil
	}
	if (raw != nil) != (pending != nil) || (pending != nil && !matchesRuntime(raw, pending.InboundConfig())) {
		h.pending = nil
		h.mu.Unlock()
		if pending != nil {
			pending.Close()
		}
		return nil, maintenance.ErrNetworkUnavailable
	}
	launch := &Launch{host: h, runtime: pending}
	launch.ctx, launch.cancel = context.WithCancel(ctx)
	h.pending = nil
	h.active = launch
	h.mu.Unlock()
	config, err := StageConfig(ctx, root, data)
	if err != nil {
		return nil, errors.Join(err, launch.Close())
	}
	launch.config = config
	return launch, nil
}

func (l *Launch) Path() (string, error) {
	if l == nil || l.config == nil {
		return "", securefs.ErrClosed
	}
	return l.config.Path()
}
func (l *Launch) ReleasePort() error {
	if l.runtime == nil {
		return nil
	}
	return l.runtime.ReleasePort()
}
func (l *Launch) Started() {
	l.host.mu.Lock()
	if !l.closed {
		l.started = true
	}
	l.host.mu.Unlock()
}

func (l *Launch) Close() error {
	if l == nil {
		return nil
	}
	l.host.mu.Lock()
	l.closed = true
	l.host.mu.Unlock()
	l.cancel()
	if l.runtime != nil {
		l.runtime.Close()
	}
	var err error
	if l.config != nil {
		err = l.config.Close()
	}
	if err == nil {
		l.host.mu.Lock()
		if l.host.active == l {
			l.host.active = nil
		}
		l.host.mu.Unlock()
	}
	return err
}

func (h *Host) Discard(id maintenance.RunID) error {
	h.mu.Lock()
	if h.active != nil && h.active.runtime != nil && h.active.runtime.InboundConfig().RunID == id {
		active := h.active
		closed := active.closed
		h.mu.Unlock()
		if closed {
			return active.Close()
		}
		return securefs.ErrBusy
	}
	var pending *maintenance.Runtime
	if h.pending != nil && h.pending.InboundConfig().RunID == id {
		pending = h.pending
		h.pending = nil
	}
	h.mu.Unlock()
	if pending != nil {
		return pending.Close()
	}
	return nil
}

func (h *Host) Endpoint(id maintenance.RunID) (maintenance.Endpoint, error) {
	h.mu.Lock()
	active := h.active
	if active == nil || active.runtime == nil || !active.started || active.closed || active.runtime.InboundConfig().RunID != id {
		h.mu.Unlock()
		return nil, maintenance.ErrNetworkUnavailable
	}
	private := active.runtime.InboundConfig()
	ctx := active.ctx
	h.mu.Unlock()
	if err := verifyRuntimeEndpoint(ctx, private); err != nil {
		return nil, err
	}
	h.mu.Lock()
	valid := h.active == active && !active.closed && active.started && ctx.Err() == nil
	h.mu.Unlock()
	if !valid {
		return nil, maintenance.ErrNetworkUnavailable
	}
	return active.runtime.Endpoint(), nil
}

// Readiness sends no credentials or CONNECT target. Operation streams verify
// the same pinned TLS identity independently before any authentication.
func verifyRuntimeEndpoint(parent context.Context, private maintenance.InboundConfig) error {
	ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
	defer cancel()
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "tcp4", private.Address.String())
	if err != nil {
		return maintenance.ErrNetworkUnavailable
	}
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	pair, err := tls.X509KeyPair([]byte(private.CertificatePEM), []byte(private.PrivateKeyPEM))
	if err != nil || len(pair.Certificate) != 1 {
		return maintenance.ErrNetworkUnavailable
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return maintenance.ErrNetworkUnavailable
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	pin := sha256.Sum256(certificate.Raw)
	connection := tls.Client(raw, &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) != 1 {
			return maintenance.ErrNetworkUnavailable
		}
		actual := sha256.Sum256(state.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare(actual[:], pin[:]) != 1 {
			return maintenance.ErrNetworkUnavailable
		}
		return nil
	}})
	if connection.HandshakeContext(ctx) != nil {
		return maintenance.ErrNetworkUnavailable
	}
	return nil
}
