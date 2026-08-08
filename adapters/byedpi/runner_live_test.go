package byedpi

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/dpi"
)

// findByeDPI locates a real ciadpi binary to run the shipped presets against:
// the TENEBRA_BYEDPI override first, then the bundled resources (where
// fetch-resources drops it), then a repo-root bin/, then PATH. ok=false makes
// the caller skip, so CI without the (gitignored) binary stays green.
func findByeDPI() (string, bool) {
	if p := os.Getenv(dpi.BinaryEnv); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}

	name := dpi.BinaryName()
	if dir, err := os.Getwd(); err == nil {
		for i := 0; i < 6; i++ {
			for _, cand := range []string{
				filepath.Join(dir, "ui-desktop", "src-tauri", "resources", name),
				filepath.Join(dir, "bin", name),
			} {
				if _, statErr := os.Stat(cand); statErr == nil {
					return cand, true
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	return "", false
}

// TestLivePresetsServeSocks runs every shipped preset through the real engine.
// It is the check the offline tests cannot make: that ciadpi accepts the option
// spellings we ship and comes up serving SOCKS on the port we picked. A preset
// with a stale or mistyped option fails here — the process exits at once and
// the wait reports it, with the engine's own complaint in the logs. Skipped
// when no ByeDPI binary is available.
func TestLivePresetsServeSocks(t *testing.T) {
	bin, ok := findByeDPI()
	if !ok {
		t.Skip("ciadpi binary not found (TENEBRA_BYEDPI, resources/, bin/ or PATH); skipping the live engine check")
	}

	for _, strategy := range dpi.DefaultStrategies {
		t.Run(strategy.Name, func(t *testing.T) {
			t.Setenv(dpi.BinaryEnv, bin)

			port := dpi.FreePort()
			argv, err := dpi.BuildArgs(dpi.LoopbackHost, port, strategy.Args)
			if err != nil {
				t.Fatalf("BuildArgs: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			r := New()
			t.Cleanup(func() { _ = r.Stop() })
			if err := r.Start(ctx, argv); err != nil {
				t.Fatalf("Start %q: %v", argv, err)
			}

			addr := net.JoinHostPort(dpi.LoopbackHost, strconv.Itoa(port))
			if err := r.WaitReady(ctx, addr, 10*time.Second); err != nil {
				t.Fatalf("preset %q never served SOCKS on %s: %v\nengine output:\n%s",
					strategy.Name, addr, err, strings.Join(r.Logs(), "\n"))
			}

			// Stopping must actually take the listener down; a lingering process
			// would keep the port and quietly proxy after the user switched the
			// bypass off.
			if err := r.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if err := r.Ready(ctx, addr); err == nil {
				t.Errorf("%s still answers SOCKS after Stop", addr)
			}
		})
	}
}
