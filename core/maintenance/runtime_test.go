package maintenance

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func runtimeForTest(t *testing.T, options RuntimeOptions) *Runtime {
	t.Helper()
	runtime, err := PrepareRuntime(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Close() })
	return runtime
}

func TestMaintenanceCertSurvivesLongRun(t *testing.T) {
	now := time.Now()
	runtime := runtimeForTest(t, RuntimeOptions{Now: func() time.Time { return now }})
	config := runtime.InboundConfig()
	block, _ := pem.Decode([]byte(config.CertificatePEM))
	if block == nil {
		t.Fatal("missing certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1", CurrentTime: now.AddDate(1, 0, 0)})
	if err != nil {
		t.Fatal("long-running endpoint loses certificate validity", err)
	}
	if !cert.NotAfter.After(now.AddDate(9, 0, 0)) {
		t.Fatal("runtime certificate expires too early")
	}
	data, err := json.Marshal(config)
	if err != nil || string(data) != "{}" {
		t.Fatal("endpoint material is serializable")
	}
	if !config.Address.Addr().IsLoopback() || config.Address.Addr().String() != "127.0.0.1" || config.Address.Port() == 0 {
		t.Fatal("not private loopback")
	}
	if listener, err := net.Listen("tcp4", config.Address.String()); err == nil {
		listener.Close()
		t.Fatal("runtime did not reserve port")
	}
	if err := runtime.ReleasePort(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReleasePort(); err != nil {
		t.Fatal("release must be idempotent", err)
	}
	listener, err := net.Listen("tcp4", config.Address.String())
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
}

func maintenanceListener(t *testing.T, config InboundConfig, address netip.AddrPort) net.Listener {
	t.Helper()
	pair, err := tls.X509KeyPair([]byte(config.CertificatePEM), []byte(config.PrivateKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", address.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
}

func TestMaintenanceImpostorRejectedBeforeAuth(t *testing.T) {
	runtime := runtimeForTest(t, RuntimeOptions{})
	impostor := runtimeForTest(t, RuntimeOptions{})
	config := runtime.InboundConfig()
	if err := runtime.ReleasePort(); err != nil {
		t.Fatal(err)
	}
	listener := maintenanceListener(t, impostor.InboundConfig(), config.Address)
	var plaintext atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		buffer := make([]byte, 8192)
		n, _ := conn.Read(buffer)
		plaintext.Store(int64(n))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := runtime.Endpoint().DialNumeric(ctx, "tcp", netip.MustParseAddrPort("203.0.113.8:443"), func(c net.Conn) (net.Conn, error) { return c, nil })
	if !errors.Is(err, ErrNetworkUnavailable) {
		t.Fatal("untrusted endpoint was not refused")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("impostor connection not closed")
	}
	if plaintext.Load() != 0 {
		t.Fatal("authentication or CONNECT sent to impostor")
	}
	if strings.Contains(err.Error(), config.Password) || strings.Contains(err.Error(), config.Username) {
		t.Fatal("secret in error")
	}
}

func TestMaintenanceCONNECTPreservesBufferedDataAndRegistersBeforeTLS(t *testing.T) {
	runtime := runtimeForTest(t, RuntimeOptions{})
	config := runtime.InboundConfig()
	if err := runtime.ReleasePort(); err != nil {
		t.Fatal(err)
	}
	listener := maintenanceListener(t, config, config.Address)
	var registered atomic.Bool
	server := make(chan error, 1)
	target := netip.MustParseAddrPort("[2001:db8::9]:443")
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			server <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		request, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			server <- err
			return
		}
		if !registered.Load() || request.Method != "CONNECT" || request.Host != target.String() || request.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(config.Username+":"+config.Password)) {
			server <- errors.New("wrong private CONNECT authority")
			return
		}
		if _, err = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\nbuffered"); err != nil {
			server <- err
			return
		}
		buffer := make([]byte, 4)
		_, err = io.ReadFull(conn, buffer)
		if err == nil && string(buffer) != "ping" {
			err = errors.New("wrong tunnel body")
		}
		if err == nil {
			_, err = conn.Write([]byte("pong"))
		}
		server <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, err := runtime.Endpoint().DialNumeric(ctx, "tcp", target, func(c net.Conn) (net.Conn, error) { registered.Store(true); return c, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	buffer := make([]byte, 8)
	if _, err = io.ReadFull(connection, buffer); err != nil || string(buffer) != "buffered" {
		t.Fatal("buffered tunnel bytes lost", err)
	}
	if _, err = connection.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(connection, buffer[:4]); err != nil || string(buffer[:4]) != "pong" {
		t.Fatal("CONNECT tunnel failed", err)
	}
	if err = <-server; err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceRuntimeRejectsRetiredEndpointAndRotatesIdentity(t *testing.T) {
	first, second := runtimeForTest(t, RuntimeOptions{}), runtimeForTest(t, RuntimeOptions{})
	a, b := first.InboundConfig(), second.InboundConfig()
	if a.RunID == "" || a.RunID == b.RunID || a.Username == b.Username || a.Password == b.Password || a.CertificatePEM == b.CertificatePEM {
		t.Fatal("runtime authority reused")
	}
	endpoint := first.Endpoint()
	if endpoint.RunID() != a.RunID {
		t.Fatal("wrong run")
	}
	first.Close()
	_, err := endpoint.DialNumeric(context.Background(), "tcp", netip.MustParseAddrPort("203.0.113.8:443"), func(c net.Conn) (net.Conn, error) { t.Fatal("retired endpoint admitted a raw socket"); return c, nil })
	if !errors.Is(err, ErrNetworkUnavailable) {
		t.Fatal("retired endpoint was not denied")
	}
}
