// Command tenebra-core is the process that owns sing-box and the tunnel. The
// desktop UI drives it with the line-delimited JSON control protocol over one
// of three transports: by default the core is a sidecar speaking on stdin/stdout
// (stdout carries protocol traffic only and every diagnostic goes to stderr);
// on Windows it can instead serve the protocol on a named pipe — as a Windows
// service, or in a console with --pipe — and on macOS and Linux on a unix domain
// socket (the privileged daemon, or a console with --socket), so the tunnel can
// outlive any one UI process. See docs/control-protocol.md for the transports.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/Divaaaan/tenebra/adapters/byedpi"
	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/dpi"
	"github.com/Divaaaan/tenebra/core/profile"
)

// pipeMode switches the console process from stdin/stdout to the named-pipe
// transport (Windows only): the development way to exercise the service's
// transport without installing a service.
var pipeMode = flag.Bool("pipe", false, "serve the control protocol on the named pipe instead of stdin/stdout (Windows only)")

// socketMode switches the console process from stdin/stdout to the unix-socket
// transport (macOS and Linux): the development way to exercise the privileged
// daemon's transport without installing one, and what that daemon runs with.
var socketMode = flag.Bool("socket", false, "serve the control protocol on a unix domain socket instead of stdin/stdout (macOS and Linux only)")

func main() {
	flag.Parse()
	// The service control manager starts us with no console and no usable
	// stdio, so the service path must be detected before anything touches
	// them. Off Windows this is always a no-op.
	if handled, err := maybeRunService(); handled || err != nil {
		if err != nil {
			log.Printf("fatal: %v", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*pipeMode, *socketMode); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

// run is the console entry point: it wires up the core and serves the JSON
// protocol — on stdin/stdout by default, on the named pipe with --pipe, on the
// unix socket with --socket — until EOF or a shutdown signal.
func run(usePipe, useSocket bool) error {
	// stdout is the protocol channel in sidecar mode; keep all logging off it.
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags)

	// The two detached transports are mutually exclusive and platform-specific;
	// reject the combination up front rather than silently pick one.
	if usePipe && useSocket {
		return errors.New("choose one transport: --pipe or --socket, not both")
	}

	// A root daemon serving --socket needs the machine-scoped store and bundled
	// sing-box pinned before buildDaemon reads them — the same ordering the
	// Windows service uses for configureServicePaths. This is a no-op for a
	// non-root --socket dev run and on platforms where serveSocket rejects
	// --socket outright.
	if useSocket {
		if err := configureSocketPaths(); err != nil {
			return err
		}
	}

	daemon, err := buildDaemon()
	if err != nil {
		return err
	}
	// Belt-and-suspenders for the system-proxy guard: Serve already calls
	// daemon.Close() (which clears any armed OS proxy) on a clean or signalled exit,
	// but a defer here also covers the --pipe/--socket paths and any early return,
	// so no exit path leaves the OS pointed at a dead proxy. Close is idempotent.
	defer func() { _ = daemon.Close() }()

	// Cancel on Ctrl-C / SIGTERM so the tunnel is torn down cleanly; serving
	// also ends on a clean stdin EOF (the UI closing the pipe).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch {
	case usePipe:
		err = servePipe(ctx, daemon)
	case useSocket:
		err = serveSocket(ctx, daemon)
	default:
		server := control.NewServer(daemon, os.Stdin, os.Stdout)
		err = server.Serve(ctx)
	}
	// A signal-driven shutdown surfaces as context.Canceled; that is a normal
	// exit, not a failure.
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// buildDaemon opens the profile store and wires a daemon with its persisted
// state — the setup shared by the console (stdio or --pipe) and the Windows
// service entry points.
func buildDaemon() (*control.Daemon, error) {
	dir := configDir()
	store, err := profile.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open profile store: %w", err)
	}

	// newRunner is chosen at build time per platform (runner_darwin.go for macOS,
	// runner_linux.go for Linux, runner_other.go for Windows).
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
	// The DPI bypass engine is optional and ships on Windows and Linux only, so a
	// build without it has to keep running. Attaching it only when the binary is
	// really on disk is what makes the absent case honest: the daemon answers
	// set_dpi_bypass with "no engine installed" instead of arming a preference
	// whose config would point traffic at a loopback port nothing listens on.
	if err := attachDPIEngine(daemon); err != nil {
		log.Printf("tenebra-core: DPI bypass unavailable (%v)", err)
	}
	// System-proxy backstop: clear any OS proxy a previous run left pointing at our
	// loopback mixed inbound (a hard kill can't run the in-process cleanup). It only
	// clears a proxy matching our exact loopback address, never a corporate one, and
	// runs before autoconnect so a stale pointer is gone before a fresh tunnel may
	// re-arm it.
	if cleared, err := daemon.ReconcileSystemProxyAtStartup(); err != nil {
		log.Printf("tenebra-core: system-proxy startup check: %v", err)
	} else if cleared {
		log.Printf("tenebra-core: cleared a stale system proxy left by a previous run")
	}
	// Autoconnect: if the preference is armed and a last connect is recorded,
	// re-issue it now. This is the daemon's own start — shared by the sidecar,
	// the --pipe console and the Windows service — so with the service the
	// tunnel comes up with the machine, before anyone logs in or a UI attaches.
	// The attempt runs in the background and never delays the control plane; a
	// client connecting mid-attempt simply sees the connecting state.
	if daemon.AutoconnectOnStart() {
		log.Printf("tenebra-core: autoconnect: reconnecting the last profile")
	}
	return daemon, nil
}

// attachDPIEngine hands the daemon a bypass engine when one is installed. The
// port is picked here rather than inside the daemon because the generated
// sing-box config has to name the same port the engine binds, and the config is
// built long before the engine starts.
//
// Not finding the binary is a normal outcome, not a failure: macOS ships no
// upstream build, and a dev checkout may simply not have fetched it.
func attachDPIEngine(d *control.Daemon) error {
	bin, err := findBundledDPIEngine()
	if err != nil {
		return err
	}
	// Publish the resolved path the same way the service publishes the sing-box
	// one. The runner resolves the binary through dpi.ResolveBinary at spawn
	// time, and that only knows the environment override and the directory
	// holding the executable — in an installed layout the engine sits in
	// resources\ instead, so without this the spawn would fail at the moment the
	// user arms the toggle.
	if err := os.Setenv(dpi.BinaryEnv, bin); err != nil {
		return fmt.Errorf("publish engine path: %w", err)
	}
	port := dpi.FreePort()
	// DefaultStrategies[0] is the upstream-recommended preset and the only one
	// the UI can currently arm; per-strategy selection is a later step, and this
	// is the one place that would change when it lands.
	args, err := dpi.BuildArgs(dpi.LoopbackHost, port, dpi.DefaultStrategies[0].Args)
	if err != nil {
		return fmt.Errorf("render engine arguments: %w", err)
	}
	d.SetDPIRunner(byedpi.New(), port, args)
	log.Printf("tenebra-core: DPI bypass engine %s ready on loopback port %d", bin, port)
	return nil
}

// findBundledDPIEngine resolves the ByeDPI binary across the layouts Tenebra is
// installed in, mirroring how the sing-box binary is located: the environment
// override wins, then the bundle's resources directory beside the executable,
// then a flat layout, and on Linux the package path the systemd unit runs from.
// Returns an error naming the paths tried, so an install that shipped without
// the engine says so in the log rather than failing later at connect time.
func findBundledDPIEngine() (string, error) {
	if p := os.Getenv(dpi.BinaryEnv); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s points at %s: %w", dpi.BinaryEnv, p, err)
		}
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	dir := filepath.Dir(exe)
	name := dpi.BinaryName()
	candidates := []string{
		filepath.Join(dir, "resources", name),
		filepath.Join(dir, name),
	}
	if runtime.GOOS == "linux" {
		candidates = append(candidates, filepath.Join("/usr/lib/tenebra", name))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("engine not found in %s", strings.Join(candidates, ", "))
}

// ruleSetFiles are the bundled rule-set binaries expected next to the sing-box
// executable: the two RU geodata sets plus the ad/tracker blocklist. They must
// match the on-disk names the routing package resolves against Options.RuleSetDir
// and the names fetch-resources.ps1 writes. Requiring all three keeps the local
// path an all-or-nothing guarantee — the ad blocklist is referenced only as a
// local set, so a config that turns ad-blocking on must never point sing-box at a
// missing path. A build shipping the bundle always carries all three (they are
// declared resources; the bundle step fails without them), so this never forces
// the RU sets back to the remote fallback in a real install.
var ruleSetFiles = []string{"geoip-ru.srs", "geosite-ru.srs", "geosite-ads.srs"}

// ruleSetDir returns the directory to load the RU rule-sets from, or "" to keep
// the remote-download fallback. It walks the platform's resource directories in
// order (ruleSetCandidates) and takes the first that holds every required
// rule-set file, so a dev build or an incomplete install transparently falls
// back to remote instead of pointing sing-box at a missing path (which would
// FATAL). A miss names everywhere it looked: the alternative — one line saying
// only that connects will now stall on a download — leaves the operator with
// nowhere to put the files.
func ruleSetDir() string {
	candidates := ruleSetCandidates()
	for _, dir := range candidates {
		if hasRuleSets(dir) {
			return dir
		}
	}
	if len(candidates) > 0 {
		log.Printf("tenebra-core: no complete RU rule-set bundle in any of: %s", strings.Join(candidates, ", "))
	}
	return ""
}

// hasRuleSets reports whether dir holds every file in ruleSetFiles. The empty
// directory is never a hit, so a candidate list carrying one (an unset
// TENEBRA_SINGBOX) cannot resolve to the process's working directory.
func hasRuleSets(dir string) bool {
	if dir == "" {
		return false
	}
	for _, f := range ruleSetFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return false
		}
	}
	return true
}

// configDir returns the directory the profile store lives in. It prefers the
// per-user config location; if that can't be determined we fall back to the
// working directory so the core still starts rather than refusing to run.
func configDir() string {
	// An explicit override wins over the per-user default. It is set by tests, by
	// the Windows service path (which pins a machine-scoped store, see
	// configureServicePaths), and by an operator who wants the store elsewhere.
	// The desktop shell does NOT set it — the sidecar is handed only
	// TENEBRA_SINGBOX — so a normal desktop run falls through to the per-user
	// location below.
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
