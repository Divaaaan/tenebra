package singbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/maintenance"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

func maintenanceEngineBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("TENEBRA_TEST_ENGINE")
	if binary == "" {
		t.Skip("native maintenance gate NOT RUN: set pinned TENEBRA_TEST_ENGINE and TENEBRA_TEST_ENGINE_SHA256")
	}
	expected := os.Getenv("TENEBRA_TEST_ENGINE_SHA256")
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	actual := sha256.Sum256(data)
	if len(expected) != 64 || hex.EncodeToString(actual[:]) != expected {
		t.Fatal("maintenance engine SHA256 does not match explicit pin")
	}
	version, err := exec.Command(binary, "version").Output()
	if err != nil || !strings.Contains(string(version), "sing-box version 1.13.13\n") {
		t.Fatal("maintenance engine must be version 1.13.13")
	}
	return binary
}

func maintenanceEngineRoot(t *testing.T) *securefs.Root {
	t.Helper()
	if base := os.Getenv("TENEBRA_SECUREFS_TEST_ROOT"); base != "" {
		t.Setenv("GOTMPDIR", filepath.Clean(base))
	} else if runtime.GOOS == "windows" {
		t.Fatal("native config requires explicit protected TENEBRA_SECUREFS_TEST_ROOT")
	}
	root, err := securefs.OpenRoot(filepath.Join(t.TempDir(), "private"), securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	return root
}

func maintenanceEngineConfig(t *testing.T, root *securefs.Root, config map[string]any) (string, string) {
	t.Helper()
	mutable, err := root.OpenDir("mutable", securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mutable.Close(); err != nil {
			t.Error(err)
		}
	})
	dir, err := mutable.Path()
	if err != nil {
		t.Fatal(err)
	}
	if experimental, ok := config["experimental"].(map[string]any); ok {
		experimental["cache_file"] = cacheFileBlock(dir)
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := root.NewStage("engine-config")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stage.Close(); err != nil {
			t.Error(err)
		}
	})
	if outcome, err := stage.Root().ReplaceFile("config.json", data); err != nil || outcome != securefs.Committed {
		t.Fatal("private config write failed", err)
	}
	lease, err := stage.Root().LeaseTree(context.Background(), securefs.Limits{MaxFiles: 1, MaxBytes: int64(len(data))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lease.Close(); err != nil {
			t.Error(err)
		}
	})
	path, err := lease.Path()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(path, "config.json"), dir
}

func TestMaintenanceEngineConfigAcceptance(t *testing.T) {
	binary := maintenanceEngineBinary(t)
	for _, mode := range []string{"default", "private", "chain"} {
		t.Run(mode, func(t *testing.T) {
			root := maintenanceEngineRoot(t)
			run, err := maintenance.PrepareRuntime(context.Background(), maintenance.RuntimeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer run.Close()
			nodes := maintenanceTestNodes("proxy", "direct", "block", "maintenance-in", "proxy", "proxy-2", "tun-in", "mixed-in", "dns-remote", "probe-in-0")
			options := routing.Options{}
			tun := TunOptions{Mode: ModeSystemProxy, MixedPort: routingFreePort(t), ClashAPIPort: routingFreePort(t)}
			private := run.InboundConfig()
			if mode != "default" {
				tun.Maintenance = &private
			}
			if mode == "chain" {
				options.Multihop = true
				options.MultihopEntry = "proxy-2"
				options.MultihopExit = "direct-2"
			}
			config, err := Build(nodes, "", options, tun)
			if err != nil {
				t.Fatal(err)
			}
			path, dir := maintenanceEngineConfig(t, root, config)
			command := exec.Command(binary, "check", "-c", path)
			command.Dir = dir
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("pinned engine refused config: %v; %s", err, output)
			}
		})
	}
}

func TestMaintenanceEngineTunnelKeepsOriginTLSAndVPNSelector(t *testing.T) {
	binary := maintenanceEngineBinary(t)
	root := maintenanceEngineRoot(t)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "SYNTHETIC_MAINTENANCE_OK") }))
	defer origin.Close()
	socks, count := routingSOCKSFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, err := maintenance.PrepareRuntime(ctx, maintenance.RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	private := run.InboundConfig()
	mixedPort := routingFreePort(t)
	config, err := Build(maintenanceTestNodes("fixture"), "fixture", routing.Options{Mode: routing.ModeDirect}, TunOptions{Mode: ModeSystemProxy, MixedPort: mixedPort, ClashAPIPort: routingFreePort(t), Maintenance: &private})
	if err != nil {
		t.Fatal(err)
	}
	// Replace only the synthetic upstream transport; generated selector, private
	// inbound and terminal routing are exercised by the actual pinned engine.
	for i, outbound := range config["outbounds"].([]map[string]any) {
		if outbound["tag"] == "fixture" {
			config["outbounds"].([]map[string]any)[i] = map[string]any{"type": "socks", "tag": "fixture", "server": "127.0.0.1", "server_port": socks.Addr().(*net.TCPAddr).Port, "version": "5"}
		}
	}
	path, dir := maintenanceEngineConfig(t, root, config)
	if err := run.ReleasePort(); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary, "run", "-c", path)
	command.Dir = dir
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		command.Process.Kill()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("owned fixture engine did not stop")
		}
	})
	ready := false
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); {
		connection, err := net.DialTimeout("tcp4", "127.0.0.1:"+strconv.Itoa(mixedPort), 50*time.Millisecond)
		if err == nil {
			connection.Close()
			ready = true
			break
		}
		select {
		case <-done:
			t.Fatal("owned fixture engine exited before ready")
		default:
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !ready {
		t.Fatal("owned fixture engine not ready")
	}
	endpoint := run.Endpoint()
	lease, err := maintenance.NewLease(ctx, maintenance.LeaseSpec{OperationID: "synthetic-operation", ProfileID: "synthetic-profile", EngineRunID: endpoint.RunID(), Egress: maintenance.VPNOnly, Deadline: time.Now().Add(10 * time.Second)}, endpoint.DialNumeric)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Revoke(nil)
	roots := x509.NewCertPool()
	roots.AddCert(origin.Certificate())
	transport := &http.Transport{DialContext: lease.DialContext, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get(origin.URL)
	if err != nil {
		t.Fatal("private engine CONNECT failed", err)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64))
	response.Body.Close()
	if err != nil || string(body) != "SYNTHETIC_MAINTENANCE_OK" || count.Load() == 0 {
		t.Fatal("private traffic did not use VPN selector", err)
	}
	// The endpoint CA alone cannot authenticate an unrelated origin certificate.
	wrongRoots := x509.NewCertPool()
	if !wrongRoots.AppendCertsFromPEM([]byte(private.CertificatePEM)) {
		t.Fatal("invalid runtime certificate")
	}
	wrong := &http.Transport{DialContext: lease.DialContext, TLSClientConfig: &tls.Config{RootCAs: wrongRoots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	defer wrong.CloseIdleConnections()
	if result, err := (&http.Client{Transport: wrong, Timeout: 3 * time.Second}).Get(origin.URL); err == nil {
		result.Body.Close()
		t.Fatal("runtime TLS trust replaced origin verification")
	}
}
