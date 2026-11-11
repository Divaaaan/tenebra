package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

var _ control.MaintenanceRunner = (*Runner)(nil)

func maintenanceFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestRunnerMaintenanceActualProcessRetainsConfigUntilExit(t *testing.T) {
	binary := os.Getenv("TENEBRA_TEST_ENGINE")
	if binary == "" {
		t.Skip("native runner gate NOT RUN: provide pinned engine")
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != os.Getenv("TENEBRA_TEST_ENGINE_SHA256") {
		t.Fatal("native runner engine hash is not pinned")
	}
	if base := os.Getenv("TENEBRA_SECUREFS_TEST_ROOT"); base != "" {
		t.Setenv("GOTMPDIR", filepath.Clean(base))
	} else if runtime.GOOS == "windows" {
		t.Fatal("explicit protected native test root required")
	}
	root, err := securefs.OpenRoot(filepath.Join(t.TempDir(), "private"), securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	mutable, err := root.OpenDir("mutable", securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer mutable.Close()
	cache, err := mutable.Path()
	if err != nil {
		t.Fatal(err)
	}
	runner := New()
	runner.binOverride = binary
	if err := runner.SetRuntimeRoot(root); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	private, err := runner.PrepareMaintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.DiscardMaintenance(private.RunID)
	config, err := singbox.Build([]model.Node{{Name: "synthetic", Protocol: model.Trojan, Server: "203.0.113.8", Port: 443, Password: "synthetic"}}, "synthetic", routing.Options{}, singbox.TunOptions{Mode: singbox.ModeSystemProxy, MixedPort: maintenanceFreePort(t), ClashAPIPort: maintenanceFreePort(t), CacheDir: cache, Maintenance: &private})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(ctx, encoded); err != nil {
		t.Fatal("system-proxy fixture must not require a TUN DLL", err)
	}
	defer runner.Stop()
	path := runner.cfgPath
	if _, err := os.Stat(path); err != nil {
		t.Fatal("active private config unavailable", err)
	}
	if err := root.Close(); !errors.Is(err, securefs.ErrBusy) {
		t.Fatal("running process lost config custody", err)
	}
	ready := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if endpoint, err := runner.MaintenanceEndpoint(private.RunID); err == nil {
			if endpoint.RunID() != private.RunID {
				t.Fatal("wrong live run")
			}
			ready = true
			break
		}
		select {
		case err := <-runner.Done():
			t.Fatal("owned engine exited before readiness", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("matching pinned engine endpoint never became ready")
	}
	if _, err := runner.MaintenanceEndpoint("old-run"); err == nil {
		t.Fatal("stale run accepted")
	}
	if err := runner.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("retired config remains active", err)
	}
	if _, err := runner.MaintenanceEndpoint(private.RunID); err == nil {
		t.Fatal("dead engine endpoint available")
	}
	next, err := runner.PrepareMaintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.DiscardMaintenance(next.RunID)
	if next.RunID == private.RunID || next.Password == private.Password {
		t.Fatal("engine identity reused")
	}
}
