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
	"strings"
	"syscall"

	"github.com/Divaaaan/tenebra/core/control"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/zapret"
)

// pipeMode switches the console process from stdin/stdout to the named-pipe
// transport (Windows only): the development way to exercise the service's
// transport without installing a service.
var pipeMode = flag.Bool("pipe", false, "serve the control protocol on the named pipe instead of stdin/stdout (Windows only)")

// socketMode switches the console process from stdin/stdout to the unix-socket
// transport (macOS and Linux): the development way to exercise the privileged
// daemon's transport without installing one, and what that daemon runs with.
var socketMode = flag.Bool("socket", false, "serve the control protocol on a unix domain socket instead of stdin/stdout (macOS and Linux only)")

// fileLogTail reads back the trailing lines of the process log when this run
// writes one to disk — the Windows service sets it to its rotating writer's
// Tail. It stays nil in the console and sidecar modes, whose diagnostics come
// from the daemon's in-memory ring instead, because their stderr belongs to
// whoever launched them.
var fileLogTail func(n int) []string

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

// startBackgroundJobs launches the work the daemon does on its own behalf, for
// as long as ctx lives.
//
// It exists as a named function with two callers because it once had one. The
// Windows service does not go through run() at all — main() hands off to
// maybeRunService before reaching it — so the bundle job ran only for a core
// started from a console. In a service install, which is every ordinary Windows
// install, the DPI bypass therefore never installed itself and never updated:
// the bundle arrived only as a side effect of connecting, and the twelve-hour
// refresh that keeps it ahead of the censor did not run at all. Both entry points
// call this now, and it is the only place the jobs are named.
//
// Keeping the bundle current is the one piece whose value expires: the censor
// learns what a release does, upstream answers with new strategies, and a stale
// bundle fails exactly like a dead node — the user sees YouTube stop loading with
// nothing to point at. The loop ends with ctx and never blocks serving.
func startBackgroundJobs(ctx context.Context, daemon *control.Daemon) {
	go daemon.RunZapretAutoUpdate(ctx)
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

	startBackgroundJobs(ctx, daemon)

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
	// Mirror the daemon's own log into this process's log destination — stderr
	// for the sidecar and console runs, the rotating service.log for the Windows
	// service. Until now those lines went only to an attached UI, which meant the
	// service's whole account of a boot-time autoconnect existed nowhere at all:
	// nobody is looking at the app when the machine starts, and by the time they
	// are, the connect that failed is an hour gone.
	daemon.SetLogSink(func(level, msg string) { log.Printf("%s: %s", level, msg) })
	// Where this run writes its log to a file, let the diagnostics bundle read
	// its own tail back; otherwise the bundle falls back to the in-memory ring.
	daemon.SetLogTail(fileLogTail)
	if lvl := daemon.LogLevel(); lvl != control.DefaultLogLevel {
		log.Printf("tenebra-core: log level %s (%s)", lvl, control.LogLevelEnv)
	}
	// Arm the tun-conflict guard where the platform can read its route table.
	// nil (macOS/Linux for now) leaves the guard disabled rather than guessing —
	// see newInterfaceProbe in the per-platform runner files.
	daemon.SetInterfaceProbe(newInterfaceProbe())
	// A node check runs its own short-lived sing-box beside the tunnel (no tun, no
	// auto_route), so it gets a fresh supervisor per run rather than sharing the
	// tunnel's — a check must never be able to stop the tunnel.
	daemon.SetProbeRunner(func() control.Runner { return newRunner() })
	// Searching the bundle for a working strategy takes minutes: every candidate
	// needs the packet filter attached, five control requests and a clean detach,
	// and while it runs the machine has whichever strategy is being tried — which
	// on a live machine means the bypass flickers on and off for five minutes.
	// Doing that automatically, on a connect the user pressed expecting the tunnel
	// to just come up, costs more than the failure it is meant to repair: the
	// tunnel alone still carries the censored services. The search stays available
	// as the deliberate operation it is (pick_zapret, the app's bypass screen).
	daemon.SetBypassRepick(false)
	// The live bypass-bundle updater, and the bundle compiled into this binary
	// that stands in when it cannot deliver one. They live here, not in the
	// daemon's constructor, so only a real core reaches the release feed or
	// unpacks a packet filter into someone's profile directory.
	daemon.SetZapretUpdater(
		func(ctx context.Context) (zapret.Release, error) { return zapret.LatestRelease(ctx, nil) },
		func(ctx context.Context, dir string, rel zapret.Release) error {
			return zapret.Apply(ctx, nil, dir, rel)
		},
		zapret.InstallEmbedded,
	)
	// Persist last-good per profile next to the store so the node that last
	// connected leads the fallback walk on the next launch. A failure to open it
	// is non-fatal: fall back to the in-memory default rather than refuse to run.
	if lg, err := control.OpenFileLastGood(dir); err != nil {
		log.Printf("tenebra-core: persistent last-good unavailable (%v); using in-memory", err)
	} else {
		daemon.SetLastGood(lg)
	}
	// Persist the bypass strategy per network, next to the store, so a machine
	// that moves between an ISP at home and one at a cafe starts each of them on
	// the strategy measured there rather than on the other's. A failure to open it
	// is non-fatal: the cache then lives only for this session.
	if ns, err := control.OpenFileNetStrategies(dir); err != nil {
		log.Printf("tenebra-core: per-network bypass cache unavailable (%v); using in-memory", err)
	} else {
		daemon.SetNetStrategies(ns)
	}
	// Persist user routing preferences (the per-app split config) next to the
	// store so a split choice survives a restart. A failure to open it is
	// non-fatal: preferences then live only for this session.
	if st, err := control.OpenFileSettings(dir); err != nil {
		log.Printf("tenebra-core: persistent settings unavailable (%v); using session defaults", err)
	} else {
		daemon.SetSettings(st)
	}
	// Point the routing layer at the bundled rule-sets. This is a hint, not a
	// guarantee: routing re-checks each file when it builds a config, so a
	// directory that is emptied by an update later still degrades cleanly instead
	// of handing sing-box a path it cannot open.
	if rsDir := ruleSetDir(); rsDir != "" {
		log.Printf("tenebra-core: loading rule-sets locally from %s", rsDir)
		daemon.SetRuleSetDir(rsDir)
	} else {
		log.Printf("tenebra-core: bundled RU rule-sets not found; smart mode will route like global until they are installed")
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

// ruleSetFiles are the RU geodata binaries that decide which resource directory
// is the rule-set directory. They must match the on-disk names the routing
// package resolves against Options.RuleSetDir and the names fetch-resources.ps1
// writes.
//
// The ad/tracker blocklist is deliberately NOT in this list, even though it ships
// beside them. It is optional in a way the geodata is not — it is referenced only
// when the user turns ad-blocking on — so requiring it here meant one absent file
// disqualified the whole directory and took smart mode's geodata down with it,
// for a feature that was switched off. The routing layer now checks each file at
// the moment it builds a config (see routing.Options.ruleSetFilePresent), so the
// blocklist's absence disables ad-blocking and nothing else.
var ruleSetFiles = []string{"geoip-ru.srs", "geosite-ru.srs"}

// ruleSetAdBlockFile is the optional blocklist. It is not required to pick a
// directory; it is only reported on, so an operator who wonders why the
// ad-blocking toggle does nothing has a line to find.
const ruleSetAdBlockFile = "geosite-ads.srs"

// ruleSetDir returns the directory to load the rule-sets from, or "" when no
// candidate holds the RU geodata. It walks the platform's resource directories in
// order (ruleSetCandidates) and takes the first that holds every required
// rule-set file.
//
// A miss is not fatal and is no longer a fallback either: with no directory,
// smart mode emits no geo rules and routes like global for the session. The old
// behaviour — fetching the .srs from GitHub at connect time — was a fallback only
// on a network that can reach GitHub, and on the networks this client is for it
// meant sing-box waited out a five-second timeout and then exited, so every node
// in the walk failed at launch and the user was told the protocols were blocked.
//
// A miss names everywhere it looked, because the fix is to put the files in one
// of those directories and nothing else in the log says where they belong.
func ruleSetDir() string {
	candidates := ruleSetCandidates()
	for _, dir := range candidates {
		if hasRuleSets(dir) {
			if _, err := os.Stat(filepath.Join(dir, ruleSetAdBlockFile)); err != nil {
				log.Printf("tenebra-core: no %s in %s; ad-blocking will stay inert if switched on", ruleSetAdBlockFile, dir)
			}
			return dir
		}
	}
	if len(candidates) > 0 {
		log.Printf("tenebra-core: no RU rule-set bundle in any of: %s", strings.Join(candidates, ", "))
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
