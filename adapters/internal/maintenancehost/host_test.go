package maintenancehost

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/maintenance"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func hostConfig(t *testing.T, private maintenance.InboundConfig) []byte {
	t.Helper()
	config, err := singbox.Build([]model.Node{{Name: "synthetic", Protocol: model.Trojan, Server: "203.0.113.8", Port: 443, Password: "synthetic"}}, "synthetic", routing.Options{}, singbox.TunOptions{Maintenance: &private})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRunnerMaintenanceRunIsolation(t *testing.T) {
	var host Host
	if err := host.SetRoot(configTestRoot(t)); err != nil {
		t.Fatal(err)
	}
	private, err := host.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Endpoint(private.RunID); err == nil {
		t.Fatal("unstarted endpoint available")
	}
	launch, err := host.Stage(context.Background(), hostConfig(t, private))
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Close()
	if err := host.Discard(private.RunID); !errors.Is(err, securefs.ErrBusy) {
		t.Fatal("staged runtime was discarded", err)
	}
	if _, err := host.Endpoint(private.RunID); err == nil {
		t.Fatal("unstarted staged endpoint available")
	}
	if err := launch.ReleasePort(); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(private.CertificatePEM), []byte(private.PrivateKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", private.Address.String())
	if err != nil {
		t.Fatal(err)
	}
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	defer tlsListener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := tlsListener.Accept()
			if err != nil {
				return
			}
			connection.(*tls.Conn).Handshake()
			connection.Close()
		}
	}()
	defer func() { tlsListener.Close(); <-done }()
	launch.Started()
	endpoint, err := host.Endpoint(private.RunID)
	deadline := time.Now().Add(5 * time.Second)
	for errors.Is(err, maintenance.ErrNetworkUnavailable) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		endpoint, err = host.Endpoint(private.RunID)
	}
	if err != nil || endpoint.RunID() != private.RunID {
		t.Fatal("matching pinned live run refused", err)
	}
	if _, err := host.Endpoint("different-run"); err == nil {
		t.Fatal("wrong run accepted")
	}
	if err := launch.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Endpoint(private.RunID); err == nil {
		t.Fatal("retired run available")
	}
	next, err := host.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Discard(next.RunID)
	if next.RunID == private.RunID || next.Password == private.Password {
		t.Fatal("runtime identity reused")
	}
}

func TestRunnerMaintenanceConfigMustMatchPreparedIdentity(t *testing.T) {
	var host Host
	if err := host.SetRoot(configTestRoot(t)); err != nil {
		t.Fatal(err)
	}
	private, err := host.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Discard(private.RunID)
	private.Password = "different-synthetic-password"
	if launch, err := host.Stage(context.Background(), hostConfig(t, private)); err == nil {
		launch.Close()
		t.Fatal("different identity reached runner")
	}
}

func TestRunnerMaintenanceUnsafeRootCannotFallBackToPublicTemp(t *testing.T) {
	var host Host
	if err := host.SetRoot(nil); err == nil {
		t.Fatal("nil configured root accepted")
	}
	if launch, err := host.Stage(context.Background(), []byte(`{}`)); err == nil {
		if launch != nil {
			launch.Close()
		}
		t.Fatal("unsafe configured root downgraded")
	}
	var legacy Host
	if launch, err := legacy.Stage(context.Background(), []byte(`{}`)); err != nil || launch != nil {
		t.Fatal("optional capability broke ordinary legacy bootstrap", err)
	}
}
