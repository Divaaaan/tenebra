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

	// newRunner is chosen at build time per platform (runner_darwin.go for macOS,
	// runner_other.go for Windows and the Linux CI build).
	runner := newRunner()
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
	// Load the RU geo rule-sets from disk instead of downloading them from GitHub
	// at every connect, but only if the bundled files are actually present. The
	// remote fallback (left in place when this is empty) blocks sing-box startup
	// for ~10s when raw.githubusercontent.com is throttled, which is the freeze we
	// are eliminating.
	if rsDir := ruleSetDir(); rsDir != "" {
		log.Printf("tenebra-core: loading RU rule-sets locally from %s", rsDir)
		daemon.SetRuleSetDir(rsDir)
	} else {
		log.Printf("tenebra-core: bundled RU rule-sets not found; falling back to remote download")
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

// ruleSetFiles are the bundled RU rule-set binaries expected next to the
// sing-box executable. They must match the on-disk names the routing package
// resolves against Options.RuleSetDir and the names fetch-resources.ps1 writes.
var ruleSetFiles = []string{"geoip-ru.srs", "geosite-ru.srs"}

// ruleSetDir returns the directory to load the RU rule-sets from, or "" to keep
// the remote-download fallback. The resources directory is the one holding the
// sing-box binary (TENEBRA_SINGBOX); the .srs ship alongside it. It returns a
// path only when every required rule-set file is actually present there, so a
// dev build or an incomplete install transparently falls back to remote instead
// of pointing sing-box at a missing path (which would FATAL).
func ruleSetDir() string {
	bin := os.Getenv("TENEBRA_SINGBOX")
	if bin == "" {
		return ""
	}
	dir := filepath.Dir(bin)
	for _, f := range ruleSetFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return ""
		}
	}
	return dir
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
