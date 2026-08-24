package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Divaaaan/tenebra/core/buildinfo"
	"github.com/Divaaaan/tenebra/core/fallback"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodecheck"
	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/core/subscription"
	"github.com/Divaaaan/tenebra/core/tunguard"
	"github.com/Divaaaan/tenebra/core/zapret"
)

// Runner owns one sing-box process. It is defined here, not in an adapter
// package, so control depends on nothing OS-specific and the dependency arrow
// points adapter -> control. The daemon starts a config, polls byte counters,
// learns of an exit through Done, and stops the process.
//
// A Runner is single-use per Start/Stop cycle but reusable across cycles: after
// Stop (or a Done signal) a fresh Start must work. Implementations must be safe
// for concurrent calls to Stats/Done alongside Start/Stop, since the traffic
// poller and process watcher run in their own goroutines.
type Runner interface {
	// Start launches sing-box with the given config JSON. It returns once the
	// process has been spawned (not once the tunnel is up); a later failure is
	// reported through Done.
	Start(ctx context.Context, configJSON []byte) error
	// Stop terminates the process and waits for it to exit. It is idempotent.
	Stop() error
	// Stats returns cumulative upload/download byte counts from the clash API.
	// An error (e.g. the API not yet listening) is non-fatal to the daemon.
	Stats() (up, down int64, err error)
	// Probe runs a clash API delay test through the outbound named tag, returning
	// the measured round-trip in milliseconds. A nil error means traffic actually
	// flows through that outbound; any error (blocked protocol, dead upstream, API
	// not yet listening, ctx cancelled) means it does not. ctx bounds the call.
	Probe(ctx context.Context, tag string) (delayMs int, err error)
	// ProbeVia is Probe against a caller-chosen destination, through any outbound
	// in the running config rather than only the selector. It is how a candidate
	// exit is judged before the tunnel is moved onto it: several unalike targets
	// through the candidate's own outbound, measured by the process that is
	// already running, so nothing is torn down to find out whether somewhere else
	// works (see core/nodecheck for why one target is not enough).
	ProbeVia(ctx context.Context, tag, target string) (delayMs int, err error)
	// Select points the running process's selector group at the outbound named
	// tag. A nil error means the live tunnel now egresses through that node with
	// no restart: same process, same tun, same routes. Any error means the switch
	// did not take and the caller must fall back to a reconnect rather than
	// report a move that did not happen.
	Select(ctx context.Context, group, tag string) error
	// Done delivers the process's exit: it sends the exit error (nil on a clean
	// exit) once and is closed afterwards. Before any Start it must block.
	Done() <-chan error
	// Logs returns a copy of the most recent sing-box output lines (newest last) —
	// the in-memory tail each runner keeps for diagnostics. It is safe to call at
	// any time (before a Start, after a Stop, or while a process runs) and returns
	// an empty slice when nothing has been captured. The daemon surfaces this tail
	// when a connect gives up, so a sing-box failure is visible in the UI log
	// rather than swallowed.
	Logs() []string
}

// pingDialTimeout bounds each per-node TCP latency probe.
const pingDialTimeout = 3 * time.Second

// emitFunc is how the daemon pushes asynchronous events out to the writer. The
// server installs one; when nil (a bare daemon in a unit test) events are
// dropped.
type emitFunc func(name string, body any)

// defaultClientWriteTimeout is how long one outbound frame may take to reach a
// client before that client is given up on (see Server.writeTimeout). It is
// deliberately long: the daemon no longer waits on the client for anything, so
// the only thing this decides is when a stream counts as dead, and a UI is
// allowed to be busy for a while. It has to be finite all the same — the
// Windows control pipe buffers nothing, so a UI whose reader has stalled leaves
// a write outstanding forever, and something has to end that.
const defaultClientWriteTimeout = 30 * time.Second

// Daemon holds all mutable connection state and implements every protocol
// command. It is driven by a Server (one request at a time) but its connection
// lifecycle also spawns goroutines (traffic poll, process watch) that mutate
// state, so every field touched from more than one goroutine is guarded by mu.
type Daemon struct {
	store  *profile.Store
	runner Runner

	// proxy applies and clears the OS-wide system proxy for ModeSystemProxy. It is
	// set once at construction (realSystemProxy in production, a fake in tests) and
	// then only read, so it needs no lock; the armed bit it toggles (proxyArmed) is
	// guarded by mu.
	proxy systemProxyController

	// ifaceProbe enumerates the machine's interfaces and their default routes for
	// the tun-conflict guard (see tunguard). It is injected by the platform layer
	// — the core stays stdlib-only and cannot read a route table itself — and is
	// set once at construction, then only read, so it needs no lock.
	//
	// nil disables the guard, which is the correct default for a platform with no
	// implementation yet and for unit tests that never raise a real tun: refusing
	// to connect because we cannot see the routes would block the very platforms
	// the check has not been ported to.
	ifaceProbe func() ([]tunguard.Iface, error)

	// zapretActive is the DPI-bypass strategy the user settled on, or empty. It is
	// what a plain start_zapret re-launches, so the picked strategy survives a
	// stop/start — and a restart, since it is persisted — without them naming it
	// again. It records the CHOICE, not the running state: whether the bypass is
	// up lives in routing.ZapretActive, which is what routing reads. Guarded by mu.

	// zapretAutoUpdate arms the background bundle updater (see
	// RunZapretAutoUpdate). On by default: the bypass is the one component that
	// expires — the censor learns the tricks a release shipped with — and a stale
	// bundle fails exactly like a broken VPN, so leaving it to the user to notice
	// is leaving them a symptom with no visible cause. Guarded by mu.
	zapretAutoUpdate bool

	// zapretLatest and zapretApply are the bundle-update mechanism, injected so a
	// test can exercise the update path without reaching GitHub or writing a
	// megabyte of archive. Set once at construction, then only read.
	zapretLatest func(context.Context) (zapret.Release, error)
	zapretApply  func(context.Context, string, zapret.Release) error

	// zapretExclude writes the node addresses the packet filter must leave alone
	// and reports which nodes it could not cover. Injected for the same reason as
	// the pair above: the real one performs DNS lookups, and a test of what the
	// daemon does with the answer should not depend on a resolver. The real one
	// is a method on a long-lived Excluder, which is what remembers the addresses
	// across connects.
	zapretExclude func(dir string, servers []string, lookups []zapret.Lookup) (zapret.ExcludeReport, error)

	// zapretOpMu serializes every operation that drives the bypass: probing,
	// starting, stopping and updating. They share one winws process and one
	// directory on disk, and overlapping them replaces a bundle under a running
	// filter. It is separate from mu, which must never be held across the minutes
	// a probe run takes.
	zapretOpMu sync.Mutex

	mu           sync.Mutex
	zapretActive string
	routing      routing.Options
	state        State
	tun          singbox.TunOptions
	// proxyArmed records whether the daemon currently has the OS system proxy
	// pointed at our mixed inbound, so disarmSystemProxy clears it exactly once and
	// never touches a proxy we didn't set. Guarded by mu.
	proxyArmed bool

	// emit is set by the server via SetEmitter before serving; the daemon calls
	// it to publish state/traffic/log events. Guarded by mu.
	emit emitFunc

	// logLevel is the threshold below which log lines are dropped (see
	// logging.go). Seeded from TENEBRA_LOG_LEVEL, default info. Guarded by mu.
	logLevel string
	// logSink is the second destination for log lines that pass the threshold —
	// the process logger, which in service mode is the rotating file. nil in
	// tests and in any mode that has not wired one. Guarded by mu.
	logSink func(level, msg string)
	// logTail reads the trailing lines of the process log file for the
	// diagnostics bundle; nil where the process logs to stderr rather than a
	// file. Guarded by mu.
	logTail func(n int) []string
	// logs retains the most recent log lines so a diagnostics bundle can be
	// assembled from a daemon no UI was ever attached to. It carries its own
	// lock, so emitLog never holds mu across the append.
	logs *logRing

	// clientWriteTimeout is handed to every Server built over this daemon (see
	// defaultClientWriteTimeout). Set before serving — tests shrink it to exercise
	// the drop of a stalled client without waiting out the production value.
	clientWriteTimeout time.Duration

	// attempts holds the latest fallback-walk snapshot (the body of the last
	// attempts event), or nil when no walk is in flight. The connect loop keeps it
	// current so a client that attaches mid-walk can be handed the live picture on
	// its status re-sync; teardown clears it when a walk ends or is superseded.
	// Guarded by mu.
	attempts *attemptsEvent

	// live describes the sing-box config the running process was started with: the
	// selector group it exposes and which node each of its members is. It is what a
	// live exit change is decided against — the running process, not a profile that
	// may have been refreshed since it started. nil while nothing is connected;
	// teardown clears it. Guarded by mu.
	live *liveConfig

	// autoSwitches are the times the watchdog last moved the exit on its own, and
	// degradedAt when each node last ran out of health probes. Together they are the
	// hysteresis: a node that just failed is not switched back to, and a field of
	// exits that all keep failing stops being churned (see hotswitch.go). Guarded by
	// mu.
	autoSwitches []time.Time
	degradedAt   map[string]time.Time

	// lastGood remembers the node that last connected per profile, feeding node
	// selection on a connect without an explicit node.
	lastGood fallback.LastGood

	// settings persists user routing preferences (the split config) so they
	// survive a restart. nil means no persistence (a bare daemon in a unit test):
	// preferences then live only for the session. Guarded by mu for writes via
	// persistSettings, but the store is itself concurrency-safe.
	settings settingsStore

	// autoconnect remembers whether the daemon reconnects the last profile when
	// it starts (see AutoconnectOnStart). Persisted with the other preferences.
	// Guarded by mu.
	autoconnect bool
	// autoFailover remembers whether the health watchdog is armed (see health.go).
	// It defaults on and is re-read on every watchdog tick, so a mid-session toggle
	// takes effect without a reconnect. Persisted with the other preferences.
	// Guarded by mu.
	autoFailover bool
	// multihop remembers the two-hop chain selection (enabled plus the entry/exit
	// server IDs). It is held here rather than on d.routing because the routing
	// options the builder consumes carry resolved outbound tags, not server IDs; the
	// IDs are resolved against the connecting profile via resolveMultihop at connect
	// time. Persisted with the other preferences. Guarded by mu.
	multihop model.Multihop
	// crashReports remembers the crash-report consent (see State.CrashReports):
	// nil when the user has not been asked, else a pointer to their explicit
	// choice. It governs only whether the GUI offers to surface a crash report on
	// the next launch; the core never sends anything anywhere. Persisted with the
	// other preferences. Guarded by mu.
	crashReports *bool
	// lastProfile and lastNode record the last successful user-commanded
	// connect: the profile, and the node only when the request pinned an
	// explicit exit. They are what autoconnect re-issues at the next daemon
	// start. Off-command connects — the kill-switch relaunch, the options
	// reconcile, autoconnect itself — re-issue this intent rather than rewrite
	// it: a relaunch pins the node it was on, and recording that would silently
	// turn a "whole profile" intent into an explicit-node one. Guarded by mu.
	lastProfile string
	lastNode    string

	// cancel tears down the current connection's goroutines; nil when idle.
	cancel context.CancelFunc
	// watching guards against double-starting the lifecycle goroutines and lets
	// disconnect wait for them to drain.
	wg sync.WaitGroup
	// connMu serializes connection transitions (teardown + the start that follows)
	// so the two connects that run off the serialized command loop — the
	// kill-switch relaunch and the connecting-window options reconcile — cannot
	// interleave with a user connect/disconnect/Close. Held around startConnect and
	// teardown by every such path; never held while waiting on d.wg's goroutines
	// (they don't take it), so it introduces no deadlock. Always acquired before mu.
	connMu sync.Mutex
	// relaunchWG tracks in-flight off-command connect goroutines (relaunch /
	// reconcile) separately from d.wg, so Close can wait for them to unwind without
	// the self-deadlock that tracking them in d.wg would cause (their startConnect
	// waits on d.wg).
	relaunchWG sync.WaitGroup
	// generation increments on every connect so a stale watcher from a previous
	// connection can tell it has been superseded and stay quiet.
	generation uint64
	// relaunches counts kill-switch relaunches of a tunnel that keeps dying within
	// relaunchResetAfter of coming up (a crash-loop), so it can't churn forever.
	// Reset by an explicit connect or disconnect, and refunded when a relaunched
	// tunnel stays up past that window — so isolated drops over a long session
	// don't accumulate toward the cap. Guarded by mu.
	relaunches int
	// relaunchResetAfter is the uptime past which a relaunched tunnel's later death
	// refunds the relaunch budget rather than spending it. Injectable for tests;
	// defaults to defaultRelaunchReset.
	relaunchResetAfter time.Duration
	// beforeReconnect, when set, is called at the entry of an off-command connect
	// goroutine (relaunch / reconcile) before it claims connMu. It is a test seam
	// for driving the interleaving with a concurrent disconnect/Close; nil in
	// production.
	beforeReconnect func()

	// now and dial are injectable for tests; production uses the real clock and
	// dialer.
	now  func() time.Time
	dial func(ctx context.Context, network, address string) (net.Conn, error)

	// classify decides why a failed connect attempt failed, so the fallback loop
	// can escalate a transport strategy on a node whose handshake looks interfered
	// with but keep advancing past a dead one. Injectable so the escalation is
	// unit-testable without a live network; production uses classifyAttemptFailure
	// (a direct TCP probe of the entry plus the through-tunnel stall signal).
	classify func(ctx context.Context, node model.Node, stalled bool) fallback.FailureClass

	// fallback-loop timings, injectable so tests run fast and deterministically.
	// probeWarmup is how long to wait after Start before the first probe (the
	// clash API needs a moment to listen). probeRetry is the gap between probe
	// retries within one attempt's budget. probeTimeout bounds a single probe.
	// probeBudget caps the total time spent probing one candidate before it is
	// declared failed.
	probeWarmup  time.Duration
	probeRetry   time.Duration
	probeTimeout time.Duration
	probeBudget  time.Duration

	// health-watchdog timings and seam, injectable so tests drive the watchdog
	// fast and offline. healthInterval is the gap between probes of the active node
	// while connected (<=0 disables the watchdog); healthProbeTimeout bounds one
	// probe; healthFailThreshold is how many consecutive failures trip a failover.
	// healthProbe runs one probe, a nil error meaning the tunnel still carries
	// traffic; production wires defaultHealthProbe (a clash-API delay test through
	// the active outbound), tests inject a scripted verdict like d.classify.
	healthInterval      time.Duration
	healthProbeTimeout  time.Duration
	healthFailThreshold int
	healthProbe         func(ctx context.Context) error

	// live-switch timings, injectable so tests drive them without waiting out the
	// production values. switchVerifyBudget caps the confirmation that traffic
	// really flows through a newly selected exit; switchProbeTimeout bounds one
	// call inside it. autoSwitchCooldown is the minimum gap between two automatic
	// exit changes, autoSwitchWindow the span over which maxAutoSwitches of them
	// are allowed, and degradedRetryAfter how long a node that ran out of health
	// probes is passed over. switchScanLimit caps how many candidates one
	// degradation scan measures.
	switchProbeTimeout time.Duration
	switchVerifyBudget time.Duration
	autoSwitchCooldown time.Duration
	autoSwitchWindow   time.Duration
	maxAutoSwitches    int
	degradedRetryAfter time.Duration
	switchScanLimit    int

	// fetch retrieves a subscription body. It is injectable so the auto-refresh
	// logic can be unit-tested offline; production uses subscription.Fetch.
	fetch func(ctx context.Context, url string) ([]byte, http.Header, error)

	// authorizeConn decides whether a peer accepted on the control channel may
	// drive the daemon, and whether it holds the daemon's own authority (see
	// peer_auth.go). Production wires the platform check, which reads credentials
	// the kernel attached to the connection; it is a field so the listener tests,
	// whose in-memory conns carry no OS credentials at all and are refused by
	// that check, can state the verdict they mean to exercise.
	authorizeConn func(net.Conn) (allowed, privileged bool)

	// httpGet performs a plain HTTP GET for the leak check's IP and DNS echo
	// requests. It is a separate, header-less getter from fetch so leak_check can
	// be unit-tested offline with canned responses; production uses
	// defaultHTTPGet. ipEchoes and dnsEchoURL are likewise injectable so a test
	// can point the check at fake URLs.
	httpGet    func(ctx context.Context, url string) ([]byte, error)
	ipEchoes   []ipEcho
	dnsEchoURL string

	// stunProbe runs the UDP/STUN reachability + NAT probe for run_stun_check, and
	// stunServers are the endpoints it queries. speedStream opens the download for
	// run_speed_test, speedURLs are those endpoints (tried in order), and speedSampleBytes
	// caps how much of it the sample reads. All are injectable so the STUN parsing,
	// NAT classification and throughput math can be unit-tested offline; production
	// uses realStunProbe / defaultStunServers and defaultSpeedStream.
	stunProbe        stunProbeFunc
	stunServers      []string
	speedStream      func(ctx context.Context, url string) (io.ReadCloser, error)
	speedURLs        []string
	speedSampleBytes int64

	// probeRunner mints a sing-box supervisor for a node-check run: a second,
	// short-lived process beside the live tunnel, running a config with no tun and
	// no auto_route (see singbox.BuildProbe). It is a factory rather than a shared
	// runner so a check can never stop the tunnel by reusing its supervisor. nil
	// means the platform did not wire one, and check_nodes says so rather than
	// pretending; tests inject a fake.
	probeRunner func() Runner
	// checkTargets are the destinations a node verdict is measured against, and
	// checkBasePort where the per-node probe listeners start. Both are fields so a
	// test can shrink the target list and move off the production ports.
	checkTargets  []string
	checkBasePort int
	// checkProbe runs one control request through a probe listener and reports how
	// far it got. Injectable so the ranking and reporting can be tested without a
	// network or a sing-box.
	checkProbe func(ctx context.Context, port int, target string) (nodecheck.Stage, int64)
	// checkBudget bounds a whole check run (see defaultCheckBudget). A field so a
	// test can shrink it to milliseconds instead of waiting one out.
	checkBudget time.Duration
	// checkRunning is the single-flight guard for that run. The probe process
	// binds a fixed range of loopback ports, so two overlapping runs would fight
	// over them; it is an atomic rather than a mu-guarded flag because the check
	// is served off the request loop and must not queue behind whatever holds mu.
	checkRunning atomic.Bool

	// localAddrs reports the machine's interface addresses, so a connect can pick
	// a tun address nothing else holds (see pickFreeTunAddress). Injectable so a
	// test can describe a machine where the default is already taken.
	localAddrs func() []net.Addr

	// ifacePresent reports whether a network interface exists, and tunWatchInterval
	// how often a live tunnel's own interface is checked for still being there. The
	// daemon used to watch only the sing-box process, which kept it reporting a
	// healthy connection over an adapter that had vanished underneath it.
	ifacePresent     func(name string) bool
	tunWatchInterval time.Duration

	// bypassProbe reports whether the bypass is carrying video on the direct path.
	// Injectable because the real one completes a TLS handshake, which a unit test
	// cannot fake through a socket.
	bypassProbe func(ctx context.Context) bool

	// bypassRepick arms the strategy search that runs when the bypass turns out
	// not to be carrying anything. Off by default so no test spends minutes
	// probing twenty strategies; main arms it for the real core.
	bypassRepick bool

	// bypassVerifyDelay is how long after a connect the bypass is checked for
	// actually carrying video (see verifyBypass). A field so a test can shorten it,
	// and zero disables the check entirely — which is what every test that is not
	// about it wants, since otherwise each connect leaves a timer running.
	bypassVerifyDelay time.Duration

	// entitlement resolves a managed subscription's entitlement for one key,
	// against the subscription's own origin. Injectable so the import/refresh
	// paths can be unit-tested offline; production uses subscription.FetchEntitlement.
	entitlement func(ctx context.Context, origin, key string) (subscription.Entitlement, error)
	// entCtx bounds every background entitlement lookup's lifetime and entCancel
	// cuts them short on Close, so a slow endpoint can't block shutdown. entWG
	// tracks the in-flight lookups so Close waits for them to unwind. The lookups
	// only ever read the store and re-emit the profile list, never touch the
	// tunnel, so they are kept off d.wg (whose Wait a disconnect blocks on).
	entCtx    context.Context
	entCancel context.CancelFunc
	entWG     sync.WaitGroup
}

// NewDaemon builds a Daemon over a profile store and runner. Routing defaults to
// smart with normalized DNS; the tun options default to the singbox defaults,
// with the stack pinned explicitly so the reported state always names it.
func NewDaemon(store *profile.Store, runner Runner) *Daemon {
	d := &Daemon{
		store:  store,
		runner: runner,
		proxy:  realSystemProxy{},
		// Only UnblockServices ships on, and the split is by direction rather than
		// by convenience. It pins censored domains *to* the tunnel ahead of the geo
		// rule, which is what stops YouTube from being sent direct because
		// googlevideo resolves to an ISP cache node — it adds nothing to what
		// leaves the tunnel, so a default-on carries no cost the user did not ask
		// for.
		//
		// GamesDirect and VoiceDirect go the other way: each takes a whole class of
		// traffic out of the tunnel. VoiceDirect is all UDP above port 50000, which
		// on a normal desktop is browser WebRTC calls and torrents, so on by default
		// means a fresh install shows "connected" while handing the real address to
		// whoever is on the other end of the call. They are worth having — the
		// latency measured here was 239ms tunnelled against 9ms direct — but that is
		// a trade with a privacy side, and a trade belongs to the user. Both are one
		// switch away in Settings.
		routing: routing.Options{
			Mode:            routing.ModeSmart,
			UnblockServices: true,
		}.Normalize(),
		// Bundle updates default on for the same reason the presets do: a bypass
		// that is a few releases behind is not slower, it stops working, and the
		// failure is indistinguishable from a dead node.
		zapretAutoUpdate: true,
		// Deliberately NOT the live release feed. Fetching a bundle is the one
		// daemon behaviour that reaches the internet on its own, and wiring it here
		// made every test that connects download one into its temp directory — three
		// of them did exactly that, at fifteen seconds each, and one got far enough
		// to have Windows pop a dialog about a winws.exe whose directory the test had
		// already deleted. main installs the real pair (SetZapretUpdater); anything
		// that has not asked for it gets an updater that politely does nothing.
		zapretLatest: func(context.Context) (zapret.Release, error) {
			return zapret.Release{}, errors.New("zapret: updater not configured")
		},
		zapretApply: func(context.Context, string, zapret.Release) error {
			return errors.New("zapret: updater not configured")
		},
		zapretExclude: (&zapret.Excluder{}).Exclude,
		// CacheDir pins sing-box's cache file to the writable store directory so
		// the root launchd daemon (cwd "/", read-only) doesn't abort at startup;
		// see singbox.TunOptions.CacheDir. Mode/MixedPort are seeded concrete (tun,
		// default port) so every snapshot reports a definite connection mode.
		tun: singbox.TunOptions{
			Mode:      singbox.ModeTun,
			MixedPort: singbox.DefaultMixedPort,
			Stack:     singbox.StackSystem,
			CacheDir:  store.Dir(),
		},
		state: State{
			State: StateIdle,
			// Stamped from construction so a status that races the first setState
			// still carries the daemon's build version (see applySettingsToState).
			DaemonVersion: buildinfo.Version,
			Routing:       string(routing.ModeSmart),
			TunStack:      singbox.StackSystem,
			ProxyMode:     singbox.ModeTun,
			ProxyPort:     singbox.DefaultMixedPort,
			// Report the effective resolvers from the moment the daemon exists, so a
			// status before SetSettings still lets the UI prefill the custom-DNS
			// inputs. These match the normalized routing defaults above.
			DNSRemote: routing.DefaultDNSRemote,
			DNSDirect: routing.DefaultDNSDirect,
			// The health watchdog is on by default; report it from the start so a
			// status before SetSettings already shows the effective value.
			AutoFailover: true,
		},
		lastGood:     fallback.NewMemLastGood(),
		autoFailover: true,
		now:          time.Now,
		fetch:        subscription.Fetch,

		logLevel: logLevelFromEnv(),
		logs:     newLogRing(logRingSize),

		clientWriteTimeout: defaultClientWriteTimeout,

		httpGet:    defaultHTTPGet,
		ipEchoes:   defaultIPEchoes,
		dnsEchoURL: dnsEchoURL,

		stunProbe:        realStunProbe,
		stunServers:      defaultStunServers,
		speedStream:      defaultSpeedStream,
		speedURLs:        defaultSpeedURLs,
		speedSampleBytes: defaultSpeedSampleBytes,

		entitlement: subscription.FetchEntitlement,

		checkTargets:  defaultCheckTargets,
		checkBasePort: defaultCheckBasePort,
		checkBudget:   defaultCheckBudget,

		bypassVerifyDelay: defaultBypassVerifyDelay,
		localAddrs:        defaultLocalAddrs,
		ifacePresent:      defaultIfacePresent,
		tunWatchInterval:  defaultTunWatchInterval,

		probeWarmup:  defaultProbeWarmup,
		probeRetry:   defaultProbeRetry,
		probeTimeout: defaultProbeTimeout,
		probeBudget:  defaultProbeBudget,

		healthInterval:      defaultHealthInterval,
		healthProbeTimeout:  defaultHealthProbeTimeout,
		healthFailThreshold: defaultHealthFailThreshold,

		switchProbeTimeout: defaultSwitchProbeTimeout,
		switchVerifyBudget: defaultSwitchVerifyBudget,
		autoSwitchCooldown: defaultAutoSwitchCooldown,
		autoSwitchWindow:   defaultAutoSwitchWindow,
		maxAutoSwitches:    defaultMaxAutoSwitches,
		degradedRetryAfter: defaultDegradedRetryAfter,
		switchScanLimit:    defaultSwitchScanLimit,

		relaunchResetAfter: defaultRelaunchReset,
	}
	// The ping dial is pinned to the physical default interface (see
	// newPingDialer): while the tunnel is up with auto_route it would otherwise be
	// captured by the tun and every node would report the ~1-2ms latency to the
	// local tun rather than the real server. Binding the socket to the physical NIC
	// steers each probe past the tun so the RTT readout stays meaningful mid-session.
	d.dial = newPingDialer().DialContext
	// The peer check needs the daemon (it logs its refusals), so it is bound
	// after construction like the other self-referential seams below.
	d.authorizeConn = d.authorizePeer
	// The adaptive-transport escalation asks classify why an attempt failed; the
	// production classifier pairs a direct TCP probe of the entry with the probe's
	// stall signal (see classifyAttemptFailure).
	d.classify = d.classifyAttemptFailure
	// The health watchdog probes the active node through the tunnel; production
	// runs a clash-API delay test through the selector (see defaultHealthProbe).
	d.healthProbe = d.defaultHealthProbe
	// A node check drives its own CONNECT through each probe listener so it can
	// tell a node that never established from one that established and carried
	// nothing (see defaultCheckProbe).
	d.checkProbe = d.defaultCheckProbe
	// The bypass check dials the physical link and completes a TLS handshake --
	// the exact operation a censor interferes with, and the one the bypass exists
	// to get through.
	d.bypassProbe = d.defaultBypassProbe
	// Background entitlement lookups run under this context so Close can cancel a
	// slow one instead of blocking shutdown on it.
	d.entCtx, d.entCancel = context.WithCancel(context.Background())
	return d
}

// SetEmitter installs the event sink. The server calls this before serving.
func (d *Daemon) SetEmitter(emit emitFunc) {
	d.mu.Lock()
	d.emit = emit
	d.mu.Unlock()
}

// SetLastGood swaps in a different last-good store, replacing the in-memory
// default. main wires the disk-backed store here so the last working node leads
// the fallback walk on the next launch; tests keep the in-memory one. Call it
// before serving — it is not synchronised against an in-flight connect.
func (d *Daemon) SetLastGood(lg fallback.LastGood) {
	d.lastGood = lg
}

// SetSettings installs a persistent settings store and immediately loads any
// saved preferences (the split config, the kill switch, the tun stack, the
// autoconnect preference and its last-connect target) into the live options and
// the reported state. main wires the disk-backed store here so the choices
// survive a restart; tests can install a fake or skip it. Call it before
// serving — it is not synchronised against an in-flight connect. Loading never
// starts a connection; that is AutoconnectOnStart's, and only its, job.
func (d *Daemon) SetSettings(store settingsStore) {
	d.settings = store
	ps := store.Load()

	d.mu.Lock()
	mode, apps := splitFromSettings(ps)
	d.routing.SplitMode = mode
	d.routing.SplitApps = apps
	d.routing.KillSwitch = ps.KillSwitch
	d.routing.TLSFragment = ps.TLSFragment
	d.routing.AdBlock = ps.AdBlock
	d.routing.IPv4Only = ps.IPv4Only
	// Empty resolvers (absent in an old file, or never customized) are left for
	// Normalize to fill with the defaults.
	d.routing.DNSRemote = ps.DNSRemote
	d.routing.DNSDirect = ps.DNSDirect
	d.routing.RulesDirect = ps.RulesDirect
	d.routing.RulesProxy = ps.RulesProxy
	d.routing.PresetRuBanking = ps.PresetRuBanking
	d.routing.PresetRuGov = ps.PresetRuGov
	// A stored mode is honoured; anything unrecognised (an old file, a hand-edit)
	// keeps the smart default rather than handing sing-box a mode it will refuse.
	if m := routing.Mode(ps.RoutingMode); m == routing.ModeSmart || m == routing.ModeGlobal || m == routing.ModeDirect {
		d.routing.Mode = m
	}
	// Absent has to read as each preset's own default (see NewDaemon), not one
	// blanket answer: the two that route traffic out of the tunnel are off unless
	// the file says otherwise, the one that routes censored domains into it is on
	// unless the file says otherwise. A stored value either way is the user's
	// choice and is honoured as written. The `true` that 0.5.0 wrote for the two
	// outward presets is the exception, and it is handled before it reaches here:
	// the v1->v2 migration (see migrateV1toV2) clears preset_games_direct and
	// preset_voice_direct, because 0.5.0 had no way to see or change them, so that
	// true was never a choice. What survives to this point is a genuine v2 choice
	// or the migrated default.
	d.routing.GamesDirect = ps.PresetGamesDirect != nil && *ps.PresetGamesDirect
	d.routing.VoiceDirect = ps.PresetVoiceDirect != nil && *ps.PresetVoiceDirect
	d.routing.UnblockServices = ps.PresetUnblockServices == nil || *ps.PresetUnblockServices
	// The picked bypass strategy is restored, not the fact that it was running:
	// nothing is launched here (loading never starts anything), but the next
	// connect re-raises the strategy the probe chose instead of the bundle
	// default.
	d.zapretActive = ps.ZapretStrategy
	// Absent (an old file) keeps automatic bundle updates on; only an explicit
	// stored false holds the installed version.
	d.zapretAutoUpdate = ps.ZapretAutoUpdate == nil || *ps.ZapretAutoUpdate
	d.refreshZapretStateLocked()
	d.routing = d.routing.Normalize()
	// A stack value from a corrupt or hand-edited file must not reach sing-box;
	// anything unknown keeps the default.
	if singbox.ValidStack(ps.TunStack) {
		d.tun.Stack = ps.TunStack
	}
	// The connection mode is likewise validated: an unknown value keeps the tun
	// default seeded in NewDaemon. A stored port replaces the default only when it
	// is a valid port; anything else keeps the default.
	if singbox.ValidMode(ps.ProxyMode) {
		d.tun.Mode = ps.ProxyMode
	}
	if ps.ProxyPort >= 1 && ps.ProxyPort <= 65535 {
		d.tun.MixedPort = ps.ProxyPort
	}
	d.autoconnect = ps.Autoconnect
	// AutoFailover defaults on: an absent field (an old file, or one written before
	// the toggle existed) keeps the watchdog armed; only an explicit stored false
	// disarms it.
	d.autoFailover = ps.AutoFailover == nil || *ps.AutoFailover
	d.multihop = model.Multihop{
		Enabled: ps.Multihop,
		EntryID: ps.MultihopEntryID,
		ExitID:  ps.MultihopExitID,
	}
	// The loaded struct is a fresh local, so holding its pointer is safe; later
	// writes always replace the pointer (never mutate through it), so no snapshot
	// ever races a change.
	d.crashReports = ps.CrashReports
	d.lastProfile = ps.LastProfile
	d.lastNode = ps.LastNode
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()
}

// SetZapretUpdater installs the pair that fetches and applies bypass bundles.
//
// It is wired by main rather than defaulted in the constructor so that reaching
// the internet is something a daemon is given, not something it is born with: a
// unit test builds a daemon and gets an updater that declines, instead of one
// that quietly downloads a bundle into a temp directory and starts a packet
// filter from it.
func (d *Daemon) SetZapretUpdater(
	latest func(ctx context.Context) (zapret.Release, error),
	apply func(ctx context.Context, dir string, rel zapret.Release) error,
) {
	if latest != nil {
		d.zapretLatest = latest
	}
	if apply != nil {
		d.zapretApply = apply
	}
}

// SetBypassRepick arms the strategy search that follows a bypass which turns out
// not to be carrying anything.
//
// Off unless asked for, because the search stops and starts the packet filter
// once per strategy and takes minutes — fine on a real machine that has just
// lost YouTube, ruinous in a unit test.
func (d *Daemon) SetBypassRepick(on bool) { d.bypassRepick = on }

// SetProbeRunner installs the factory a node check uses to run its own sing-box.
//
// It is separate from the tunnel's runner on purpose: a check must never be able
// to stop the tunnel, and the probe process is short-lived where the tunnel's is
// not. main wires the platform supervisor here; a daemon without one refuses
// check_nodes with a plain message instead of failing somewhere deeper.
func (d *Daemon) SetProbeRunner(f func() Runner) {
	d.probeRunner = f
}

// SetRuleSetDir points the routing layer at a directory holding the bundled RU
// rule-set binaries (geoip-ru.srs / geosite-ru.srs) so smart-mode configs load
// them locally instead of downloading them from GitHub at startup — the latter
// blocks sing-box for ~10s (and can FATAL) when raw.githubusercontent.com is
// throttled. main passes the resources dir here only after confirming both
// files exist; an empty dir leaves the remote fallback in place. Call it before
// serving — it is not synchronised against an in-flight connect.
func (d *Daemon) SetRuleSetDir(dir string) {
	d.mu.Lock()
	d.routing.RuleSetDir = dir
	d.mu.Unlock()
}

// persistSettings saves the user preferences (split, kill switch, tun stack,
// autoconnect, the last-connect target) through the settings store, if one is
// installed. It snapshots the live values under mu itself, so the settings file
// always carries a coherent view even when a set command and the connect loop's
// last-connect recording race. Persistence is best-effort: a write failure is
// logged via the daemon's log event but never fails the originating command,
// mirroring how last-good tolerates a failed write.
func (d *Daemon) persistSettings() {
	if d.settings == nil {
		return
	}
	d.mu.Lock()
	ps := d.settingsLocked()
	d.mu.Unlock()
	if err := d.settings.Save(ps); err != nil {
		d.emitLog(LogWarn, fmt.Sprintf("persist settings: %v", err))
	}
}

// settingsLocked projects the persisted preferences out of the daemon's live
// options. Callers must hold d.mu.
func (d *Daemon) settingsLocked() persistedSettings {
	// Copy the flag into a fresh local before taking its address: the returned
	// struct outlives the lock (Save reads it), so it must not alias the live
	// field a later write would change.
	autoFailover := d.autoFailover
	zapretAutoUpdate := d.zapretAutoUpdate
	games := d.routing.GamesDirect
	voice := d.routing.VoiceDirect
	services := d.routing.UnblockServices
	return persistedSettings{
		Version:               settingsVersion,
		RoutingMode:           string(d.routing.Mode),
		PresetGamesDirect:     &games,
		PresetVoiceDirect:     &voice,
		PresetUnblockServices: &services,
		SplitMode:             string(d.routing.SplitMode),
		SplitApps:             d.routing.SplitApps,
		KillSwitch:            d.routing.KillSwitch,
		TLSFragment:           d.routing.TLSFragment,
		TunStack:              d.tun.Stack,
		ProxyMode:             d.tun.Mode,
		ProxyPort:             d.tun.MixedPort,
		Autoconnect:           d.autoconnect,
		AutoFailover:          &autoFailover,
		CrashReports:          d.crashReports,
		AdBlock:               d.routing.AdBlock,
		IPv4Only:              d.routing.IPv4Only,
		DNSRemote:             d.routing.DNSRemote,
		DNSDirect:             d.routing.DNSDirect,
		RulesDirect:           d.routing.RulesDirect,
		RulesProxy:            d.routing.RulesProxy,
		PresetRuBanking:       d.routing.PresetRuBanking,
		PresetRuGov:           d.routing.PresetRuGov,
		Multihop:              d.multihop.Enabled,
		MultihopEntryID:       d.multihop.EntryID,
		MultihopExitID:        d.multihop.ExitID,
		LastProfile:           d.lastProfile,
		LastNode:              d.lastNode,
		ZapretStrategy:        d.zapretActive,
		// Copied into a local first for the same reason as autoFailover: the
		// returned struct outlives the lock.
		ZapretAutoUpdate: &zapretAutoUpdate,
	}
}

// Handle dispatches one request to its command handler and returns the response
// to send. Unknown commands produce an error response rather than a transport
// failure. ctx bounds any network work the command performs (subscription
// fetch, ping), and carries what the peer that sent the request is allowed to
// ask for.
//
// The privilege gate sits here, in front of the switch, rather than inside each
// handler: a command that places or runs executable code in the daemon's
// directory is a privilege boundary, and a boundary that has to be re-stated in
// every new handler is one that will eventually be forgotten in one. Refusals
// are answered and logged, never dropped — a UI that asked has to be able to
// tell "you need administrator rights" from a daemon that hung.
func (d *Daemon) Handle(ctx context.Context, req Request) Response {
	if requiresAdminPeer(req.Cmd) && !peerPrivilegeFrom(ctx) {
		d.emitLog(LogWarn, fmt.Sprintf("control: refusing %s: the peer holds no administrative rights", req.Cmd))
		return newError(req.ID, fmt.Sprintf(
			"%s: нужны права администратора — команда ставит или запускает код от имени службы", req.Cmd))
	}
	switch req.Cmd {
	case CmdStatus:
		return d.handleStatus(req)
	case CmdListProfiles:
		return d.handleListProfiles(req)
	case CmdImportSubscription:
		return d.handleImportSubscription(ctx, req)
	case CmdImportLink:
		return d.handleImportLink(req)
	case CmdImportLinks:
		return d.handleImportLinks(req)
	case CmdRemoveProfile:
		return d.handleRemoveProfile(req)
	case CmdRefreshSub:
		return d.handleRefreshSubscription(ctx, req)
	case CmdConnect:
		return d.handleConnect(ctx, req)
	case CmdDisconnect:
		return d.handleDisconnect(req)
	case CmdPing:
		return d.handlePing(ctx, req)
	case CmdCheckNodes:
		return d.handleCheckNodes(ctx, req)
	case CmdImportZapret:
		return d.handleImportZapret(req)
	case CmdListZapret:
		return d.handleListZapret(req)
	case CmdPickZapret:
		return d.handlePickZapret(ctx, req)
	case CmdStartZapret:
		return d.handleStartZapret(ctx, req)
	case CmdStopZapret:
		return d.handleStopZapret(ctx, req)
	case CmdUpdateZapret:
		return d.handleUpdateZapret(ctx, req)
	case CmdSetZapretAutoUpdate:
		return d.handleSetZapretAutoUpdate(req)
	case CmdSetRouting:
		return d.handleSetRouting(req)
	case CmdSetSplit:
		return d.handleSetSplit(req)
	case CmdSetPresets:
		return d.handleSetPresets(req)
	case CmdSetKillSwitch:
		return d.handleSetKillSwitch(req)
	case CmdSetTLSFragment:
		return d.handleSetTLSFragment(req)
	case CmdSetMultihop:
		return d.handleSetMultihop(req)
	case CmdSetProxyMode:
		return d.handleSetProxyMode(req)
	case CmdSetTun:
		return d.handleSetTun(req)
	case CmdSetAutoconnect:
		return d.handleSetAutoconnect(req)
	case CmdSetAutoFailover:
		return d.handleSetAutoFailover(req)
	case CmdSetDNS:
		return d.handleSetDNS(req)
	case CmdSetRules:
		return d.handleSetRules(req)
	case CmdSetCrashReports:
		return d.handleSetCrashReports(req)
	case CmdCheckServices:
		return d.handleCheckServices(ctx, req)
	case CmdLeakCheck:
		return d.handleLeakCheck(ctx, req)
	case CmdRunStunCheck:
		return d.handleRunStunCheck(ctx, req)
	case CmdRunSpeedTest:
		return d.handleRunSpeedTest(ctx, req)
	case CmdCollectDiagnostics:
		return d.handleCollectDiagnostics(req)
	default:
		return newError(req.ID, fmt.Sprintf("unknown command %q", req.Cmd))
	}
}

// snapshotState returns a copy of the current state under lock.
func (d *Daemon) snapshotState() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// snapshotRouting returns a copy of the live routing options under lock. The
// options are a value type, but SplitApps is a shared slice, so callers that
// might mutate it should copy first; current readers only inspect it.
func (d *Daemon) snapshotRouting() routing.Options {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.routing
}

// snapshotTun returns a copy of the live tun/inbound options under lock. The
// system-proxy guard reads it to learn the effective mixed address; it is a value
// type with no shared slices, so the copy is safe to use after the lock drops.
func (d *Daemon) snapshotTun() singbox.TunOptions {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tun
}

// handleStatus answers a status command with the current connection snapshot.
// It also re-pushes the live fallback-walk snapshot when one is in flight: a
// client re-syncs by sending status on connect (see docs/control-protocol.md),
// so a UI that attached mid-walk learns of it here rather than only seeing the
// steps that happen after it subscribed.
func (d *Daemon) handleStatus(req Request) Response {
	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	d.pushAttemptsIfActive()
	return resp
}

// pushAttemptsIfActive re-emits the stored fallback-walk snapshot when a walk is
// in progress (the state is still connecting and a snapshot is held), so a
// client attaching mid-walk sees it. It no-ops once the walk has settled — a
// completed walk is not replayed to a client that connected after it finished.
func (d *Daemon) pushAttemptsIfActive() {
	d.mu.Lock()
	snap := d.attempts
	active := d.state.State == StateConnecting && snap != nil
	emit := d.emit
	d.mu.Unlock()
	if active && emit != nil {
		emit(EventAttempts, *snap)
	}
}

// redactedNode is the subset of a stored server the UI renders: its stable ID
// and the non-secret display fields. It deliberately drops every credential a
// model.Node carries — the VLESS/VMess UUID, the Trojan/Shadowsocks/Hysteria2
// password, the Shadowsocks method and obfs secret, the (Amnezia)WG private and
// pre-shared keys, and the REALITY keys — because the control socket is readable
// by any local user (the deliberate machine-wide-tunnel trust; see
// docs/control-protocol.md), so serialising the full node here would hand every
// secret in the store to any unprivileged process. The fields kept are exactly
// those the desktop Node type consumes (id, protocol, name, server, port).
type redactedNode struct {
	ID       string         `json:"id"`
	Protocol model.Protocol `json:"protocol"`
	Name     string         `json:"name"`
	Server   string         `json:"server"`
	Port     int            `json:"port"`
	// Insecure is not a secret — it is the opposite, a warning: the node disables
	// TLS certificate verification (skip-cert-verify), which the UI surfaces as a
	// badge. Dropping it here would silently hide that warning, so it is carried
	// through the redacted view exactly as profile.Server.MarshalJSON derives it.
	Insecure bool `json:"insecure,omitempty"`
}

// redactedProfile mirrors profile.Profile minus the two secret-bearing parts:
// the subscription URL — whose path or query embeds the account token — and each
// node's credentials (see redactedNode). The URL is dropped whole rather than
// masked in place: the UI never renders it (it drives the refresh affordance off
// Source, not URL presence), and a token can sit in a path segment, a query
// parameter, or a fragment, so any partial mask risks leaking one it did not
// anticipate. Everything the UI does render — the ID, name, source, refresh
// timestamp, and the subscription quota/expiry — is preserved verbatim.
type redactedProfile struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Source       string         `json:"source"`
	Nodes        []redactedNode `json:"nodes"`
	UpdatedAt    time.Time      `json:"updatedAt"`
	ExpiresAt    *time.Time     `json:"expiresAt,omitempty"`
	TrafficUsed  int64          `json:"trafficUsed,omitempty"`
	TrafficTotal int64          `json:"trafficTotal,omitempty"`
	// Managed and Tier are safe to surface: neither is a secret (unlike the URL,
	// which is dropped), and the UI needs both to draw the managed/premium badge.
	Managed bool   `json:"managed,omitempty"`
	Tier    string `json:"tier,omitempty"`
}

// redactProfile projects one stored profile onto its wire-safe view, copying
// only the non-secret fields. The connect path never goes through here — it
// reads the full stored profile straight from the store — so redacting the
// outbound view costs the UI nothing.
func redactProfile(p profile.Profile) redactedProfile {
	nodes := make([]redactedNode, len(p.Servers))
	for i, s := range p.Servers {
		nodes[i] = redactedNode{
			ID:       s.ID,
			Protocol: s.Protocol,
			Name:     s.Name,
			Server:   s.Server,
			Port:     s.Port,
			Insecure: s.TLS != nil && s.TLS.Insecure,
		}
	}
	return redactedProfile{
		ID:           p.ID,
		Name:         p.Name,
		Source:       p.Source,
		Nodes:        nodes,
		UpdatedAt:    p.UpdatedAt,
		ExpiresAt:    p.ExpiresAt,
		TrafficUsed:  p.TrafficUsed,
		TrafficTotal: p.TrafficTotal,
		Managed:      p.Managed,
		Tier:         p.Tier,
	}
}

// redactProfiles maps a stored profile list to the wire-safe view, always
// returning a non-nil slice so the UI receives [] rather than null.
func redactProfiles(ps []profile.Profile) []redactedProfile {
	out := make([]redactedProfile, len(ps))
	for i, p := range ps {
		out[i] = redactProfile(p)
	}
	return out
}

// handleListProfiles returns every stored profile in the redacted wire view —
// display fields only, no subscription token, no node credentials (see
// redactedProfile) — normalising a nil slice to [] so the UI always receives an
// array.
func (d *Daemon) handleListProfiles(req Request) Response {
	out := struct {
		Profiles []redactedProfile `json:"profiles"`
	}{Profiles: redactProfiles(d.store.List())}
	resp, err := newResult(req.ID, out)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleImportSubscription fetches a subscription URL, parses its nodes into a
// new profile, applies any Subscription-Userinfo quota, and stores it.
func (d *Daemon) handleImportSubscription(ctx context.Context, req Request) Response {
	if req.URL == "" {
		return newError(req.ID, "import_subscription: missing url")
	}
	name := req.Name
	if name == "" {
		name = req.URL
	}
	body, header, err := d.fetch(ctx, req.URL)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	nodes, _, err := subscription.ParseSubscription(body)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	if len(nodes) == 0 {
		return newError(req.ID, "import_subscription: no usable nodes in subscription")
	}

	p, err := profile.NewProfile(name, profile.SourceSubscription, req.URL, nodes)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	applyUserInfo(&p, header.Get("Subscription-Userinfo"))
	// Recognise a managed subscription (operator-served) so the UI can badge it
	// immediately; the tier itself is resolved off the import path below.
	p.Managed = subscription.DetectManaged(req.URL)

	if err := d.store.Add(p); err != nil {
		return newError(req.ID, err.Error())
	}
	// A managed subscription's entitlement is fetched in the background so it
	// never delays or fails the import: the profile is already stored and
	// returned; the tier lands on it (and the UI is signalled) if and when the
	// lookup confirms premium.
	d.startEntitlementLookup(p.ID, req.URL, p.Managed)
	return d.profileResult(req.ID, p)
}

// handleImportLink parses a single share link into a one-node manual profile,
// naming it from the request, the node's own label, or a generic fallback.
func (d *Daemon) handleImportLink(req Request) Response {
	if req.Link == "" {
		return newError(req.ID, "import_link: missing link")
	}
	node, err := subscription.ParseLink(req.Link)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	name := req.Name
	if name == "" {
		// Fall back to the node's own label, then a generic one.
		if node.Name != "" {
			name = node.Name
		} else {
			name = "Imported link"
		}
	}
	p, err := profile.NewProfile(name, profile.SourceManual, "", []model.Node{node})
	if err != nil {
		return newError(req.ID, err.Error())
	}
	if err := d.store.Add(p); err != nil {
		return newError(req.ID, err.Error())
	}
	return d.profileResult(req.ID, p)
}

// handleImportLinks parses a batch of share links into a single multi-node
// manual profile. It is the convenience path for pasting several links at once
// or loading a .txt list: every valid link becomes one server in the same
// profile, unparseable lines are skipped (and counted) rather than aborting, and
// duplicates/blanks/comments are dropped by ParseLinks. The reply carries the new
// profile plus how many links were imported and skipped, so the UI can report
// "imported N, skipped M".
func (d *Daemon) handleImportLinks(req Request) Response {
	if len(req.Links) == 0 {
		return newError(req.ID, "import_links: missing links")
	}
	nodes, skipped := subscription.ParseLinks(req.Links)
	if len(nodes) == 0 {
		// Nothing parsed: surface a clear failure (and how many were skipped)
		// instead of creating an empty profile the user can't connect to.
		return newError(req.ID, fmt.Sprintf("import_links: no valid links found (%d skipped)", skipped))
	}
	name := req.Name
	if name == "" {
		name = "Imported links"
	}
	p, err := profile.NewProfile(name, profile.SourceManual, "", nodes)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	if err := d.store.Add(p); err != nil {
		return newError(req.ID, err.Error())
	}
	return d.importLinksResult(req.ID, p, len(nodes), skipped)
}

// importLinksResult wraps a batch import into the {profile, imported, skipped}
// response shape. imported is the number of servers added (equal to the profile's
// server count) and skipped the number of links that failed to parse. The profile
// is redacted (see redactProfile) before it goes on the wire: this reply travels
// the same local socket as list_profiles, so it must not carry the node
// credentials or a token-bearing URL either.
func (d *Daemon) importLinksResult(id int64, p profile.Profile, imported, skipped int) Response {
	out := struct {
		Profile  redactedProfile `json:"profile"`
		Imported int             `json:"imported"`
		Skipped  int             `json:"skipped"`
	}{Profile: redactProfile(p), Imported: imported, Skipped: skipped}
	resp, err := newResult(id, out)
	if err != nil {
		return newError(id, err.Error())
	}
	return resp
}

// handleRemoveProfile deletes a profile, first tearing down the tunnel if it is
// the one currently connected so no orphaned process outlives it.
func (d *Daemon) handleRemoveProfile(req Request) Response {
	if req.Profile == "" {
		return newError(req.ID, "remove_profile: missing profile")
	}
	// If we're connected to this profile, tear the tunnel down first so we don't
	// leave an orphaned process bound to a profile that no longer exists. Under
	// connMu so the transition is authoritative over an in-flight relaunch/reconcile.
	if cur := d.snapshotState(); cur.Profile == req.Profile && cur.State != StateIdle {
		d.connMu.Lock()
		d.teardown(StateIdle, "", "")
		d.connMu.Unlock()
	}
	if err := d.store.Remove(req.Profile); err != nil {
		return newError(req.ID, err.Error())
	}
	return newResult0(req.ID)
}

// handleRefreshSubscription re-fetches one subscription profile's nodes from its
// source URL. It rejects manual profiles, which have no URL to refresh from.
func (d *Daemon) handleRefreshSubscription(ctx context.Context, req Request) Response {
	if req.Profile == "" {
		return newError(req.ID, "refresh_subscription: missing profile")
	}
	p, ok := d.store.Get(req.Profile)
	if !ok {
		return newError(req.ID, profile.ErrNotFound.Error())
	}
	if p.Source != profile.SourceSubscription || p.URL == "" {
		return newError(req.ID, "refresh_subscription: profile is not a subscription")
	}

	updated, _, err := d.refreshProfile(ctx, p)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	// A manual refresh changed stored data (servers and/or usage); tell the UI so
	// any other view of the profile list updates without a poll.
	d.emitProfiles()
	return d.profileResult(req.ID, updated)
}

// refreshProfile fetches and re-parses one subscription profile, grafts the
// fresh content onto the existing profile so its ID and creation identity
// persist, folds in the latest usage from the Subscription-Userinfo header, and
// writes it back through the store. It returns the updated profile and whether
// the stored profile actually changed (so callers can decide whether to signal
// the UI). The profile must be a subscription with a non-empty URL; callers
// guarantee that.
func (d *Daemon) refreshProfile(ctx context.Context, p profile.Profile) (profile.Profile, bool, error) {
	body, header, err := d.fetch(ctx, p.URL)
	if err != nil {
		return profile.Profile{}, false, err
	}
	nodes, _, err := subscription.ParseSubscription(body)
	if err != nil {
		return profile.Profile{}, false, err
	}
	if len(nodes) == 0 {
		return profile.Profile{}, false, fmt.Errorf("refresh: no usable nodes in subscription %q", p.Name)
	}

	// Rebuild a profile to get fresh, stably-IDed servers, then graft the new
	// content onto the existing profile so its ID and creation identity persist.
	rebuilt, err := profile.NewProfile(p.Name, profile.SourceSubscription, p.URL, nodes)
	if err != nil {
		return profile.Profile{}, false, err
	}
	before := p
	p.Servers = rebuilt.Servers
	p.UpdatedAt = rebuilt.UpdatedAt
	// Only refresh traffic/expiry when this response actually carries a user-info
	// header. A refresh that returns the node list but no Subscription-Userinfo
	// (some panels send it only intermittently) must preserve the known quota and
	// expiry rather than silently zeroing them — the 6h background sweep would
	// otherwise wipe them. applyUserInfo no-ops on a blank header, so guarding the
	// overwrite here is what keeps the prior values.
	if ui := header.Get("Subscription-Userinfo"); ui != "" {
		p.ExpiresAt = nil
		p.TrafficUsed = 0
		p.TrafficTotal = 0
		applyUserInfo(&p, ui)
	}
	// Re-recognise the managed shape (idempotent for a stable URL) and re-validate
	// the tier inline: a refresh is already a background or user-initiated sweep,
	// so a synchronous lookup is fine. It is fail-closed — a transient failure
	// re-validates to free rather than aborting the refresh — so an entitlement
	// that lapsed (or an endpoint that is momentarily unreachable) drops the badge
	// until the next refresh restores it.
	p.Managed = subscription.DetectManaged(p.URL)
	p.Tier = d.lookupTier(ctx, p.URL, p.Managed)

	if err := d.store.Update(p); err != nil {
		return profile.Profile{}, false, err
	}
	return p, profileChanged(before, p), nil
}

// refreshAllSubscriptions re-fetches every subscription profile that has a URL,
// reusing the same refresh path as the manual command. A fetch or parse failure
// on one profile is logged and skipped — it never aborts the sweep or drops the
// profile's existing data. It returns whether any stored profile changed, so the
// caller can emit a single profiles event for the batch. With no subscription
// profiles it returns false without doing any work.
func (d *Daemon) refreshAllSubscriptions(ctx context.Context) bool {
	changedAny := false
	for _, p := range d.store.List() {
		if p.Source != profile.SourceSubscription || p.URL == "" {
			continue
		}
		_, changed, err := d.refreshProfile(ctx, p)
		if err != nil {
			d.emitLog(LogWarn, fmt.Sprintf("auto-refresh of %q failed: %v", p.Name, err))
			continue
		}
		if changed {
			changedAny = true
		}
	}
	return changedAny
}

// entitlementTimeout bounds a single background entitlement lookup so a stalled
// endpoint neither leaks a goroutine nor delays Close beyond it.
const entitlementTimeout = 15 * time.Second

// lookupTier resolves a managed subscription's effective entitlement tier,
// folding every failure into the free tier (fail-closed). A non-managed
// subscription has no tier ("" — unknown/not applicable); a managed one whose
// target cannot be derived, whose endpoint errors, or whose answer is not an
// active premium entitlement resolves to "free". Premium is only ever returned
// on a positive, authenticated answer, so the badge cannot be conjured by a
// failure path.
func (d *Daemon) lookupTier(ctx context.Context, subURL string, managed bool) string {
	if !managed {
		return ""
	}
	origin, key, ok := subscription.EntitlementTarget(subURL)
	if !ok {
		return subscription.TierFree
	}
	ent, err := d.entitlement(ctx, origin, key)
	if err != nil {
		return subscription.TierFree
	}
	return ent.NormalizedTier()
}

// startEntitlementLookup resolves a freshly imported managed subscription's tier
// off the import path. It only ever surfaces an upgrade: the profile was just
// stored with no tier, so a free/failed lookup leaves it unchanged (and emits
// nothing), while a confirmed premium answer is written back and the UI is
// signalled to reload. A non-managed import is a no-op.
func (d *Daemon) startEntitlementLookup(id, subURL string, managed bool) {
	if !managed {
		return
	}
	base := d.entCtx
	if base == nil {
		base = context.Background()
	}
	d.entWG.Add(1)
	go func() {
		defer d.entWG.Done()
		ctx, cancel := context.WithTimeout(base, entitlementTimeout)
		defer cancel()
		if d.lookupTier(ctx, subURL, true) != subscription.TierPremium {
			return
		}
		cur, ok := d.store.Get(id)
		if !ok || cur.Tier == subscription.TierPremium {
			return
		}
		cur.Tier = subscription.TierPremium
		if err := d.store.Update(cur); err != nil {
			return
		}
		d.emitProfiles()
	}()
}

// profileChanged reports whether a refresh produced a profile materially
// different from the one before it, ignoring the UpdatedAt timestamp (which a
// refresh always bumps). It drives whether the UI is told to reload: an
// identical subscription should not churn the list.
func profileChanged(before, after profile.Profile) bool {
	if before.TrafficUsed != after.TrafficUsed ||
		before.TrafficTotal != after.TrafficTotal {
		return true
	}
	// A managed/tier flip changes the badge, so the UI must reload for it.
	if before.Managed != after.Managed || before.Tier != after.Tier {
		return true
	}
	if (before.ExpiresAt == nil) != (after.ExpiresAt == nil) {
		return true
	}
	if before.ExpiresAt != nil && !before.ExpiresAt.Equal(*after.ExpiresAt) {
		return true
	}
	if len(before.Servers) != len(after.Servers) {
		return true
	}
	for i := range before.Servers {
		if before.Servers[i].ID != after.Servers[i].ID {
			return true
		}
	}
	return false
}

// handleSetRouting validates and stores the routing mode (smart/global/direct).
//
// The choice is persisted and applied to a live tunnel in place, like the kill
// switch. Neither used to happen: the mode was held in memory only, so a user who
// picked global got smart back at the next launch with the UI reporting the mode
// the daemon had silently reset to — and while connected the change did nothing
// at all until the user thought to reconnect, which is indistinguishable from a
// control that does not work.
func (d *Daemon) handleSetRouting(req Request) Response {
	mode := routing.Mode(req.Mode)
	switch mode {
	case routing.ModeSmart, routing.ModeGlobal, routing.ModeDirect:
	default:
		return newError(req.ID, fmt.Sprintf("set_routing: unknown mode %q", req.Mode))
	}

	d.mu.Lock()
	changed := d.routing.Mode != mode
	d.routing.Mode = mode
	d.routing = d.routing.Normalize()
	d.state.Routing = string(mode)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetPresets toggles the three routing presets: game clients direct,
// real-time UDP direct, and the censored-services list routed by whether the
// bypass covers them.
//
// An omitted field leaves that preset alone. The three have different defaults
// and are toggled from different rows of the same screen, so a UI that had to
// restate every one to change any one would eventually restate them wrong — and
// two of the three decide whether a class of traffic leaves the tunnel, which is
// not something to get wrong by accident.
//
// This only records intent; the routing layer decides what a recorded intent
// actually emits. All three are inert in direct mode, and the two that pin
// traffic to the direct outbound also yield to the kill switch — unblock-services
// does not, because it only ever pins domains to the tunnel.
func (d *Daemon) handleSetPresets(req Request) Response {
	if req.Games == nil && req.Voice == nil && req.Services == nil {
		return newError(req.ID, "set_presets: nothing to change")
	}

	d.mu.Lock()
	before := d.routing
	if req.Games != nil {
		d.routing.GamesDirect = *req.Games
	}
	if req.Voice != nil {
		d.routing.VoiceDirect = *req.Voice
	}
	if req.Services != nil {
		d.routing.UnblockServices = *req.Services
	}
	changed := before.GamesDirect != d.routing.GamesDirect ||
		before.VoiceDirect != d.routing.VoiceDirect ||
		before.UnblockServices != d.routing.UnblockServices
	d.routing = d.routing.Normalize()
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetSplit updates the per-app split configuration. The normalized config
// is persisted and, like set_routing, applied to a live tunnel in place: a user
// who excludes an app while connected expects that app to leave the tunnel now,
// not after they work out that a reconnect is required.
func (d *Daemon) handleSetSplit(req Request) Response {
	mode := routing.SplitMode(req.Mode)
	switch mode {
	case routing.SplitOff, routing.SplitExclude, routing.SplitInclude:
	default:
		return newError(req.ID, fmt.Sprintf("set_split: unknown mode %q", req.Mode))
	}

	d.mu.Lock()
	before := d.routing
	d.routing.SplitMode = mode
	d.routing.SplitApps = req.Apps
	d.routing = d.routing.Normalize()
	changed := before.SplitMode != d.routing.SplitMode ||
		!sameStrings(before.SplitApps, d.routing.SplitApps)
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// sameStrings reports whether two normalized string slices are equal. Used to
// decide whether a settings command actually changed anything, so an idempotent
// re-send does not hot-swap sing-box for nothing.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// handleSetKillSwitch arms or disarms the kill switch. The choice is recorded,
// persisted, and — unlike set_routing/set_split — applied to a live tunnel in
// place: the daemon rebuilds the config for the node it is already on and
// hot-swaps the sing-box process (see reapplyLive), so arming doesn't wait for
// the user to reconnect. Armed means strict_route on the tun (sing-box installs
// filter rules that drop any packet trying to escape the tunnel) plus an
// automatic relaunch if the tunnel process itself dies (see watchProcess).
func (d *Daemon) handleSetKillSwitch(req Request) Response {
	d.mu.Lock()
	changed := d.routing.KillSwitch != req.On
	d.routing.KillSwitch = req.On
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetTLSFragment arms or disarms forced TLS ClientHello fragmentation. Like
// the kill switch the choice is recorded, persisted, and — unlike
// set_routing/set_split — applied to a live tunnel in place: the daemon rebuilds
// the config for the node it is already on and hot-swaps the sing-box process (see
// reapplyLive), so arming doesn't wait for a reconnect. Armed folds tls.fragment
// into every TLS-bearing outbound the config builds. The adaptive walk still
// reaches fragmentation per-node on a censored handshake; this is the user's
// unconditional override for a network that blocks the plaintext SNI outright.
func (d *Daemon) handleSetTLSFragment(req Request) Response {
	d.mu.Lock()
	changed := d.routing.TLSFragment != req.On
	d.routing.TLSFragment = req.On
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetMultihop arms or disarms the two-hop chain and records which entry and
// exit nodes it runs through. Like the kill switch the choice is recorded,
// persisted, and applied to a live tunnel in place via reapplyLive, so arming
// doesn't wait for a reconnect. Enabling validates the selection up front — both
// nodes must be named, distinct, and present in the profile the request targets —
// so a bad pick is rejected whole rather than half-applied; disabling ignores the
// IDs but keeps them recorded so the UI can re-enable the last pick. The IDs are
// resolved to outbound tags against the connecting profile later (resolveMultihop),
// so a selection that no longer resolves simply degrades to a single hop.
func (d *Daemon) handleSetMultihop(req Request) Response {
	mh := model.Multihop{Enabled: req.Enabled, EntryID: req.EntryID, ExitID: req.ExitID}
	if mh.Enabled {
		if mh.EntryID == "" || mh.ExitID == "" {
			return newError(req.ID, "set_multihop: entry and exit nodes are required")
		}
		if mh.EntryID == mh.ExitID {
			return newError(req.ID, "set_multihop: entry and exit nodes must differ")
		}
		p, ok := d.store.Get(req.Profile)
		if !ok {
			return newError(req.ID, "set_multihop: profile not found")
		}
		if !hasServer(p, mh.EntryID) {
			return newError(req.ID, "set_multihop: entry node not in profile")
		}
		if !hasServer(p, mh.ExitID) {
			return newError(req.ID, "set_multihop: exit node not in profile")
		}
	}

	d.mu.Lock()
	changed := d.multihop != mh
	d.multihop = mh
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// hasServer reports whether the profile contains a server with the given stable
// ID. It backs set_multihop's existence check on the entry/exit selection.
func hasServer(p profile.Profile, id string) bool {
	for _, s := range p.Servers {
		if s.ID == id {
			return true
		}
	}
	return false
}

// resolveMultihop folds a stored multihop selection (server IDs) into the routing
// options the builder consumes (outbound tags), using the tag map the connecting
// profile produces (serverTags). It engages only for a valid, distinct pair whose
// IDs both resolve to a tag the builder will actually emit; anything else leaves
// the options untouched so the build degrades to a normal single hop rather than
// carrying a dangling detour. tags maps a server ID to its outbound tag.
func resolveMultihop(ro routing.Options, mh model.Multihop, tags map[string]string) routing.Options {
	if !mh.Valid() {
		return ro
	}
	entryTag := tags[mh.EntryID]
	exitTag := tags[mh.ExitID]
	if entryTag == "" || exitTag == "" || entryTag == exitTag {
		return ro
	}
	ro.Multihop = true
	ro.MultihopEntry = entryTag
	ro.MultihopExit = exitTag
	return ro
}

// multihopResolved reports whether two routing snapshots carry the same resolved
// multihop chain (the builder-facing tag fields). It gates the connecting-window
// reconcile so a multihop toggle that lands mid-connect triggers a hot-swap just
// as the kill switch or TLS fragmentation would.
func multihopResolved(a, b routing.Options) bool {
	return a.Multihop == b.Multihop &&
		a.MultihopEntry == b.MultihopEntry &&
		a.MultihopExit == b.MultihopExit
}

// handleSetProxyMode switches the connection mode between "tun" and
// "system-proxy". Like the kill switch it is recorded, persisted, and applied to
// a live tunnel in place via reapplyLive — the inbound family is a startup option
// of sing-box, so the swap restarts the process on the same node rather than
// walking the fallback order again. Switching a live tunnel to tun clears the OS
// proxy (teardown disarms it); switching to system-proxy re-arms it once the
// swapped tunnel comes up (recordSuccess). An unknown mode is rejected whole. An
// explicit ProxyPort overrides the mixed inbound's loopback port; 0 leaves it.
func (d *Daemon) handleSetProxyMode(req Request) Response {
	if !singbox.ValidMode(req.ProxyMode) {
		return newError(req.ID, fmt.Sprintf("set_proxy_mode: unknown mode %q (want %q or %q)",
			req.ProxyMode, singbox.ModeTun, singbox.ModeSystemProxy))
	}
	if req.ProxyPort != 0 && (req.ProxyPort < 1 || req.ProxyPort > 65535) {
		return newError(req.ID, fmt.Sprintf("set_proxy_mode: port %d out of range 1..65535", req.ProxyPort))
	}

	d.mu.Lock()
	changed := d.tun.Mode != req.ProxyMode
	d.tun.Mode = req.ProxyMode
	if req.ProxyPort != 0 && d.tun.MixedPort != req.ProxyPort {
		d.tun.MixedPort = req.ProxyPort
		changed = true
	}
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetTun switches the tun network stack (system/gvisor/mixed). Recorded,
// persisted, and applied to a live tunnel in place via reapplyLive — the stack
// is a startup option of the tun inbound, so the swap restarts sing-box on the
// same node rather than walking the whole fallback order again.
func (d *Daemon) handleSetTun(req Request) Response {
	if !singbox.ValidStack(req.Stack) {
		return newError(req.ID, fmt.Sprintf("set_tun: unknown stack %q", req.Stack))
	}

	d.mu.Lock()
	changed := d.tun.Stack != req.Stack
	d.tun.Stack = req.Stack
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetAutoconnect arms or disarms connect-on-start. The choice is
// recorded, persisted with the other preferences, and reported back in the
// state. Unlike the kill switch it changes nothing about a live tunnel: the
// preference only matters when the daemon itself next starts (see
// AutoconnectOnStart), so there is no live re-apply.
func (d *Daemon) handleSetAutoconnect(req Request) Response {
	d.mu.Lock()
	d.autoconnect = req.On
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetAutoFailover arms or disarms the health watchdog. Like autoconnect it
// changes nothing about a live tunnel in place: the watchdog re-reads the flag on
// its next tick, so a mid-session toggle takes effect on its own without a
// reconnect. The choice is recorded, persisted with the other preferences, and
// reported back in the state.
func (d *Daemon) handleSetAutoFailover(req Request) Response {
	d.mu.Lock()
	d.autoFailover = req.On
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetCrashReports records the crash-report consent. Like autoconnect it
// changes nothing about a live tunnel — it only governs whether the GUI offers
// to surface a locally saved crash report on the next launch, and nothing is
// ever sent anywhere by the core — so there is no live re-apply. The choice is
// stored as an explicit true/false (never left nil once the user has answered),
// so the first-run prompt does not return after they have decided.
func (d *Daemon) handleSetCrashReports(req Request) Response {
	d.mu.Lock()
	v := req.On
	d.crashReports = &v
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// handleSetDNS records the DNS preferences: the ad/tracker-block toggle, the
// IPv4-only toggle, and the two custom resolvers. Like the kill switch it applies
// to a live tunnel in place via reapplyLive — all of them feed the generated dns
// block, so "apply now" means a hot-swap on the same node rather than waiting for
// the next connect. A resolver
// that fails validation rejects the whole command (nothing is recorded) so a typo
// can't half-apply or reach sing-box; an empty resolver is accepted and Normalize
// substitutes the default. The normalized choice is persisted so it survives a
// restart, and reflected back so the UI stays in sync.
func (d *Daemon) handleSetDNS(req Request) Response {
	if !routing.ValidDNSServer(req.DNSRemote) {
		return newError(req.ID, fmt.Sprintf("set_dns: invalid remote resolver %q", req.DNSRemote))
	}
	if !routing.ValidDNSServer(req.DNSDirect) {
		return newError(req.ID, fmt.Sprintf("set_dns: invalid direct resolver %q", req.DNSDirect))
	}

	d.mu.Lock()
	// d.routing is always kept normalized, so "before" already holds the effective
	// (default-substituted) resolvers to compare the new choice against.
	before := d.routing
	d.routing.AdBlock = req.AdBlock
	d.routing.IPv4Only = req.IPv4Only
	d.routing.DNSRemote = req.DNSRemote
	d.routing.DNSDirect = req.DNSDirect
	// Normalize turns an empty resolver back into the default, so the recorded and
	// reported values are always the effective ones — and so clearing an
	// already-default field to "" compares equal below and is correctly a no-op.
	d.routing = d.routing.Normalize()
	changed := dnsPrefsDiffer(before, d.routing)
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// dnsPrefsDiffer reports whether the DNS-affecting preferences (ad-block,
// IPv4-only, and the two resolvers) differ between two option snapshots. It gates
// the live hot-swap so only a real change restarts sing-box.
func dnsPrefsDiffer(a, b routing.Options) bool {
	return a.AdBlock != b.AdBlock || a.DNSRemote != b.DNSRemote ||
		a.DNSDirect != b.DNSDirect || a.IPv4Only != b.IPv4Only
}

// handleSetRules records the custom domain-suffix routing rules and the RU
// direct-rule presets. Like set_dns it validates first — a malformed suffix
// rejects the whole command (nothing half-applies or reaches sing-box) — then
// records, persists, reflects the choice back, and, when a tunnel is live,
// re-applies it in place via reapplyLive (a hot-swap on the same node, not a
// reconnect). The suffixes are normalized (lowercased, trimmed, de-duplicated,
// sorted), so the reported and persisted values are stable.
func (d *Daemon) handleSetRules(req Request) Response {
	for _, s := range req.RulesDirect {
		if !routing.ValidDomainSuffix(s) {
			return newError(req.ID, fmt.Sprintf("set_rules: invalid direct rule %q", s))
		}
	}
	for _, s := range req.RulesProxy {
		if !routing.ValidDomainSuffix(s) {
			return newError(req.ID, fmt.Sprintf("set_rules: invalid proxy rule %q", s))
		}
	}

	d.mu.Lock()
	// d.routing is always kept normalized, so "before" already holds the effective
	// (sorted, de-duplicated) rules to compare the new choice against.
	before := d.routing
	d.routing.RulesDirect = req.RulesDirect
	d.routing.RulesProxy = req.RulesProxy
	d.routing.PresetRuBanking = req.PresetRuBanking
	d.routing.PresetRuGov = req.PresetRuGov
	d.routing = d.routing.Normalize()
	changed := rulesPrefsDiffer(before, d.routing)
	applySettingsToState(&d.state, d.routing, d.tun, d.autoconnect, d.autoFailover, d.crashReports, d.multihop)
	d.mu.Unlock()

	d.persistSettings()
	if changed {
		d.reapplyLive()
	}

	resp, err := newResult(req.ID, d.snapshotState())
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// rulesPrefsDiffer reports whether the custom-rule preferences (the two suffix
// lists and the two preset toggles) differ between two option snapshots. It gates
// the live hot-swap so only a real change restarts sing-box. Both snapshots are
// normalized, so slices.Equal compares two stable, sorted lists.
func rulesPrefsDiffer(a, b routing.Options) bool {
	return a.PresetRuBanking != b.PresetRuBanking ||
		a.PresetRuGov != b.PresetRuGov ||
		!slices.Equal(a.RulesDirect, b.RulesDirect) ||
		!slices.Equal(a.RulesProxy, b.RulesProxy)
}

// applySettingsToState mirrors the daemon-wide preferences onto a State: the
// normalized split config, the kill switch, the tun stack, the autoconnect and
// auto-failover preferences, and the DNS choices (ad-block, IPv4-only, plus the
// two resolvers).
// Split off
// collapses to empty fields so the wire form omits them, keeping a no-op split
// invisible to the UI; the app slice is copied so the State never aliases the
// daemon's live routing options. The resolvers are always the normalized
// (effective) values, so the UI can prefill its custom-DNS inputs.
func applySettingsToState(s *State, ro routing.Options, tun singbox.TunOptions, autoconnect, autoFailover bool, crashReports *bool, multihop model.Multihop) {
	// The build version is a daemon-wide constant like the preferences below:
	// stamping it here keeps it on every snapshot no matter which State literal
	// a call site handed in, so a GUI can always compare it against its own.
	s.DaemonVersion = buildinfo.Version
	// Report the mode exactly as chosen (an empty app list no longer collapses
	// it to off — the UI needs the mode active to show the app editor at all),
	// and always carry the app list so toggling the mode off and back on does
	// not lose the user's set.
	if ro.SplitMode == routing.SplitOff {
		s.Split = ""
	} else {
		s.Split = string(ro.SplitMode)
	}
	s.SplitApps = append([]string(nil), ro.SplitApps...)
	// The routing mode is a stored preference like the rest of these, so it is
	// reported from the live options rather than only where set_routing writes it.
	// Without this a mode restored from the settings file was in effect but not on
	// screen: the daemon routed globally while the UI showed smart.
	if ro.Mode != "" {
		s.Routing = string(ro.Mode)
	}
	s.KillSwitch = ro.KillSwitch
	s.TLSFragment = ro.TLSFragment
	s.TunStack = tun.Stack
	// Report the effective connection mode and mixed port. The daemon seeds both
	// concrete at construction and SetSettings/handleSetProxyMode keep them so, but
	// fall back to the builder defaults here to stay robust against a zero value.
	if tun.Mode != "" {
		s.ProxyMode = tun.Mode
	} else {
		s.ProxyMode = singbox.ModeTun
	}
	if tun.MixedPort != 0 {
		s.ProxyPort = tun.MixedPort
	} else {
		s.ProxyPort = singbox.DefaultMixedPort
	}
	s.Autoconnect = autoconnect
	s.AutoFailover = autoFailover
	// Copy the crash-report consent by value so the State never aliases the
	// daemon's live pointer: nil stays "not asked" (both fields cleared), a set
	// value is mirrored with the has-been-asked bit raised.
	if crashReports == nil {
		s.CrashReports = nil
		s.CrashReportsAsked = false
	} else {
		v := *crashReports
		s.CrashReports = &v
		s.CrashReportsAsked = true
	}
	s.AdBlock = ro.AdBlock
	s.IPv4Only = ro.IPv4Only
	s.DNSRemote = ro.DNSRemote
	s.DNSDirect = ro.DNSDirect
	// The rule lists are copied so the State never aliases the daemon's live
	// options; an empty list collapses to nil, so the wire form omits it (like the
	// split fields), keeping unset rules invisible to the UI.
	s.RulesDirect = append([]string(nil), ro.RulesDirect...)
	s.RulesProxy = append([]string(nil), ro.RulesProxy...)
	s.PresetRuBanking = ro.PresetRuBanking
	s.PresetRuGov = ro.PresetRuGov
	s.PresetGamesDirect = ro.GamesDirect
	s.PresetVoiceDirect = ro.VoiceDirect
	s.PresetUnblockServices = ro.UnblockServices
	// Copy the multihop selection by value into a fresh pointer so the State never
	// aliases the daemon's live field: nil while nothing has been selected (so the
	// wire form omits it), else the current enabled/entry/exit, carried even when
	// disabled so the UI can prefill its node selectors.
	if multihop.IsZero() {
		s.Multihop = nil
	} else {
		mh := multihop
		s.Multihop = &mh
	}
}

// handlePing measures dial latency to every server in a profile and returns the
// per-server results.
func (d *Daemon) handlePing(ctx context.Context, req Request) Response {
	if req.Profile == "" {
		return newError(req.ID, "ping: missing profile")
	}
	p, ok := d.store.Get(req.Profile)
	if !ok {
		return newError(req.ID, profile.ErrNotFound.Error())
	}
	results := d.pingServers(ctx, p.Servers)
	out := struct {
		Results []PingResult `json:"results"`
	}{Results: results}
	resp, err := newResult(req.ID, out)
	if err != nil {
		return newError(req.ID, err.Error())
	}
	return resp
}

// pingFanout caps how many per-node latency probes pingServers runs at once. The
// server list comes straight from a profile, and a profile can be an
// attacker-supplied subscription carrying thousands of nodes; without a bound the
// privileged daemon would fan out one concurrent dial per node, exhausting file
// descriptors/sockets from the root process on a single crafted ping. Sixteen
// keeps the probe effectively as fast as unbounded for any realistic profile
// (dials dominated by the network, not the pool) while capping a hostile one.
const pingFanout = 16

// pingServers TCP-dials each server's host:port and reports the dial latency,
// running at most pingFanout probes concurrently (see pingFanout). It is
// best-effort: an unreachable server yields ok=false with a zero RTT rather than
// failing the whole call. Results preserve input order — each goroutine writes
// its own index, so the worker pool never reorders them.
func (d *Daemon) pingServers(ctx context.Context, servers []profile.Server) []PingResult {
	results := make([]PingResult, len(servers))
	// A buffered channel of pingFanout tokens is the semaphore: acquiring before
	// the spawn (and blocking the loop when the pool is full) is what bounds the
	// number of in-flight dials, rather than spawning every goroutine up front.
	sem := make(chan struct{}, pingFanout)
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, srv profile.Server) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = d.pingOne(ctx, srv)
		}(i, srv)
	}
	wg.Wait()
	return results
}

// pingOne TCP-dials a single server within pingDialTimeout and reports the
// latency. A server missing a host or port, or one that fails to dial, comes
// back with ok=false rather than an error.
func (d *Daemon) pingOne(ctx context.Context, srv profile.Server) PingResult {
	res := PingResult{Node: srv.ID}
	if srv.Server == "" || srv.Port == 0 {
		return res
	}
	dialCtx, cancel := context.WithTimeout(ctx, pingDialTimeout)
	defer cancel()
	addr := net.JoinHostPort(srv.Server, strconv.Itoa(srv.Port))
	start := d.now()
	conn, err := d.dial(dialCtx, "tcp", addr)
	if err != nil {
		return res
	}
	res.RTTMs = d.now().Sub(start).Milliseconds()
	res.Ok = true
	_ = conn.Close()
	return res
}

// profileResult wraps a profile into the {profile: Profile} response shape used
// by the import_subscription / import_link / refresh_subscription commands. The
// profile is redacted (see redactProfile) before it goes on the wire: these
// replies travel the same local socket as list_profiles, so serialising the full
// stored profile would hand its node credentials and token-bearing subscription
// URL to any unprivileged reader — exactly what redacting list_profiles closed.
func (d *Daemon) profileResult(id int64, p profile.Profile) Response {
	out := struct {
		Profile redactedProfile `json:"profile"`
	}{Profile: redactProfile(p)}
	resp, err := newResult(id, out)
	if err != nil {
		return newError(id, err.Error())
	}
	return resp
}

// newResult0 is a successful empty response (no data), for commands that return
// nothing.
func newResult0(id int64) Response {
	return Response{ID: id, Ok: true}
}

// applyUserInfo folds a parsed Subscription-Userinfo header into a profile's
// traffic/expiry fields. A blank header leaves the profile untouched.
func applyUserInfo(p *profile.Profile, header string) {
	if header == "" {
		return
	}
	info, err := subscription.ParseUserInfo(header)
	if err != nil {
		return
	}
	p.TrafficUsed = info.Upload + info.Download
	p.TrafficTotal = info.Total
	if !info.Expire.IsZero() {
		e := info.Expire.UTC()
		p.ExpiresAt = &e
	}
}

// profileNodes converts a profile's servers into the model.Node slice the
// builder consumes, preserving order.
func profileNodes(p profile.Profile) []model.Node {
	nodes := make([]model.Node, len(p.Servers))
	for i, s := range p.Servers {
		nodes[i] = s.Node
	}
	return nodes
}

// serverIDs returns each server's stable ID in profile order, parallel to
// profileNodes, so the connect loop can map a node back to the server it came
// from when it folds a per-node transport strategy into that one node.
func serverIDs(p profile.Profile) []string {
	ids := make([]string, len(p.Servers))
	for i, s := range p.Servers {
		ids[i] = s.ID
	}
	return ids
}

// applyStrategyToNodes returns the node slice for one connect attempt with strat's
// handshake reshaping folded into the single node identified by targetID (matched
// through the parallel nodeIDs), leaving every other node untouched so the
// selector still lists them all. The default strategy reshapes nothing, so the
// original slice is returned as-is; otherwise the slice is copied and only the
// target node is replaced, so the loop's shared node slice is never mutated in
// place. A targetID absent from nodeIDs leaves the slice unchanged.
func applyStrategyToNodes(nodes []model.Node, nodeIDs []string, targetID string, strat fallback.Strategy) []model.Node {
	if strat.IsDefault() {
		return nodes
	}
	out := make([]model.Node, len(nodes))
	copy(out, nodes)
	for i := range out {
		if i < len(nodeIDs) && nodeIDs[i] == targetID {
			out[i] = strat.ApplyTo(out[i])
		}
	}
	return out
}

// buildCandidates turns a profile's servers into fallback attempts. An explicit
// node ID collapses the set to that single server, so a user who asked for a
// specific exit gets exactly it (and an error if it is unknown). Without an
// explicit node, every renderable server becomes a candidate and the fallback
// machine decides their order.
//
// Servers the builder can't render — a zero protocol, or one not in
// knownProtocol — are dropped: a config selecting such a node would silently
// fall back to a different outbound, so probing it would measure the wrong exit.
// Better to never attempt it. If that leaves nothing, connect fails with a clear
// message rather than dialling the wrong server.
func buildCandidates(p profile.Profile, explicit string) ([]fallback.Attempt, error) {
	if explicit != "" {
		for _, s := range p.Servers {
			if s.ID == explicit {
				if !renderable(s) {
					return nil, fmt.Errorf("connect: node %s has an unsupported protocol %q", explicit, s.Protocol)
				}
				return []fallback.Attempt{{NodeID: s.ID, Node: s.Node}}, nil
			}
		}
		return nil, fmt.Errorf("connect: node not found in profile")
	}

	cands := make([]fallback.Attempt, 0, len(p.Servers))
	for _, s := range p.Servers {
		if !renderable(s) {
			continue
		}
		cands = append(cands, fallback.Attempt{NodeID: s.ID, Node: s.Node})
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("connect: profile has no usable servers")
	}
	return cands, nil
}

// serverTags computes, for every renderable server in the profile, the sing-box
// selector tag it will carry, keyed by the server's stable ID. The builder
// derives each tag from the node name, uniquing collisions with a numeric suffix
// and skipping nodes it can't render — a zero/unknown protocol, or a known one
// that fails singbox.ValidateNode; this mirrors that walk exactly so the tag the
// loop hands Build as the selector default points at the intended exit and not a
// namesake. Getting it wrong would route through the wrong server, so the
// duplication is deliberate. Skipped servers get no entry, matching the builder
// dropping them.
func serverTags(p profile.Profile) map[string]string {
	seen := map[string]int{}
	uniq := func(name string) string {
		base := sanitizeTag(name)
		if seen[base] == 0 {
			seen[base] = 1
			return base
		}
		for {
			seen[base]++
			cand := fmt.Sprintf("%s-%d", base, seen[base])
			if seen[cand] == 0 {
				seen[cand] = 1
				return cand
			}
		}
	}

	tags := make(map[string]string, len(p.Servers))
	for _, s := range p.Servers {
		if s.Protocol == "" {
			continue // builder skips zero-protocol nodes; no tag assigned
		}
		tag := uniq(s.Node.Name)
		if !singbox.ValidateNode(s.Node) {
			// The builder validates every node before emitting it and, on failure,
			// allocates then frees its tag (delete(seen, tag) in buildNodes) — a
			// known protocol carrying bad params (out-of-range port, missing
			// credentials, keyless REALITY) is dropped exactly like an unknown one.
			// Skipping this check here (only knownProtocol below) would keep a tag
			// the builder never emits, drifting the suffix counter so a later tag —
			// and thus the selector default — points at the wrong exit.
			delete(seen, tag)
			continue
		}
		if !knownProtocol(s.Protocol) {
			// The builder allocates a tag then frees it for protocols it can't
			// render (delete(seen, tag) in buildNodes), so no tag survives and the
			// suffix counter is rolled back. Mirror that or every later tag drifts.
			delete(seen, tag)
			continue
		}
		tags[s.ID] = tag
	}
	return tags
}

// renderable reports whether the builder can turn this server into a working
// outbound: it must have a known, non-zero protocol AND carry the parameters
// that protocol needs (singbox.ValidateNode). A node with a known protocol but
// invalid params — a keyless REALITY VLESS, a zero/oversized port, missing
// credentials — is dropped by the builder, so it must not become a fallback
// candidate either: probing it would build a config that silently omits it and
// measure a different exit.
func renderable(s profile.Server) bool {
	return s.Protocol != "" && knownProtocol(s.Protocol) && singbox.ValidateNode(s.Node)
}

// knownProtocol reports whether the builder can render a node of this protocol
// into an outbound or endpoint. It must track the protocols handled in
// singbox.outbound and the AmneziaWG endpoint path; anything else the builder
// drops, freeing its tag.
func knownProtocol(p model.Protocol) bool {
	switch p {
	case model.VLESS, model.Hysteria2, model.Shadowsocks,
		model.Trojan, model.VMess, model.AmneziaWG:
		return true
	default:
		return false
	}
}

// sanitizeTag mirrors the builder's tag sanitisation (control chars and quotes
// become '-', blank becomes "node"). It is duplicated here, not imported,
// because it is an internal detail of the singbox package; this copy only needs
// to agree with it for the selector-default computation in nodesAndTag.
func sanitizeTag(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "node"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteByte('-')
		case r == '"' || r == '\\':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "node"
	}
	return out
}
