// Command tenebra-core is the sidecar that owns sing-box and the tunnel. The
// desktop UI speaks to it over stdin/stdout using the line-delimited JSON
// control protocol; stdout therefore carries protocol traffic only and every
// diagnostic goes to stderr.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Divaaaan/tenebra/adapters/windows"
	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/profile"
)

func main() {
	if err := run(); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

// run wires up the core: it opens the profile store, builds the runner and
// control daemon, restores persisted last-good and routing settings, and serves
// the JSON protocol on stdin/stdout until EOF or a shutdown signal.
func run() error {
	// stdout is the protocol channel; keep all logging off it.
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags)

	dir := configDir()
	store, err := profile.Open(dir)
	if err != nil {
		return fmt.Errorf("open profile store: %w", err)
	}

	runner := windows.New()
	daemon := control.NewDaemon(store, runner)
	// Persist last-good per profile next to the store so the node that last
	// connected leads the fallback walk on the next launch. A failure to open it
	// is non-fatal: fall back to the in-memory default rather than refuse to run.
	if lg, err := control.OpenFileLastGood(dir); err != nil {
		log.Printf("tenebra-core: persistent last-good unavailable (%v); using in-memory", err)
	} else {
		daemon.SetLastGood(lg)
	}
	// Persist user routing preferences (the per-app split config) next to the
	// store so a split choice survives a restart. A failure to open it is
	// non-fatal: preferences then live only for this session.
	if st, err := control.OpenFileSettings(dir); err != nil {
		log.Printf("tenebra-core: persistent settings unavailable (%v); using session defaults", err)
	} else {
		daemon.SetSettings(st)
	}
	server := control.NewServer(daemon, os.Stdin, os.Stdout)

	// Cancel on Ctrl-C / SIGTERM so the tunnel is torn down cleanly; Serve also
	// returns on a clean stdin EOF (the UI closing the pipe).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = server.Serve(ctx)
	// A signal-driven shutdown surfaces as context.Canceled; that is a normal
	// exit, not a failure.
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// configDir returns the directory the profile store lives in. It prefers the
// per-user config location; if that can't be determined we fall back to the
// working directory so the core still starts rather than refusing to run.
func configDir() string {
	// An explicit override (used by tests, and by the desktop app when it wants
	// the store in a specific place) wins over the per-user default.
	if dir := os.Getenv("TENEBRA_CONFIG_DIR"); dir != "" {
		log.Printf("tenebra-core: store at %s (TENEBRA_CONFIG_DIR)", dir)
		return dir
	}
	base, err := os.UserConfigDir()
	if err != nil {
		log.Printf("user config dir unavailable (%v); using current directory", err)
		base = "."
	}
	dir := filepath.Join(base, "tenebra")
	log.Printf("tenebra-core: store at %s", dir)
	return dir
}
