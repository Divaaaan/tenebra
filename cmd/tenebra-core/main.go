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

	"github.com/tenebra-vpn/tenebra/adapters/windows"
	"github.com/tenebra-vpn/tenebra/core/control"
	"github.com/tenebra-vpn/tenebra/core/profile"
)

func main() {
	if err := run(); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run() error {
	// stdout is the protocol channel; keep all logging off it.
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags)

	store, err := profile.Open(configDir())
	if err != nil {
		return fmt.Errorf("open profile store: %w", err)
	}

	runner := windows.New()
	daemon := control.NewDaemon(store, runner)
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
	base, err := os.UserConfigDir()
	if err != nil {
		log.Printf("user config dir unavailable (%v); using current directory", err)
		base = "."
	}
	dir := filepath.Join(base, "tenebra")
	log.Printf("tenebra-core: store at %s", dir)
	return dir
}
