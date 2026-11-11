package singbox

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/routing"
)

// The native fixture starts only loopback listeners. It never changes a system
// proxy, interface, route, firewall, DNS setting or installed service.
func TestRoutingEngineAppPolicy(t *testing.T) {
	bin := os.Getenv("TENEBRA_TEST_ENGINE")
	if bin == "" {
		t.Skip("native routing gate NOT RUN: set TENEBRA_TEST_ENGINE to pinned sing-box 1.13.13")
	}
	version, err := exec.Command(bin, "version").Output()
	if err != nil || !strings.Contains(string(version), "sing-box version 1.13.13\n") {
		t.Fatalf("requires pinned engine 1.13.13: %v", err)
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native process ownership is unsupported on this OS")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "SYNTHETIC_ROUTING_OK")
	}))
	t.Cleanup(server.Close)
	socks, proxyCount := routingSOCKSFixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binaryBytes, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, actual, configured string
		split                    routing.SplitMode
		wantProxy                bool
	}{
		{"include mixed case", "App[1].exe", "APP[1].EXE", routing.SplitInclude, true},
		{"include escaped negative", "App1.exe", "App[1].exe", routing.SplitInclude, false},
		{"include unicode", "Приложение+.exe", "ПРИЛОЖЕНИЕ+.EXE", routing.SplitInclude, true},
		{"include empty", "App.exe", "", routing.SplitInclude, false},
		{"include unlisted", "Other.exe", "App.exe", routing.SplitInclude, false},
		{"exclude mixed case", "App[1].exe", "APP[1].EXE", routing.SplitExclude, false},
		{"exclude escaped negative", "App1.exe", "App[1].exe", routing.SplitExclude, true},
		{"exclude unicode", "Приложение+.exe", "ПРИЛОЖЕНИЕ+.EXE", routing.SplitExclude, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			child := filepath.Join(dir, tc.actual)
			if err := os.WriteFile(child, binaryBytes, 0o700); err != nil {
				t.Fatal(err)
			}
			port := routingFreePort(t)
			var apps []string
			if tc.configured != "" {
				apps = []string{tc.configured}
			}
			nodes := []model.Node{{Protocol: model.Trojan, Name: "fixture", Server: "127.0.0.1", Port: 443, Password: "TEST_ONLY"}}
			cfg, err := Build(nodes, "fixture", routing.Options{Mode: routing.ModeGlobal,
				SplitMode: tc.split, SplitApps: apps, RulesProxy: []string{"example.test"}, UnblockServices: true},
				TunOptions{Mode: ModeSystemProxy, MixedPort: port})
			if err != nil {
				t.Fatal(err)
			}
			// Replace just the synthetic node transport. Selector, route and DNS are
			// the real generated configuration and the real engine selects a path.
			for i, outbound := range cfg["outbounds"].([]map[string]any) {
				if outbound["tag"] == "fixture" {
					cfg["outbounds"].([]map[string]any)[i] = map[string]any{"type": "socks", "tag": "fixture",
						"server": "127.0.0.1", "server_port": socks.Addr().(*net.TCPAddr).Port, "version": "5"}
				}
			}
			delete(cfg, "experimental")
			routingRunEngine(t, bin, dir, port, cfg)
			before := proxyCount.Load()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, child, "-test.run=^TestRoutingEngineHelperProcess$")
			cmd.Env = append(os.Environ(), "TENEBRA_ROUTING_CHILD=1",
				"TENEBRA_ROUTING_PROXY=http://127.0.0.1:"+strconv.Itoa(port), "TENEBRA_ROUTING_TARGET="+server.URL)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("native request: %v\n%s", err, output)
			}
			gotProxy := proxyCount.Load() > before
			if gotProxy != tc.wantProxy {
				t.Fatalf("actual outbound proxy=%v, want %v; process=%q configured=%q", gotProxy, tc.wantProxy, tc.actual, tc.configured)
			}
		})
	}
}

func TestRoutingEngineHelperProcess(t *testing.T) {
	if os.Getenv("TENEBRA_ROUTING_CHILD") != "1" {
		return
	}
	proxy, err := url.Parse(os.Getenv("TENEBRA_ROUTING_PROXY"))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
	response, err := client.Get(os.Getenv("TENEBRA_ROUTING_TARGET"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64))
	if err != nil || string(body) != "SYNTHETIC_ROUTING_OK" {
		t.Fatalf("unexpected fixture response: %v", err)
	}
}

func routingFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func routingRunEngine(t *testing.T, bin, dir string, port int, cfg map[string]any) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "synthetic-config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(dir, "engine.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run", "-c", path)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("fixture engine did not stop")
		}
		log.Close()
		if t.Failed() {
			data, _ := os.ReadFile(filepath.Join(dir, "engine.log"))
			t.Logf("synthetic engine log:\n%s", data)
		}
	})
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("fixture engine exited before ready: %v", err)
		default:
		}
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 50*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("fixture engine did not open loopback listener")
}

func routingSOCKSFixture(t *testing.T) (net.Listener, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	count := &atomic.Int64{}
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for connection := range connections {
			connection.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[client] = true
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer client.Close()
				defer func() { mu.Lock(); delete(connections, client); mu.Unlock() }()
				client.SetDeadline(time.Now().Add(8 * time.Second))
				var greeting [2]byte
				if _, err := io.ReadFull(client, greeting[:]); err != nil || greeting[0] != 5 {
					return
				}
				methods := make([]byte, int(greeting[1]))
				if _, err := io.ReadFull(client, methods); err != nil {
					return
				}
				if _, err := client.Write([]byte{5, 0}); err != nil {
					return
				}
				var header [4]byte
				if _, err := io.ReadFull(client, header[:]); err != nil || header[0] != 5 || header[1] != 1 || header[3] != 1 {
					return
				}
				var target [6]byte
				if _, err := io.ReadFull(client, target[:]); err != nil {
					return
				}
				ip := net.IP(target[:4])
				if !ip.IsLoopback() {
					return
				}
				upstream, err := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(int(binary.BigEndian.Uint16(target[4:])))), time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				count.Add(1)
				if _, err := client.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				copied := make(chan struct{})
				go func() { io.Copy(upstream, client); upstream.Close(); close(copied) }()
				io.Copy(client, upstream)
				client.Close()
				<-copied
			}()
		}
	}()
	return listener, count
}

