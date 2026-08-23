//go:build linux

// Package linux runs and supervises the sing-box process that backs the tunnel
// on Linux. It satisfies control.Runner: the control daemon hands it a config,
// it spawns sing-box, exposes traffic counters from the clash API, and reports
// the process exit.
//
// This is the Linux analog of adapters/windows and adapters/macos and is a
// near-copy of the latter on purpose: the clash API client, the log ring buffer,
// and the process supervisor are identical across the three desktop targets. It
// lives in its own linux-tagged package rather than reusing the windows adapter
// — which is deliberately build-tag-free and does compile and run here — because
// every diagnostic that adapter emits is prefixed "windows:", and a Linux user
// reading "windows: start sing-box: permission denied" in the app's log is being
// told something false about their own machine. Lifting the common half of the
// three into a shared package is a follow-up worth doing on its own; duplicating
// platform code behind an honest comment is a pattern the codebase already uses
// (see adapters/macos and control.sanitizeTag).
//
// The real differences from Windows are the device and the privilege. There is
// no wintun to place beside the binary: sing-box opens /dev/net/tun, which the
// kernel only hands out to a process with CAP_NET_ADMIN, and auto_route (plus
// strict_route, the kill switch) then installs routing rules and a firewall
// policy that need the same capability. So this adapter has no dll handling and
// instead records, before each launch, why an unprivileged run or a host without
// the tun module will not be able to bring the tunnel up.
package linux

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// defaultClashPort matches singbox.TunOptions' default external controller port,
// where the config tells sing-box to expose the clash API.
const defaultClashPort = 9090

// singboxEnv overrides binary resolution when set, so an operator (or a test, or
// the packaged service unit) can point at a specific sing-box build.
const singboxEnv = "TENEBRA_SINGBOX"

// logRingSize bounds the in-memory tail of sing-box output kept for diagnostics.
const logRingSize = 200

// statsTimeout keeps a clash API poll from blocking the traffic loop if the API
// is slow or not yet listening.
const statsTimeout = 2 * time.Second

// selectTimeout bounds the PUT that moves the selector. It is short on purpose:
// the call goes to loopback, so anything slower than this means the API is not
// answering, and a live exit switch that hangs is worse than one that fails fast
// and falls back to a reconnect.
const selectTimeout = 2 * time.Second

// probeURL is the target the clash API delay test fetches through the outbound.
// A 204 means real traffic reached the internet through that proxy; it is the
// canonical reachability check sing-box's own clash API exposes. HTTPS is used
// over plaintext http so the reachability check isn't a cleartext beacon on the
// wire: the clash delay test runs a full request through the outbound and times
// the response either way, so the TLS handshake changes nothing but the measured
// path, and gstatic serves the same 204 over https.
const probeURL = "https://www.gstatic.com/generate_204"

// probeTimeoutMs is the server-side timeout (in milliseconds) handed to the
// clash API delay test, matching the connect loop's per-attempt budget. The
// HTTP request itself is given a little more headroom so the local call doesn't
// time out before the API has a chance to answer with its own timeout verdict.
const probeTimeoutMs = 5000

// tunDevice is the character device sing-box opens to create the tun interface.
// It is provided by the tun kernel module; on a host where the module was never
// loaded, or in a container started without access to it, the node is simply
// absent and no amount of privilege conjures it.
const tunDevice = "/dev/net/tun"

// blocked is returned by Done before the first Start. It has no sender and is
// never closed, so a receive on it blocks forever — exactly the "Done must block
// before any Start" contract, without allocating a fresh channel per caller.
var blocked = make(chan error)

// Runner spawns and supervises one sing-box process at a time. The zero value is
// not usable; build it with New. It is safe for concurrent use: Stats and Done
// may be called while Start or Stop runs.
type Runner struct {
	// binOverride, when non-empty, is the sing-box path to run instead of the
	// one resolved next to the executable. New seeds it from TENEBRA_SINGBOX.
	binOverride string
	// ClashPort is the clash API port Stats queries; 0 means defaultClashPort.
	ClashPort int

	mu      sync.Mutex
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	done    chan error
	cfgPath string // temp config file for the running process, removed on stop
	ring    *ringBuffer
	// clashSecret is the clash API token baked into the running process's config.
	// Stats and Probe send it as a bearer so the authenticated external controller
	// answers them; set on each Start, read under mu.
	clashSecret string
}

// New builds a Runner with defaults: the sing-box binary is resolved from
// TENEBRA_SINGBOX or located next to the current executable, and the clash API
// is polled on port 9090. Both can be overridden afterwards via the exported
// field or by setting the environment before New.
func New() *Runner {
	return &Runner{
		binOverride: os.Getenv(singboxEnv),
		ClashPort:   defaultClashPort,
		ring:        newRingBuffer(logRingSize),
	}
}

// Start launches sing-box with configJSON. It returns once the process is
// spawned; the tunnel coming up (or failing) is observed through Done. Starting
// while a process is already running is rejected — the caller is expected to
// Stop first.
func (r *Runner) Start(ctx context.Context, configJSON []byte) error {
	bin, err := r.resolveSingbox()
	if err != nil {
		return err
	}

	// Which sing-box is about to run is worth a log line here and nowhere else:
	// it can come from the override, from beside the core, from a distribution's
	// private helper directory, or from PATH (see SingboxCandidates), and "which
	// one did it pick" is the first question any report about a version-specific
	// failure raises.
	r.ring.add("linux: running sing-box from " + bin)

	// The two ways a tun-mode connect fails before sing-box has said anything
	// useful are recorded here rather than turned into errors. Neither is fatal
	// to a Start: system-proxy mode needs no tun device and no capability at all,
	// so refusing to spawn would break the one mode that still works on a
	// locked-down host. When the config does ask for a tun, sing-box fails on its
	// own and that failure surfaces honestly through Done — these lines are what
	// let a reader of the log see why.
	for _, hint := range []string{elevationHint(), tunDeviceHint()} {
		if hint != "" {
			r.ring.add(hint)
		}
	}

	cfgPath, err := writeConfig(configJSON)
	if err != nil {
		return err
	}

	// The clash API secret travels inside the config we were handed; read it back
	// so Stats/Probe authenticate to the same controller this process exposes.
	secret := clashSecretFromConfig(configJSON)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		os.Remove(cfgPath)
		return errors.New("linux: sing-box already running")
	}

	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, bin, "run", "-c", cfgPath)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		os.Remove(cfgPath)
		return fmt.Errorf("linux: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		os.Remove(cfgPath)
		return fmt.Errorf("linux: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		os.Remove(cfgPath)
		return fmt.Errorf("linux: start sing-box: %w", err)
	}

	done := make(chan error, 1)
	r.cmd = cmd
	r.cancel = cancel
	r.done = done
	r.cfgPath = cfgPath
	r.clashSecret = secret

	// Drain both streams into the ring buffer; the goroutines end when the pipes
	// close on process exit.
	go r.scan(stdout)
	go r.scan(stderr)

	// One watcher owns Wait. It publishes the exit on done, closes it, and clears
	// the running state so the Runner can be started again.
	go func() {
		werr := cmd.Wait()
		cancel()
		os.Remove(cfgPath)

		r.mu.Lock()
		if r.cmd == cmd { // still the current process, not superseded
			r.cmd = nil
			r.cancel = nil
			r.cfgPath = ""
		}
		r.mu.Unlock()

		done <- werr
		close(done)
	}()

	return nil
}

// Stop terminates the running process and waits for it to exit. It is
// idempotent: with nothing running it returns nil.
func (r *Runner) Stop() error {
	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	running := r.cmd != nil
	r.mu.Unlock()

	if !running {
		return nil
	}
	if cancel != nil {
		cancel() // signals exec to kill the process group
	}
	// Wait for the watcher to observe the exit so Stop doesn't race ahead of
	// cleanup. done is buffered and always closed by the watcher.
	if done != nil {
		<-done
	}
	return nil
}

// Done reports the next process exit. Before any Start it blocks forever; after
// a Start it returns that run's channel, which delivers the exit error once and
// is then closed.
func (r *Runner) Done() <-chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done == nil {
		return blocked
	}
	return r.done
}

// Stats fetches cumulative byte counters from the clash API. The API only
// listens once sing-box has started, so an error here is expected during
// startup and treated as non-fatal by the caller.
func (r *Runner) Stats() (up, down int64, err error) {
	port := r.ClashPort
	if port == 0 {
		port = defaultClashPort
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/connections", port)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("linux: clash stats: %w", err)
	}
	setClashAuth(req, r.clashAuth())

	client := &http.Client{Timeout: statsTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("linux: clash stats: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("linux: clash stats: status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, 0, fmt.Errorf("linux: clash stats: read: %w", err)
	}
	return parseConnections(body)
}

// Probe asks the clash API to run a delay test through the named outbound: it
// fetches a known 204 endpoint via that proxy and reports the round-trip in
// milliseconds. A successful test is honest proof that traffic actually flows
// through tag — unlike "the process stayed up", it fails when the protocol is
// blocked, the handshake never completes, or the upstream is dead. A non-200
// from the API (which includes its own timeout, surfaced as a 408/504) or any
// transport error means the outbound is not usable.
//
// ctx bounds the whole call so the connect loop can abandon a probe when the
// connection is superseded; the clash API is also told its own timeout so it
// stops testing rather than holding the request open.
func (r *Runner) Probe(ctx context.Context, tag string) (delayMs int, err error) {
	return r.ProbeVia(ctx, tag, probeURL)
}

// ProbeVia is Probe against a caller-chosen destination: the same clash API
// delay test, but measuring whether THAT target survives the outbound named tag.
// It exists because "is this node alive" is not one question — a node can serve
// one control URL normally while black-holing everything the user cares about
// (see core/nodecheck), so the degradation watchdog measures several destinations
// through a candidate before moving the tunnel onto it. tag names any outbound in
// the running config, not just the selector, so a candidate exit can be measured
// without disturbing the one currently carrying traffic.
func (r *Runner) ProbeVia(ctx context.Context, tag, target string) (delayMs int, err error) {
	port := r.ClashPort
	if port == 0 {
		port = defaultClashPort
	}
	endpoint := delayURLFor(port, tag, target)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("linux: clash delay request: %w", err)
	}
	setClashAuth(req, r.clashAuth())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("linux: clash delay: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		// The API returns a message body (e.g. {"message":"An error occurred..."})
		// on a failed test; include a trimmed form so logs show why it failed.
		return 0, fmt.Errorf("linux: clash delay: status %s: %s", resp.Status, trimBody(body))
	}
	return parseDelay(body)
}

// Select points the running sing-box's selector group at the outbound named tag,
// over the same loopback clash API Stats and Probe already use. This is what
// makes an exit change seamless: the tun device, its routes and the sing-box
// process all stay exactly as they are, and only the selector's choice of
// downstream outbound moves, so nothing the OS knows about the tunnel is
// disturbed. The selector is built with interrupt_exist_connections off (see
// core/singbox), so connections already open keep the exit they were dialled
// through and only new ones take the new node.
//
// An error means the switch did NOT take — an unknown tag, an API not listening
// yet, a refused secret — and the caller is expected to fall back to a full
// reconnect rather than report a move that did not happen.
func (r *Runner) Select(ctx context.Context, group, tag string) error {
	port := r.ClashPort
	if port == 0 {
		port = defaultClashPort
	}
	body, err := json.Marshal(struct {
		Name string `json:"name"`
	}{tag})
	if err != nil {
		return fmt.Errorf("linux: clash select body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, selectURL(port, group), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("linux: clash select request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	setClashAuth(req, r.clashAuth())

	client := &http.Client{Timeout: selectTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("linux: clash select: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("linux: clash select: status %s: %s", resp.Status, trimBody(rb))
	}
	return nil
}

// delayURL builds the clash API delay-test endpoint for the outbound named tag,
// measured against the default probe target. The tag is path-escaped and the
// probe target query-escaped so a name or URL with reserved characters can't
// corrupt the request. It is split out from Probe so the URL shape can be
// asserted without a live API.
func delayURL(port int, tag string) string {
	return delayURLFor(port, tag, probeURL)
}

// delayURLFor is delayURL against an explicit target, which is what lets one
// candidate outbound be judged on several destinations rather than on one.
func delayURLFor(port int, tag, target string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/proxies/%s/delay?timeout=%d&url=%s",
		port, url.PathEscape(tag), probeTimeoutMs, url.QueryEscape(target))
}

// selectURL builds the clash API endpoint that points a selector group at one of
// its members. Split out like delayURL so the request shape is assertable
// offline, and path-escaped for the same reason: a node name carrying reserved
// characters must not be able to reshape the request.
func selectURL(port int, group string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/proxies/%s", port, url.PathEscape(group))
}

// parseDelay reads the {"delay":N} body the clash API returns from a successful
// delay test and yields the round-trip in milliseconds. It is split out from
// Probe so the parse can be tested without spawning sing-box or making an HTTP
// call.
func parseDelay(body []byte) (delayMs int, err error) {
	var out struct {
		Delay int `json:"delay"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("linux: parse clash delay: %w", err)
	}
	return out.Delay, nil
}

// trimBody renders a short, single-line form of an API error body for logs.
func trimBody(b []byte) string {
	const max = 200
	s := string(b)
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// clashAuth returns the clash API bearer secret for the running process, or ""
// when the config carried none (an unauthenticated API).
func (r *Runner) clashAuth() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clashSecret
}

// setClashAuth attaches the clash API secret as a bearer token when one is set.
// sing-box's external controller answers 401 to any request missing it once a
// secret is configured, which is what keeps other local processes off the
// tunnel's control surface.
func setClashAuth(req *http.Request, secret string) {
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
}

// clashSecretFromConfig extracts the clash API secret from a sing-box config so
// Stats and Probe can authenticate to the external controller the running
// process exposes. It returns "" for a config without one (or an unparseable
// config), in which case the callers send no Authorization header.
func clashSecretFromConfig(configJSON []byte) string {
	var cfg struct {
		Experimental struct {
			ClashAPI struct {
				Secret string `json:"secret"`
			} `json:"clash_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return ""
	}
	return cfg.Experimental.ClashAPI.Secret
}

// Logs returns a copy of the most recent sing-box output lines, newest last, for
// diagnostics.
func (r *Runner) Logs() []string {
	r.mu.Lock()
	ring := r.ring
	r.mu.Unlock()
	if ring == nil {
		return nil
	}
	return ring.snapshot()
}

// scan copies a process stream line by line into the ring buffer.
func (r *Runner) scan(rc io.ReadCloser) {
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		r.ring.add(sc.Text())
	}
}

// resolveSingbox returns the sing-box binary path: the override (env or field)
// if set, otherwise the first hit of the platform search order (FindSingbox).
// When nothing is found it still returns the neighbour path rather than an
// error, so the spawn fails naming a concrete location instead of an empty one —
// the caller reports that at connect time, which is where the user can act on it.
func (r *Runner) resolveSingbox() (string, error) {
	if r.binOverride != "" {
		return r.binOverride, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("linux: locate executable: %w", err)
	}
	exeDir := filepath.Dir(exe)
	if bin := FindSingbox(exeDir); bin != "" {
		return bin, nil
	}
	return filepath.Join(exeDir, singboxBinaryName()), nil
}

// singboxBinaryName is the expected sing-box filename on Linux. The release
// ships the binary without an extension, unlike the Windows sing-box.exe.
func singboxBinaryName() string {
	return "sing-box"
}

// InstallDirs lists, in probe order, the directories a Tenebra install may keep
// its private files in on Linux — the sing-box binary and the .srs rule-sets
// alike. It is exported because two callers must agree on it: this adapter,
// which resolves the binary, and the core's startup, which pins TENEBRA_SINGBOX
// and locates the rule-sets. Two independently maintained lists would drift, and
// a rule-set directory that disagrees with the binary's is a slow, silent
// failure (a ten-second remote download at every connect).
//
// Unlike Windows and macOS, "next to the executable" is not the answer here.
// Those two ship one self-contained directory — an installation folder, an .app
// bundle — while a Linux distribution package spreads the same payload across
// the filesystem hierarchy: the launcher lands in <prefix>/bin and its private
// helpers in a per-package directory whose name is a matter of distribution
// policy. So the order is:
//
//   - exeDir, which covers a dev checkout, an unpacked AppImage, and a
//     self-contained /opt-style install where everything sits together;
//   - <prefix>/lib/tenebra, <prefix>/libexec/tenebra and <prefix>/share/tenebra
//     derived from exeDir's parent, which covers a package installed under any
//     prefix (/usr, /usr/local, /opt/tenebra) without hard-coding one. lib and
//     libexec are the two conventions distributions split on for private
//     helpers; share is where architecture-independent data like the .srs
//     belongs, and is searched for the binary too rather than refusing to find
//     one a packager put there;
//   - the same three under /usr as an absolute backstop, for a core executable
//     that is not under <prefix>/bin at all — a wrapper's temp copy, or a
//     locally built binary run on a machine that has the package installed.
//
// The per-package directory is probed under both spellings, each with and
// without a resources/ subdirectory, because the two Linux packages we ship do
// not agree on it. The Arch PKGBUILD lays the payload out by hand and uses the
// lowercase package name (/usr/lib/tenebra/sing-box), while the .deb is written
// by Tauri's bundler, which derives the directory from the product name and
// nests bundled resources one level deeper (/usr/lib/Tenebra/resources/
// sing-box). Probing only the lowercase form was a real failure and not a
// cosmetic one: the GUI resolves resources through Tauri and would still find
// them, so a .deb install would look fine until the daemon — which has no such
// help — silently failed to find sing-box at connect time.
//
// Duplicates are dropped, so the list reads as an honest search path in a log.
func InstallDirs(exeDir string) []string {
	prefix := filepath.Dir(exeDir)
	dirs := []string{exeDir, filepath.Join(exeDir, "resources")}
	for _, base := range []string{prefix, "/usr"} {
		for _, private := range []string{"lib", "libexec", "share"} {
			for _, name := range []string{"tenebra", "Tenebra"} {
				dir := filepath.Join(base, private, name)
				dirs = append(dirs, dir, filepath.Join(dir, "resources"))
			}
		}
	}
	return dedupe(dirs)
}

// SingboxCandidates lists, in probe order, every path sing-box may live at on
// Linux: one per install directory, then whatever `sing-box` resolves to on
// PATH. The PATH entry last is what makes the distribution-dependency layout
// work — on a system where sing-box is a packaged dependency in /usr/bin rather
// than a binary Tenebra ships, it is the only place it will ever be found — and
// it is last so a version Tenebra installed alongside itself always wins over
// whatever else happens to be on PATH.
//
// An explicit TENEBRA_SINGBOX is not part of this list: it is an override that
// short-circuits the search entirely, before any candidate is probed.
func SingboxCandidates(exeDir string) []string {
	dirs := InstallDirs(exeDir)
	out := make([]string, 0, len(dirs)+1)
	for _, dir := range dirs {
		out = append(out, filepath.Join(dir, singboxBinaryName()))
	}
	if p, err := exec.LookPath(singboxBinaryName()); err == nil {
		out = append(out, p)
	}
	return dedupe(out)
}

// FindSingbox returns the first candidate that exists, or "" when none does.
// The caller decides what a miss means: the runner falls back to the neighbour
// path so its spawn error names one, and the daemon's startup leaves
// TENEBRA_SINGBOX unset so the runner gets to make that decision at connect time.
func FindSingbox(exeDir string) string {
	for _, p := range SingboxCandidates(exeDir) {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// dedupe removes repeated paths while preserving order. The install prefix
// derived from exeDir is very often /usr itself, which would otherwise make the
// absolute backstop a verbatim repeat of the entries just before it.
func dedupe(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := paths[:0:0]
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// elevationHint returns a short diagnostic note when the current process lacks
// the privilege the tun path needs, or "" when it is already root. It never
// blocks and never fakes a launch — it only explains, in the logs, why a
// non-elevated run will not be able to open the tunnel.
func elevationHint() string {
	return elevationHintFor(os.Geteuid())
}

// elevationHintFor is the pure core of elevationHint with the effective UID
// passed in, so the mapping can be tested without a privileged process.
//
// The euid test is a deliberate simplification: what the tun path actually needs
// is CAP_NET_ADMIN, which a file capability or an ambient set can grant to a
// non-root process. Reading the capability set would need a syscall for a
// message, and a false hint costs nothing — the line is advisory, sing-box still
// runs and still reports its own verdict — while missing the hint for the
// overwhelmingly common "user ran it unprivileged" case would cost the user the
// explanation. Root is the shipping arrangement: the daemon that serves the
// control socket runs as root and drives this runner.
func elevationHintFor(euid int) string {
	if euid == 0 {
		return ""
	}
	return "linux: sing-box needs CAP_NET_ADMIN (in practice, root) to open " + tunDevice +
		" and install auto_route's routing rules; start the tenebra.service systemd unit and let the GUI attach to its control socket"
}

// tunDeviceHint returns a diagnostic note when the tun device node is missing,
// or "" when it is present. Unlike the privilege hint this is not about the
// current process at all: /dev/net/tun is created by the tun kernel module, so
// its absence means the module is not loaded or the container was started
// without access to it, and no tun-mode connect can succeed until that is fixed.
func tunDeviceHint() string {
	return tunDeviceHintFor(tunDevice)
}

// tunDeviceHintFor is the injectable core of tunDeviceHint, so both outcomes can
// be tested without depending on how the host or CI container is configured.
func tunDeviceHintFor(path string) string {
	if _, err := os.Stat(path); err == nil {
		return ""
	}
	return "linux: " + path + " is missing; load the tun kernel module (modprobe tun) or grant the container access to it before connecting in tun mode"
}

// writeConfig writes config JSON to a temp file and returns its path. The caller
// (the watcher) removes it when the process exits.
func writeConfig(configJSON []byte) (string, error) {
	f, err := os.CreateTemp("", "tenebra-singbox-*.json")
	if err != nil {
		return "", fmt.Errorf("linux: create config file: %w", err)
	}
	if _, err := f.Write(configJSON); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("linux: write config file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("linux: close config file: %w", err)
	}
	return f.Name(), nil
}

// connections is the subset of the clash API /connections payload we read.
type connections struct {
	DownloadTotal int64 `json:"downloadTotal"`
	UploadTotal   int64 `json:"uploadTotal"`
}

// parseConnections extracts cumulative upload/download totals from a clash API
// /connections body.
func parseConnections(body []byte) (up, down int64, err error) {
	var c connections
	if err := json.Unmarshal(body, &c); err != nil {
		return 0, 0, fmt.Errorf("linux: parse clash stats: %w", err)
	}
	return c.UploadTotal, c.DownloadTotal, nil
}

// ringBuffer is a fixed-capacity FIFO of recent log lines, safe for concurrent
// writes from the two stream scanners and reads from Logs.
type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	cap   int
}

// newRingBuffer returns an empty ring that retains the most recent capacity
// lines.
func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{cap: capacity}
}

// add appends a line, dropping the oldest once the buffer is at capacity.
func (b *ringBuffer) add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lines) == b.cap {
		copy(b.lines, b.lines[1:])
		b.lines[len(b.lines)-1] = line
		return
	}
	b.lines = append(b.lines, line)
}

// snapshot returns a copy of the buffered lines, oldest first, so callers can
// read them without holding the lock or aliasing the backing array.
func (b *ringBuffer) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}
