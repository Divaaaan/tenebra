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
	"github.com/Divaaaan/tenebra/core/state"
	"github.com/Divaaaan/tenebra/core/zapret"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

// pipeMode switches the console process from stdin/stdout to the named-pipe
// transport (Windows only): the development way to exercise the service's
// transport without installing a service.
var pipeMode = flag.Bool("pipe", false, "serve the control protocol on the named pipe instead of stdin/stdout (Windows only)")

// socketMode switches the console process from stdin/stdout to the unix-socket
// transport (macOS and Linux): the development way to exercise the privileged
// daemon's transport without installing one, and what that daemon runs with.
var socketMode = flag.Bool("socket", false, "serve the control protocol on a unix domain socket instead of stdin/stdout (macOS and Linux only)")

var releaseHostProtection = flag.Bool("release-host-protection", false, "explicitly remove only Tenebra-owned persistent host protection (administrator recovery/uninstall)")

// fileLogTail reads back the trailing lines of the process log when this run
// writes one to disk — the Windows service sets it to its rotating writer's
// Tail. It stays nil in the console and sidecar modes, whose diagnostics come
// from the daemon's in-memory ring instead, because their stderr belongs to
// whoever launched them.
var fileLogTail func(n int) []string

func main() {
	if handled, err := control.RunUserProxyHelper(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	flag.Parse()
	if *releaseHostProtection {
		if err := releaseNativeHostProtection(); err != nil {
			log.Printf("host protection cleanup: %v", err)
			os.Exit(1)
		}
		return
	}
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
	authority, err := selectRunAuthority(useSocket, unixServiceRoot(), *maintenanceTx, *maintenanceLaunch)
	if err != nil {
		return err
	}
	if useSocket && !authority.managed {
		if err := configureSocketPaths(); err != nil {
			return err
		}
	}

	open := openDevelopmentFilesystem
	if authority.managed {
		open = func(ctx context.Context) (daemonFilesystem, error) { return openManagedFilesystem(ctx, authority) }
	}
	bootstrap := daemonBootstrap{Open: open, Build: buildDaemonWithFilesystem}
	daemon, filesystem, err := bootstrap.Prepare(context.Background())
	if err != nil {
		return err
	}
	defer filesystem.Close()
	if filesystem.managed == nil {
		startProductionConnection(daemon)
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

	if filesystem.managed != nil {
		closePrivate, err := startManagedPrivate(ctx, daemon, &filesystem)
		if err != nil {
			return err
		}
		defer closePrivate()
	}

	startBackgroundJobs(ctx, daemon)

	switch {
	case usePipe:
		err = servePipe(ctx, daemon)
	case useSocket:
		if filesystem.managed != nil {
			err = serveManagedSocket(ctx, daemon, filesystem.managed.paths.BusinessSocket)
		} else {
			err = serveSocket(ctx, daemon)
		}
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

// Both entry points retain the verified roots for the full daemon lifetime.
// Canonical state readers are composed here; nil roots never select legacy IO.
func buildDaemonWithFilesystem(filesystem *daemonFilesystem) (*control.Daemon, error) {
	if filesystem == nil || filesystem.State == nil || filesystem.Private == nil {
		return nil, securefs.ErrUntrusted
	}
	backup, err := filesystem.Private.OpenDir("state-backups", filesystem.privatePolicy, true)
	if err != nil {
		return nil, err
	}
	filesystem.closers = append(filesystem.closers, backup)
	store, err := state.Open(context.Background(), filesystem.State, backup)
	if err != nil {
		return nil, fmt.Errorf("open profile store: %w", err)
	}
	filesystem.closers = append(filesystem.closers, store)
	filesystem.canonical = store

	// newRunner is chosen at build time per platform (runner_darwin.go for macOS,
	// runner_linux.go for Linux, runner_other.go for Windows).
	runner := newRunner()
	if filesystem.managed != nil {
		path, err := filesystem.managed.runtimeRoot.Path()
		if err != nil {
			return nil, err
		}
		runner = newManagedRunner(filepath.Join(path, "sing-box"))
	}
	if runner == nil {
		return nil, securefs.ErrUnsupported
	}
	runtimeRoot, err := filesystem.Private.OpenDir("engine-runtime", filesystem.privatePolicy, true)
	if err != nil {
		return nil, err
	}
	filesystem.closers = append(filesystem.closers, runtimeRoot)
	if capability, ok := runner.(control.MaintenanceRunner); ok {
		if err := capability.SetRuntimeRoot(runtimeRoot); err != nil {
			return nil, err
		}
	}
	daemon := control.NewDaemon(store.Profiles(), runner)
	if err := daemon.SetCanonicalState(store); err != nil {
		return nil, err
	}
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
	daemon.SetProbeRunner(func() control.Runner {
		probe := newRunner()
		if filesystem.managed != nil {
			path, err := filesystem.managed.runtimeRoot.Path()
			if err == nil {
				probe = newManagedRunner(filepath.Join(path, "sing-box"))
			} else {
				probe = newManagedRunner("")
			}
		}
		if capability, ok := probe.(control.MaintenanceRunner); ok {
			if err := capability.SetRuntimeRoot(runtimeRoot); err != nil {
				// A configured host records this failure and refuses Start; it
				// cannot fall back to an ambient temporary configuration.
				log.Print("tenebra-core: private probe runtime is unavailable")
			}
		}
		return probe
	})
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
	// Measurements remain a separate cache, but trust/read errors never become
	// a silent in-memory fallback. Preferences and last-good have one Store.
	ns, err := control.OpenRootNetStrategies(filesystem.State, func(error) { log.Print("tenebra-core: network strategy cache persistence failed") })
	if err != nil {
		return nil, err
	}
	daemon.SetNetStrategies(ns)
	// Point the routing layer at the bundled rule-sets. This is a hint, not a
	// guarantee: routing re-checks each file when it builds a config, so a
	// directory that is emptied by an update later still degrades cleanly instead
	// of handing sing-box a path it cannot open.
	rsDir := ""
	if filesystem.managed != nil {
		rsDir, err = filesystem.managed.runtimeRoot.Path()
		if err != nil {
			return nil, err
		}
	} else {
		rsDir = ruleSetDir()
	}
	if rsDir != "" {
		log.Printf("tenebra-core: loading rule-sets locally from %s", rsDir)
		daemon.SetRuleSetDir(rsDir)
	} else {
		log.Printf("tenebra-core: bundled RU rule-sets not found; smart mode will route like global until they are installed")
	}
	if filesystem.Development {
		if err := store.Admit(); err != nil {
			return nil, err
		}
	}
	return daemon, nil
}

// Kept outside buildDaemon so ordinary constructor/unit fixtures never apply
// native policy or change the process resolver. Recovery precedes autoconnect.
func startProductionConnection(daemon *control.Daemon) {
	startProductionConnectionWithIntent(daemon, true)
}
func startProductionConnectionWithIntent(daemon *control.Daemon, autoconnect bool) {
	if err := daemon.CanonicalReady(); err != nil {
		log.Print("tenebra-core: business admission remains closed")
		return
	}
	if cleared, err := daemon.ReconcileSystemProxyAtStartup(); err != nil {
		log.Print("tenebra-core: system-proxy startup check failed")
	} else if cleared {
		log.Print("tenebra-core: cleared a stale system proxy left by a previous run")
	}
	configureHostProtection(daemon)
	// Sidecar, --pipe console and service share this one startup attempt. It
	// runs in the background; clients attaching during it see connecting.
	if autoconnect && daemon.AutoconnectOnStart() {
		log.Printf("tenebra-core: autoconnect: reconnecting the last profile")
	}
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
